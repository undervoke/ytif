package gate

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/affected"
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
	Base    string    // ci only: narrow test checks to projects changed since this ref
	Report  string    // "" or "github"
	Records string    // "" means <git-common-dir>/ytif/records.jsonl
	Context string    // "" detects ci or local
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
	// A base that does not resolve is a usage error, found before any
	// preparation or discovery starts.
	var base string
	if o.Base != "" {
		var err error
		if base, err = gitx.MergeBase(root, o.Base); err != nil {
			fmt.Fprintf(o.Stderr, "ytif: --base %s: %v\n", o.Base, err)
			return ExitUsage
		}
	}
	s, err := NewSurvey(ctx, root, o.Sources, o.Config, o.Stderr)
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
	narrowed := ""
	if base != "" {
		var dropped int
		selected, dropped = narrow(ctx, s, base, selected, o.Stderr)
		narrowed = fmt.Sprintf(" · %d unaffected since %.12s", dropped, base)
	}

	fmt.Fprintf(o.Stdout, "ytif %s · %d selected%s · %s %d files\n", o.Gate, len(selected), narrowed, in.Scope, len(in.Files))

	cancelledAt := make(chan time.Time, 1)
	stop := context.AfterFunc(ctx, func() { cancelledAt <- time.Now() })
	defer stop()
	run := collect(ctx, s, selected, in)
	findings := s.Findings()
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
	listing := check.Report{Invocations: s.Listing}
	run.reports = append([]check.Report{listing}, run.reports...)
	if err := writeRecords(root, o.Records, recordLines(o.Gate, ctxName, in.Scope, run, findings, cutoff)); err != nil {
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

// testRunners are the runners whose unit holds a check's inputs. A verify
// check's unit is only where it is written, so it is never narrowed.
var testRunners = map[string]bool{
	"go-test": true, "bun-test": true, "node-test": true,
	"dotnet-test": true, "vitest-test": true, "playwright-test": true,
}

// narrow drops test checks whose unit belongs to a project the changes since
// base do not affect. Without a graph it drops nothing and says why.
func narrow(ctx context.Context, s *Survey, base string, keys []check.Key, log io.Writer) ([]check.Key, int) {
	g, reason, invs, err := affected.Nx(ctx, s.Repo, base)
	s.Listing = append(s.Listing, invs...)
	if err != nil {
		reason = err.Error()
	}
	if g == nil {
		fmt.Fprintf(log, "ytif: nothing narrowed: %s\n", reason)
		return keys, 0
	}
	var kept []check.Key
	for _, k := range keys {
		if !testRunners[k.Runner] || !g.Unaffected(k.Unit) {
			kept = append(kept, k)
		}
	}
	return kept, len(keys) - len(kept)
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

// recordLines turns one run into records. After an interruption at cutoff,
// a process the interruption stopped leaves no records of its own, and its
// checks only those outcomes known to have come before the interruption.
func recordLines(gate, ctxName, scope string, run outcome, findings []reconcile.Finding, cutoff time.Time) []record.Line {
	now := time.Now().UTC()
	attempt := record.NewAttempt()
	base := record.Line{Time: now, Attempt: attempt, Gate: gate, Context: ctxName, Scope: scope}
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
