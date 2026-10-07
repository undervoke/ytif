package gate

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gitx"
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
	Gate    string    // commit, push, or ci
	Report  string    // "" or "github"
	Records string    // "" means <git-common-dir>/ytif/records.jsonl
	Context string    // "" detects ci or local
	Profile string    // "" runs every source; otherwise the execution adapter's profile
	Stdin   io.Reader // push only: pre-push ref lines
	Stdout  io.Writer
	Stderr  io.Writer
	Sources []check.Source
	Config  ConfigChecker
}

// dispatchError is a selected check that produced no outcome.
type dispatchError struct {
	key    check.Key
	detail string
}

// Run executes one gate and returns its exit code.
func Run(ctx context.Context, root string, o Options) int {
	if check.Rank(o.Gate) == 0 {
		fmt.Fprintf(o.Stderr, "ytif: unknown gate %q\n", o.Gate)
		return ExitUsage
	}
	if o.Profile != "" {
		return runProfile(ctx, root, o)
	}
	s, err := NewSurvey(ctx, root, o.Sources, o.Config, o.Stderr, nil)
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
	selected := selectKeys(s, o)

	fmt.Fprintf(o.Stdout, "ytif %s · %d selected · %s %d files\n", o.Gate, len(selected), in.Scope, len(in.Files))

	run := collect(ctx, s, selected, in)
	findings := s.Findings()
	listing := check.Report{Invocations: s.Listing}
	run.reports = append([]check.Report{listing}, run.reports...)
	return finish(ctx, o, root, in.Scope, run, findings)
}

// finish presents one run, writes its records, and returns its exit code.
func finish(ctx context.Context, o Options, root, scope string, run outcome, findings []reconcile.Finding) int {
	cancelledAt := make(chan time.Time, 1)
	stop := context.AfterFunc(ctx, func() { cancelledAt <- time.Now() })
	defer stop()
	exit := present(o, run, findings)
	var cutoff time.Time
	if ctx.Err() != nil {
		cutoff = <-cancelledAt
		fmt.Fprintf(o.Stdout, "ytif %s: interrupted; outcomes the interruption cut short are not recorded\n", o.Gate)
		exit = ExitFail
	}

	ctxName := o.Context
	if ctxName == "" {
		ctxName = detectContext()
	}
	prov := provenance{commit: gitx.Head(root), worktree: root, profile: o.Profile}
	if err := writeRecords(root, o.Records, recordLines(o.Gate, ctxName, scope, prov, run, findings, cutoff)); err != nil {
		// A run whose cost and outcomes are not recorded did not complete.
		fmt.Fprintf(o.Stderr, "ytif: records not written: %v\n", err)
		return ExitUsage
	}
	return exit
}

func bindInput(root string, o Options, tracked []string) (check.Input, error) {
	in := check.Input{Gate: o.Gate}
	var err error
	switch o.Gate {
	case check.GateCommit:
		in.Scope = check.ScopeStaged
		in.Files, err = gitx.Staged(root)
	case check.GatePush:
		in.Scope = check.ScopePushed
		in.Files, err = gitx.Pushed(root, o.Stdin)
	case check.GateCI:
		in.Scope, in.Files = check.ScopeTree, tracked
	}
	if in.Files == nil {
		in.Files = []string{} // an empty scope stays empty; it never widens to the tree
	}
	return in, err
}

// selectKeys applies cumulative placement.
func selectKeys(s *Survey, o Options) []check.Key {
	rank := check.Rank(o.Gate)
	var keys []check.Key
	for _, e := range s.Result.Managed {
		if check.Rank(e.Placement) <= rank {
			keys = append(keys, e.Key())
		}
	}
	return keys
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

func detectContext() string {
	switch strings.ToLower(os.Getenv("CI")) {
	case "", "0", "false":
		return "local"
	}
	return "ci"
}

// provenance identifies where a run executed. Empty fields mean unknown:
// records written before provenance existed, or a repository without
// commits yet.
type provenance struct {
	commit   string
	worktree string
	profile  string
}

// recordLines turns one run into records. After an interruption at cutoff,
// a process the interruption stopped leaves no records of its own, and its
// checks only those outcomes known to have come before the interruption.
// Skips are preserved as result lines with the skip outcome and no elapsed
// time, so the board can tell a skipped check from an unrecorded one.
func recordLines(gate, ctxName, scope string, prov provenance, run outcome, findings []reconcile.Finding, cutoff time.Time) []record.Line {
	now := time.Now().UTC()
	attempt := record.NewAttempt()
	base := record.Line{Time: now, Attempt: attempt, Gate: gate, Context: ctxName, Scope: scope,
		Commit: prov.commit, Worktree: prov.worktree, Profile: prov.profile}
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
			l.Detail = clip(r.Output)
			if r.Timed {
				l.ElapsedMS = record.Millis(r.Elapsed)
			}
			lines = append(lines, l)
		}
		for _, k := range rep.Skipped {
			l := base
			l.Kind, l.Key, l.Runner, l.Unit, l.Outcome = record.KindResult, k.String(), k.Runner, k.Unit, record.OutcomeSkip
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
