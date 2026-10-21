// Package gate runs verification gates through selection, discovery, dispatch and records.
package gate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/checkrun"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/proc"
	"github.com/undervoke/ytif/internal/reconcile"
)

// ConfigChecker reconciles gate configuration (lefthook, CI) with the rail.
type ConfigChecker interface {
	Check(ctx context.Context, repo check.Repo, exemptions inventory.Exemptions) []reconcile.Finding
}

// Survey is a repository's discovered checks and their reconciliation.
type Survey struct {
	Repo       check.Repo
	Inventory  inventory.Inventory
	Exemptions inventory.Exemptions
	Sources    []check.Source
	Owner      map[string]check.Source // runner → source
	Keys       map[string][]check.Key  // runner → discovered keys
	Failed     map[string]error        // runner → incomplete discovery
	Listing    []check.Invocation      // processes preparation, discovery, and narrowing started
	Result     reconcile.Result
	Config     []reconcile.Finding // nil when gate configuration was not checked
	Admission  []reconcile.Finding
	Active     map[string]bool
	logMu      sync.Mutex
}

// NewSurvey performs full discovery for list. Gate runs select active runners first.
func NewSurvey(ctx context.Context, root string, sources []check.Source, config ConfigChecker, log io.Writer) (*Survey, error) {
	s, err := loadSurvey(ctx, root, sources, config, log, DefaultJobs)
	if err != nil {
		return nil, err
	}
	if len(s.Admission) == 0 {
		for r := range s.Owner {
			s.Active[r] = true
		}
		in := check.Input{Mode: "full"}
		s.explore(proc.WithEnvironment(ctx, in.Environment()), in)
	} else {
		s.reconcile()
	}
	return s, nil
}

func loadSurvey(ctx context.Context, root string, sources []check.Source, config ConfigChecker, log io.Writer, jobs int) (*Survey, error) {
	tracked, err := gitx.Tracked(root)
	if err != nil {
		return nil, err
	}
	inv, err := inventory.LoadInventory(root)
	if err != nil {
		return nil, err
	}
	exemptions, err := inventory.LoadExemptions(root)
	if err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "ytif-")
	if err != nil {
		return nil, err
	}
	sources = append(append([]check.Source(nil), sources...), checkrun.NewSource(inv.Verifications, jobs))
	s := &Survey{
		Repo:       check.Repo{Root: root, Tracked: tracked, Scratch: scratch, Log: log},
		Inventory:  inv,
		Exemptions: exemptions,
		Sources:    sources,
		Owner:      map[string]check.Source{},
		Keys:       map[string][]check.Key{},
		Failed:     map[string]error{},
		Active:     map[string]bool{},
	}
	for _, src := range sources {
		for _, r := range src.Runners() {
			if s.Owner[r] != nil {
				panic("ytif: runner " + r + " has two sources")
			}
			s.Owner[r] = src
		}
	}
	for _, issue := range checkrun.CheckAdmission(root, inv.Verifications, inv.Prepare) {
		s.Admission = append(s.Admission, reconcile.Finding{Kind: reconcile.InvalidPlacement, Key: issue.Key, Location: issue.Location, Detail: issue.Detail})
	}
	for i, p := range inv.Prepare {
		for _, r := range p.Runners {
			if s.Owner[r] == nil {
				s.Admission = append(s.Admission, reconcile.Finding{Kind: reconcile.UnknownRunner, Location: fmt.Sprintf("%s: prepare[%d]", inventory.InventoryFile, i), Detail: "unknown runner " + r})
			}
		}
	}
	if config != nil {
		s.Config = config.Check(ctx, s.Repo, exemptions)
	}
	return s, nil
}

func (s *Survey) explore(ctx context.Context, in check.Input) {
	s.prepare(ctx, in)
	s.discover(ctx)
	s.reconcile()
}

func (s *Survey) reconcile() {
	known := map[string]bool{}
	for r := range s.Owner {
		known[r] = true
	}
	s.Result = reconcile.Reconcile(s.Inventory, known, s.Keys, s.Failed)
	s.Result.Findings = append(s.Result.Findings, s.Admission...)
	s.Result.Findings = append(s.Result.Findings, s.placementOrder()...)
	reconcile.Sort(s.Result.Findings)
}

// prepare runs only steps needed by a runner selected for discovery.
func (s *Survey) prepare(ctx context.Context, in check.Input) {
	for _, p := range s.Inventory.Prepare {
		needed := false
		for _, r := range p.Runners {
			needed = needed || s.Active[r]
		}
		if !needed {
			continue
		}
		command := strings.Join(p.Run, " ")
		cmd := exec.CommandContext(ctx, p.Run[0], p.Run[1:]...)
		cmd.Dir = s.Repo.Root
		cmd.Env = append(cmd.Environ(), in.Environment()...)
		cmd.Stdout, cmd.Stderr = s.Repo.Log, s.Repo.Log
		elapsed, err := proc.Run(ctx, cmd)
		s.Listing = append(s.Listing, check.Invocation{
			Runner: strings.Join(p.Runners, ","), Unit: command, What: "prepare",
			Elapsed: elapsed, Err: err, Interrupted: errors.Is(err, proc.ErrInterrupted),
		})
		if err != nil {
			for _, r := range p.Runners {
				if s.Active[r] {
					s.Failed[r] = fmt.Errorf("prepare %s: %w", command, err)
				}
			}
		}
	}
}

// discover runs active sources concurrently. A failed preparation is not retried.
func (s *Survey) discover(ctx context.Context) {
	type found struct {
		keys []check.Key
		invs []check.Invocation
		err  error
	}
	out := make([]found, len(s.Sources))
	started := make([]bool, len(s.Sources))
	var wg sync.WaitGroup
	for i, src := range s.Sources {
		active, prepared := false, false
		for _, r := range src.Runners() {
			active = active || s.Active[r]
			prepared = prepared || (s.Active[r] && s.Failed[r] == nil)
		}
		if !active || !prepared {
			continue
		}
		started[i] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			repo, flush := s.repoFor(src)
			defer flush()
			keys, invs, err := src.Discover(ctx, repo)
			out[i] = found{keys, invs, err}
		}()
	}
	wg.Wait()
	for i, src := range s.Sources {
		if !started[i] {
			continue
		}
		f := out[i]
		for _, r := range src.Runners() {
			s.Keys[r] = []check.Key{}
		}
		s.Listing = append(s.Listing, f.invs...)
		for _, k := range f.keys {
			if s.Owner[k.Runner] != src {
				f.err = fmt.Errorf("discovered key %s outside its runners", k)
				continue
			}
			s.Keys[k.Runner] = append(s.Keys[k.Runner], k)
		}
		if f.err != nil {
			for _, r := range src.Runners() {
				s.Failed[r] = f.err
			}
		}
	}
}

// repoFor returns the repository as src sees it while other sources run:
// its log lines carry src's runners. flush writes what src left of a line.
func (s *Survey) repoFor(src check.Source) (check.Repo, func()) {
	t := &tagged{mu: &s.logMu, dst: s.Repo.Log, prefix: []byte("[" + strings.Join(src.Runners(), ",") + "] ")}
	repo := s.Repo
	repo.Log = t
	return repo, t.flush
}

// placementOrder reports a co-run relation that cannot honor gate membership.
func (s *Survey) placementOrder() []reconcile.Finding {
	entries := map[check.Key]inventory.Entry{}
	for _, e := range s.Inventory.Verifications {
		entries[e.Key()] = e
	}
	var findings []reconcile.Finding
	for k, others := range coRuns(s) {
		e, ok := entries[k]
		if !ok {
			continue
		}
		for _, other := range others {
			o, ok := entries[other]
			if !ok {
				continue
			}
			for _, g := range []string{check.GateCheck, check.GateCommit, check.GatePush, check.GateCI} {
				if e.Member(g) && !o.Member(g) {
					findings = append(findings, reconcile.Finding{Kind: reconcile.PlacementOrder, Key: k, Detail: fmt.Sprintf("enabled at %s, but requires %s, which is not", g, other)})
				}
			}
		}
	}
	return findings
}

// Findings returns reconciliation and gate-configuration findings.
func (s *Survey) Findings() []reconcile.Finding {
	return append(append([]reconcile.Finding(nil), s.Result.Findings...), s.Config...)
}

// Close removes the scratch directory.
func (s *Survey) Close() {
	_ = os.RemoveAll(s.Repo.Scratch)
}
