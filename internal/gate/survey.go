// Package gate runs gates: discovery, reconciliation, cumulative selection,
// dispatch, reporting, and records.
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
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/proc"
	"github.com/undervoke/ytif/internal/reconcile"
)

// ConfigChecker reconciles gate configuration (lefthook, CI) with the rail.
type ConfigChecker interface {
	Check(ctx context.Context, repo check.Repo, routing inventory.Routing) []reconcile.Finding
}

// Survey is a repository's discovered checks and their reconciliation.
type Survey struct {
	Repo      check.Repo
	Inventory inventory.Inventory
	Routing   inventory.Routing
	Sources   []check.Source
	Owner     map[string]check.Source // runner → source
	Keys      map[string][]check.Key  // runner → discovered keys
	Failed    map[string]error        // runner → incomplete discovery
	Listing   []check.Invocation      // processes preparation, discovery, and narrowing started
	Result    reconcile.Result
	Config    []reconcile.Finding // nil when gate configuration was not checked
}

// NewSurvey discovers every source's checks and reconciles them with the
// inventory and, when config is set, the gate configuration. An error means
// the inventory or routing list cannot be read. Close removes the scratch
// directory.
func NewSurvey(ctx context.Context, root string, sources []check.Source, config ConfigChecker, log io.Writer) (*Survey, error) {
	tracked, err := gitx.Tracked(root)
	if err != nil {
		return nil, err
	}
	inv, err := inventory.LoadInventory(root)
	if err != nil {
		return nil, err
	}
	routing, err := inventory.LoadRouting(root)
	if err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "ytif-")
	if err != nil {
		return nil, err
	}
	s := &Survey{
		Repo:      check.Repo{Root: root, Tracked: tracked, Scratch: scratch, Log: log},
		Inventory: inv,
		Routing:   routing,
		Sources:   sources,
		Owner:     map[string]check.Source{},
		Keys:      map[string][]check.Key{},
		Failed:    map[string]error{},
	}
	for _, src := range sources {
		for _, r := range src.Runners() {
			if s.Owner[r] != nil {
				panic("ytif: runner " + r + " has two sources")
			}
			s.Owner[r] = src
		}
	}
	if err := s.prepare(ctx); err != nil {
		s.Close()
		return nil, err
	}
	s.discover(ctx)
	known := map[string]bool{}
	for r := range s.Owner {
		known[r] = true
	}
	s.Result = reconcile.Reconcile(inv, known, s.Keys, s.Failed)
	s.Result.Findings = append(s.Result.Findings, s.placementOrder()...)
	reconcile.Sort(s.Result.Findings)
	if config != nil {
		s.Config = config.Check(ctx, s.Repo, routing)
		reconcile.Sort(s.Config)
	}
	return s, nil
}

// prepare runs the inventory's preparation commands in order, before any
// discovery. A failed command fails discovery of its runners, so the gate
// fails as it does for any incomplete discovery.
func (s *Survey) prepare(ctx context.Context) error {
	for i, p := range s.Inventory.Prepare {
		for _, r := range p.Runners {
			if s.Owner[r] == nil {
				return fmt.Errorf("%s: prepare[%d]: unknown runner %q", inventory.InventoryFile, i, r)
			}
		}
	}
	for _, p := range s.Inventory.Prepare {
		command := strings.Join(p.Run, " ")
		cmd := exec.CommandContext(ctx, p.Run[0], p.Run[1:]...)
		cmd.Dir = s.Repo.Root
		cmd.Stdout, cmd.Stderr = s.Repo.Log, s.Repo.Log
		start := time.Now()
		err := proc.Run(cmd)
		s.Listing = append(s.Listing, check.Invocation{
			Runner: strings.Join(p.Runners, ","), Unit: command, What: "prepare",
			Elapsed: time.Since(start), Err: err, Interrupted: errors.Is(err, proc.ErrInterrupted),
		})
		if err != nil {
			for _, r := range p.Runners {
				s.Failed[r] = fmt.Errorf("prepare %s: %w", command, err)
			}
		}
	}
	return nil
}

// discover runs every source's discovery concurrently. A source whose
// runners all failed preparation is not discovered.
func (s *Survey) discover(ctx context.Context) {
	type found struct {
		keys []check.Key
		invs []check.Invocation
		err  error
	}
	out := make([]found, len(s.Sources))
	var wg sync.WaitGroup
	for i, src := range s.Sources {
		unprepared := true
		for _, r := range src.Runners() {
			unprepared = unprepared && s.Failed[r] != nil
		}
		if unprepared {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys, invs, err := src.Discover(ctx, s.Repo)
			out[i] = found{keys, invs, err}
		}()
	}
	wg.Wait()
	for i, src := range s.Sources {
		f := out[i]
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

// placementOrder reports managed checks that necessarily run a managed
// check placed at a later gate, which would run it outside its placement.
func (s *Survey) placementOrder() []reconcile.Finding {
	placement := map[check.Key]string{}
	for _, e := range s.Result.Managed {
		placement[e.Key()] = e.Placement
	}
	var findings []reconcile.Finding
	for _, src := range s.Sources {
		co, ok := src.(check.CoRunner)
		if !ok {
			continue
		}
		for k, others := range co.CoRuns() {
			p, ok := placement[k]
			if !ok {
				continue
			}
			for _, o := range others {
				if q, ok := placement[o]; ok && check.Rank(q) > check.Rank(p) {
					findings = append(findings, reconcile.Finding{Kind: reconcile.PlacementOrder, Key: k,
						Detail: fmt.Sprintf("placed at %s, but running it also runs %s, placed at %s; place that at %s or earlier", p, o, q, p)})
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
