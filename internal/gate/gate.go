package gate

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/affected"
	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/proc"
	"github.com/undervoke/ytif/internal/reconcile"
	"github.com/undervoke/ytif/internal/record"
)

// Exit codes.
const (
	ExitPass  = 0
	ExitFail  = 1
	ExitUsage = 2
)

// Options configures one gate run.
type Options struct {
	Gate    string    // check, commit, push, or ci
	Base    string    // affected comparison base; empty selects full
	Jobs    int       // processes run at once; 0 means DefaultJobs
	Report  string    // "" or "github"
	Record  bool      // append records for this trigger
	Records string    // "" means <git-common-dir>/ytif/records.jsonl
	Stdin   io.Reader // push only: pre-push ref lines
	Stdout  io.Writer
	Stderr  io.Writer
	Sources []check.Source
	Config  ConfigChecker
}

// DefaultJobs is how many processes a gate runs at once unless told.
var DefaultJobs = runtime.NumCPU()

// dispatchError is a selected check that produced no outcome.
type dispatchError struct {
	key    check.Key
	detail string
}

// Run executes one gate and returns its exit code.
func Run(ctx context.Context, root string, o Options) int {
	if !check.ValidGate(o.Gate) {
		fmt.Fprintf(o.Stderr, "ytif: unknown gate %q\n", o.Gate)
		return ExitUsage
	}
	jobs := o.Jobs
	if jobs == 0 {
		jobs = DefaultJobs
		if o.Gate == check.GateCheck {
			jobs = 2
		}
	}
	ctx = proc.WithLimit(ctx, jobs)
	snapshot, err := gitx.SelectionSnapshot(root, o.Gate, o.Base, o.Stdin)
	if err != nil {
		fmt.Fprintf(o.Stderr, "ytif: %v\n", err)
		return ExitUsage
	}
	s, err := loadSurvey(ctx, root, o.Sources, o.Config, o.Stderr, jobs)
	if err != nil {
		fmt.Fprintf(o.Stderr, "ytif: %v\n", err)
		return ExitUsage
	}
	defer s.Close()

	in, err := bindInput(root, o, s.Repo.Tracked)
	if err != nil {
		fmt.Fprintf(o.Stderr, "ytif: %v\n", err)
		return ExitUsage
	}
	mode := "full"
	if o.Base != "" {
		mode = "affected"
	}
	selection := record.Selection{
		RequestedMode:    mode,
		EffectiveMode:    mode,
		Branch:           snapshot.Branch,
		Base:             snapshot.Base,
		Head:             snapshot.Head,
		Bases:            snapshot.Bases,
		Tips:             snapshot.Tips,
		MergeParents:     snapshot.MergeParents,
		DirtyDigest:      snapshot.DirtyDigest,
		RevisionMismatch: snapshot.RevisionMismatch,
		Registered:       len(s.Inventory.Verifications),
		Eligible:         []string{},
		Selected:         []string{},
	}
	eligible := map[check.Key]bool{}
	var selected []check.Key
	for _, e := range s.Inventory.Verifications {
		if e.Member(o.Gate) {
			eligible[e.Key()] = true
			selection.Eligible = append(selection.Eligible, e.Key().String())
			selected = append(selected, e.Key())
		}
	}
	if snapshot.RevisionMismatch {
		fmt.Fprintf(o.Stderr, "ytif: results reflect the working tree at %s, not necessarily pushed tips %s\n", snapshot.Head, strings.Join(snapshot.Tips, ", "))
	}
	if len(s.Admission) == 0 && mode == "affected" {
		g, reason, invs, nxErr := affected.NxFiles(ctx, s.Repo, snapshot.Changed)
		s.Listing = append(s.Listing, invs...)
		if nxErr != nil {
			reason = nxErr.Error()
		}
		if g == nil {
			selection.EffectiveMode, selection.Fallback = "full", reason
			fmt.Fprintf(o.Stderr, "ytif: affected unavailable; running gate members: %s\n", reason)
		} else {
			kept := make([]check.Key, 0, len(selected))
			for _, k := range selected {
				if k.Runner == inventory.CommandRunner || !g.Unaffected(k.Unit) {
					kept = append(kept, k)
				}
			}
			selected = kept
		}
	}
	in.Mode, in.Base, in.Head = selection.EffectiveMode, snapshot.Base, snapshot.Head
	ctx = proc.WithEnvironment(ctx, in.Environment())
	if len(s.Admission) == 0 {
		for _, k := range selected {
			s.Active[k.Runner] = true
		}
		if o.Gate == check.GateCI && selection.EffectiveMode == "full" {
			for r := range s.Owner {
				s.Active[r] = true
			}
		}
		s.explore(ctx, in)
		selected = closeCoRuns(selected, eligible, coRuns(s))
	} else {
		s.reconcile()
	}
	wanted := map[check.Key]bool{}
	for _, k := range selected {
		wanted[k] = true
		selection.Selected = append(selection.Selected, k.String())
	}
	for _, e := range s.Inventory.Verifications {
		if wanted[e.Key()] {
			continue
		}
		reason := "unaffected"
		if !eligible[e.Key()] {
			reason = "not-in-gate"
			if o.Gate == check.GatePush && e.SkipPush {
				reason = "skip-push"
			}
		}
		selection.Omissions = append(selection.Omissions, record.Omission{Key: e.Key().String(), Reason: reason})
	}
	fmt.Fprintf(o.Stdout, "ytif %s · %s · %d selected / %d eligible · %s %d files · jobs %d\n", o.Gate, selection.EffectiveMode, len(selected), len(eligible), in.Scope, len(in.Files), jobs)

	cancelledAt := make(chan time.Time, 1)
	stop := context.AfterFunc(ctx, func() { cancelledAt <- time.Now() })
	defer stop()
	var run outcome
	if len(s.Admission) == 0 {
		managed := map[check.Key]bool{}
		for _, e := range s.Result.Managed {
			managed[e.Key()] = true
		}
		var runnable []check.Key
		for _, k := range selected {
			if managed[k] {
				runnable = append(runnable, k)
			}
		}
		run = collect(ctx, s, runnable, in)
	}
	findings := s.Findings()
	exit := present(o, run, findings)
	if len(s.Admission) > 0 {
		exit = ExitUsage
	}
	var cutoff time.Time
	if ctx.Err() != nil {
		cutoff = <-cancelledAt
		fmt.Fprintf(o.Stdout, "ytif %s: interrupted; unfinished outcomes are not recorded\n", o.Gate)
		exit = ExitFail
	}
	selection.Completed = ctx.Err() == nil && len(s.Admission) == 0
	if !o.Record {
		return exit
	}
	run.reports = append([]check.Report{{Invocations: s.Listing}}, run.reports...)
	lines := recordLines(o.Gate, in.Scope, run, findings, cutoff)
	attempt, now := record.NewAttempt(), time.Now().UTC()
	if len(lines) > 0 {
		attempt, now = lines[0].Attempt, lines[0].Time
	}
	status := "pass"
	if exit != ExitPass {
		status = "fail"
	}
	lines = append(lines, record.Line{Time: now, Attempt: attempt, Kind: record.KindSelection, Gate: o.Gate, Scope: in.Scope, Outcome: status, Selection: &selection})
	if err := writeRecords(root, o.Records, lines); err != nil {
		fmt.Fprintf(o.Stderr, "ytif: records not written: %v\n", err)
		return ExitUsage
	}
	return exit
}

func bindInput(root string, o Options, tracked []string) (check.Input, error) {
	in := check.Input{Gate: o.Gate, Scope: check.ScopeTree, Files: tracked}
	var err error
	if o.Gate == check.GateCommit {
		in.Scope = check.ScopeStaged
		in.Files, err = gitx.Staged(root)
	}
	if in.Files == nil {
		in.Files = []string{}
	}
	return in, err
}

// closeCoRuns restores unaffected partners only within the gate's membership.
func closeCoRuns(keys []check.Key, eligible map[check.Key]bool, co map[check.Key][]check.Key) []check.Key {
	seen := map[check.Key]bool{}
	for _, k := range keys {
		seen[k] = true
	}
	out := append([]check.Key(nil), keys...)
	for i := 0; i < len(out); i++ {
		for _, partner := range co[out[i]] {
			if eligible[partner] && !seen[partner] {
				seen[partner] = true
				out = append(out, partner)
			}
		}
	}
	return out
}

func ownedBy(s *Survey, src check.Source, keys []check.Key) []check.Key {
	var out []check.Key
	for _, k := range keys {
		if s.Owner[k.Runner] == src {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out
}

// recordLines turns one run into records. After an interruption at cutoff,
// a process the interruption stopped leaves no records of its own, and its
// checks only those outcomes known to have come before the interruption.
func recordLines(gate, scope string, run outcome, findings []reconcile.Finding, cutoff time.Time) []record.Line {
	now := time.Now().UTC()
	attempt := record.NewAttempt()
	base := record.Line{Time: now, Attempt: attempt, Gate: gate, Scope: scope}
	stopped := map[check.Key]bool{}
	for _, rep := range run.reports {
		for _, inv := range rep.Invocations {
			if inv.Interrupted {
				for _, k := range inv.Keys {
					stopped[k] = true
				}
			}
		}
	}
	var lines []record.Line
	for _, rep := range run.reports {
		for _, r := range rep.Results {
			if stopped[r.Key] && (r.Ended.IsZero() || !r.Ended.Before(cutoff)) {
				continue
			}
			l := base
			l.Kind, l.Key, l.Runner, l.Unit, l.Outcome = record.KindResult, r.Key.String(), r.Key.Runner, r.Key.Unit, string(r.Outcome)
			if r.Timed {
				l.ElapsedMS = record.Millis(r.Elapsed)
			}
			lines = append(lines, l)
		}
		for _, inv := range rep.Invocations {
			if inv.Interrupted {
				continue
			}
			l := base
			l.Kind, l.Runner, l.Unit, l.What = record.KindInvocation, inv.Runner, inv.Unit, inv.What
			l.ElapsedMS = record.Millis(inv.Elapsed)
			if inv.Err != nil {
				l.Detail = clip(inv.Err.Error())
			}
			lines = append(lines, l)
		}
	}
	for _, d := range run.dispatch {
		if stopped[d.key] {
			continue
		}
		l := base
		l.Kind, l.Key, l.Runner, l.Unit, l.Detail = record.KindDispatch, d.key.String(), d.key.Runner, d.key.Unit, clip(d.detail)
		lines = append(lines, l)
	}
	for _, p := range run.problems {
		l := base
		l.Kind, l.Detail = record.KindDispatch, clip(p)
		lines = append(lines, l)
	}
	for _, f := range findings {
		l := base
		l.Kind, l.Outcome, l.Detail = record.KindReconcile, string(f.Kind), f.String()
		if f.Key != (check.Key{}) {
			l.Key, l.Runner, l.Unit = f.Key.String(), f.Key.Runner, f.Key.Unit
		} else {
			l.Runner = f.Runner
		}
		lines = append(lines, l)
	}
	return lines
}

// clip bounds the detail a record keeps; full output belongs to the gate's
// console, while records serve statistics.
func clip(s string) string {
	const max = 500
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "…"
}

func writeRecords(root, path string, lines []record.Line) error {
	if path == "" {
		common, err := gitx.CommonDir(root)
		if err != nil {
			return err
		}
		path = record.DefaultPath(common)
	}
	return record.Append(path, lines)
}
