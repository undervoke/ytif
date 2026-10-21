// Package cli parses the ytif command line.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/allow"
	"github.com/undervoke/ytif/internal/board"
	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gate"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/guard"
	"github.com/undervoke/ytif/internal/initcmd"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/launch"
	"github.com/undervoke/ytif/internal/record"
)

const usageText = `usage: ytif <command> [flags]

gates:
  check                  run verifications enabled at check
  commit                 run verifications enabled at commit
  push                   run verifications except skip-push
  ci                     run all verifications

other:
  migrate                convert the legacy inventory to ytif-verifications.yaml
  init [--hosts LIST]     pin this ytif, wire gates, and register selected agent hosts
  list                   show verifications and reconciliation findings
  stats                  show recent elapsed time and hits per verification
  board [--out PATH]     write the verification board as one HTML file and open it
  guard [--host cursor]   agent hook: refuse direct verification runs
  allow [DURATION|--off] let agents run verifications themselves for DURATION
  update                 replace the ytif on PATH with the latest release
  version                print the version

Inside a Go module that pins ytif, commands other than update, init and migrate
run the pinned version.

gate flags:
  --report github        emit GitHub Actions annotations
  --records PATH         append records to PATH instead of the git common dir
  --base REF             select Nx-affected verifications; absent means full
  --jobs N               bound concurrent processes (check: 2; other gates: CPU count)
`

// Run executes one command and returns its exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return gate.ExitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd, rest := args[0], args[1:]
	switch cmd {
	case check.GateCheck, check.GateCommit, check.GatePush, check.GateCI:
		return runGate(ctx, cmd, rest, stdin, stdout, stderr)
	case "migrate":
		if len(rest) != 0 {
			fmt.Fprintln(stderr, "ytif migrate: takes no arguments")
			return gate.ExitUsage
		}
		root, code := repoRoot(stderr)
		if code != 0 {
			return code
		}
		if err := inventory.Migrate(root, stdout); err != nil {
			fmt.Fprintf(stderr, "ytif migrate: %v\n", err)
			return gate.ExitUsage
		}
		return gate.ExitPass
	case "init":
		return runInit(rest, stdin, stdout, stderr, version)
	case "list":
		return runList(ctx, rest, stdout, stderr)
	case "stats":
		return runStats(rest, stdout, stderr)
	case "board":
		return runBoard(rest, stdout, stderr)
	case "guard":
		return runGuard(rest, stdin, stdout, stderr)
	case "allow":
		return runAllow(rest, stdout, stderr)
	case "update":
		if len(rest) != 0 {
			fmt.Fprintf(stderr, "ytif update: takes no arguments\n%s", usageText)
			return gate.ExitUsage
		}
		return launch.Update(stdout, stderr, version)
	case "version", "--version":
		fmt.Fprintln(stdout, version)
		return gate.ExitPass
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return gate.ExitPass
	}
	fmt.Fprintf(stderr, "ytif: unknown command %q\n%s", cmd, usageText)
	return gate.ExitUsage
}

func runGate(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs, report, records := gateFlags(name, stderr)
	defaultJobs := gate.DefaultJobs
	if name == check.GateCheck {
		defaultJobs = 2
	}
	jobs := fs.Int("jobs", defaultJobs, "run at most N processes at once")
	base := fs.String("base", "", "select Nx-affected verifications since REF")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError(stderr, fs, "takes no arguments")
	}
	if *report != "" && *report != "github" {
		return usageError(stderr, fs, fmt.Sprintf("--report %q: want github", *report))
	}
	if *jobs < 1 {
		return usageError(stderr, fs, fmt.Sprintf("--jobs %d: want 1 or more", *jobs))
	}
	root, code := repoRoot(stderr)
	if code != 0 {
		return code
	}
	var refs io.Reader
	if name == check.GatePush && pipedStdin(stdin) {
		refs = stdin
	}
	return gate.Run(ctx, root, gate.Options{
		Gate: name, Base: deref(base), Jobs: *jobs, Report: *report, Records: *records, Stdin: refs,
		Stdout: stdout, Stderr: stderr, Sources: sources(), Config: configChecker(),
	})
}

func runList(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	summary := fs.Bool("summary", false, "print only counts")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError(stderr, fs, "takes no arguments")
	}
	root, code := repoRoot(stderr)
	if code != 0 {
		return code
	}
	return gate.List(ctx, root, sources(), configChecker(), stdout, stderr, *summary)
}

func runStats(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	last := fs.Int("last", 20, "average over the last N observations of each check")
	gateName := fs.String("gate", "", "only this lane (check, commit, push, ci)")
	ctxName := fs.String("context", "", "only this context (local, ci, agent)")
	var paths multiFlag
	fs.Var(&paths, "records", "record file to read (repeatable); default: local records")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError(stderr, fs, "takes no arguments")
	}
	if len(paths) == 0 {
		root, code := repoRoot(stderr)
		if code != 0 {
			return code
		}
		common, err := gitx.CommonDir(root)
		if err != nil {
			fmt.Fprintf(stderr, "ytif: %v\n", err)
			return gate.ExitUsage
		}
		paths = multiFlag{record.DefaultPath(common)}
	}
	lines, malformed, err := record.Read(paths)
	if err != nil {
		fmt.Fprintf(stderr, "ytif: %v\n", err)
		return gate.ExitUsage
	}
	checks, invs, guards := record.Stats(lines, record.Filter{Gate: *gateName, Context: *ctxName}, *last)
	avgHead := fmt.Sprintf("AVG(last %d)", *last)
	fmt.Fprintf(stdout, "%-6s %-6s %-12s %-7s %-7s %s\n", "RUNS", "HITS", avgHead, "GATE", "CONTEXT", "VERIFICATION")
	for _, c := range checks {
		fmt.Fprintf(stdout, "%-6d %-6d %-12s %-7s %-7s %s\n", c.Runs, c.Hits, avg(c.AvgMS), c.Gate, c.Context, c.Key)
	}
	if len(invs) > 0 {
		fmt.Fprintf(stdout, "\n%-6s %-12s %-7s %-7s %s\n", "RUNS", avgHead, "GATE", "CONTEXT", "INVOCATION")
		for _, i := range invs {
			fmt.Fprintf(stdout, "%-6d %-12s %-7s %-7s %s\n", i.Runs, avg(i.AvgMS), i.Gate, i.Context,
				strings.TrimSpace(i.Runner+" "+i.What+" "+i.Unit))
		}
	}
	if len(guards) > 0 {
		fmt.Fprintf(stdout, "\n%-9s %-7s %s\n", "REFUSALS", "CONTEXT", "GUARDED RUNNER")
		for _, g := range guards {
			fmt.Fprintf(stdout, "%-9d %-7s %s\n", g.Refusals, g.Context, g.Runner)
		}
	}
	if malformed > 0 {
		fmt.Fprintf(stderr, "ytif: skipped %d malformed record lines\n", malformed)
	}
	return gate.ExitPass
}

func runBoard(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "write the board to PATH and do not open it")
	var paths multiFlag
	fs.Var(&paths, "records", "record file to read (repeatable); default: local records")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError(stderr, fs, "takes no arguments")
	}
	root, code := repoRoot(stderr)
	if code != 0 {
		return code
	}
	common, err := gitx.CommonDir(root)
	if err != nil {
		fmt.Fprintf(stderr, "ytif: %v\n", err)
		return gate.ExitUsage
	}
	if len(paths) == 0 {
		paths = multiFlag{record.DefaultPath(common)}
	}
	lines, malformed, err := record.Read(paths)
	if err != nil {
		fmt.Fprintf(stderr, "ytif: %v\n", err)
		return gate.ExitUsage
	}
	if malformed > 0 {
		fmt.Fprintf(stderr, "ytif: skipped %d malformed record lines\n", malformed)
	}
	html, err := board.Render(board.Assets, root, lines, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "ytif: %v\n", err)
		return gate.ExitUsage
	}
	path := *out
	if path == "" {
		path = board.DefaultPath(common)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(stderr, "ytif: %v\n", err)
		return gate.ExitUsage
	}
	if err := os.WriteFile(path, html, 0o644); err != nil {
		fmt.Fprintf(stderr, "ytif: %v\n", err)
		return gate.ExitUsage
	}
	fmt.Fprintln(stdout, path)
	if *out == "" {
		if err := board.Open(path); err != nil {
			fmt.Fprintf(stderr, "ytif: open the board yourself: %v\n", err)
		}
	}
	return gate.ExitPass
}

func runAllow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("allow", flag.ContinueOnError)
	fs.SetOutput(stderr)
	off := fs.Bool("off", false, "withdraw the permission now")
	if err := fs.Parse(args); err != nil || fs.NArg() > 1 || *off && fs.NArg() != 0 {
		return usageError(stderr, fs, "takes a DURATION or --off")
	}
	root, code := repoRoot(stderr)
	if code != 0 {
		return code
	}
	common, err := gitx.CommonDir(root)
	if err != nil {
		fmt.Fprintf(stderr, "ytif: %v\n", err)
		return gate.ExitUsage
	}
	now := time.Now()
	switch {
	case *off:
		err = allow.Clear(common)
	case fs.NArg() == 1:
		d, perr := time.ParseDuration(fs.Arg(0))
		if perr != nil || d <= 0 {
			return usageError(stderr, fs, fmt.Sprintf("%q: want a positive duration such as 8h or 90m", fs.Arg(0)))
		}
		err = allow.Set(common, now.Add(d))
	}
	if err != nil {
		fmt.Fprintf(stderr, "ytif: %v\n", err)
		return gate.ExitUsage
	}
	if until, ok := allow.Until(common, now); ok {
		fmt.Fprintf(stdout, "agents may run checks until %s\n", until.Local().Format("2006-01-02 15:04 MST"))
	} else {
		fmt.Fprintln(stdout, "agents may not run checks")
	}
	return gate.ExitPass
}

func gateFlags(name string, stderr io.Writer) (*flag.FlagSet, *string, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	report := fs.String("report", "", "annotation format: github")
	records := fs.String("records", "", "append records to this file")
	return fs, report, records
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func repoRoot(stderr io.Writer) (string, int) {
	root, err := gitx.Root(".")
	if err != nil {
		fmt.Fprintf(stderr, "ytif: not inside a git repository: %v\n", err)
		return "", gate.ExitUsage
	}
	return root, 0
}

func usageError(stderr io.Writer, fs *flag.FlagSet, msg string) int {
	fmt.Fprintf(stderr, "ytif %s: %s\n%s", fs.Name(), msg, usageText)
	return gate.ExitUsage
}

// pipedStdin reports whether stdin carries data from a pipe or file rather
// than a terminal, so a manual push run never blocks on keyboard input.
func pipedStdin(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return r != nil
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice == 0
}

func avg(ms *float64) string {
	switch {
	case ms == nil:
		return "-"
	case *ms < 1:
		return "<1ms"
	}
	return time.Duration(*ms * float64(time.Millisecond)).Round(time.Millisecond).String()
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func runInit(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hosts := fs.String("hosts", "", "comma-separated claude-code,codex,opencode,kilo,cursor,pi; none for gates only (default: prompt)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return gate.ExitPass
		}
		return gate.ExitUsage
	}
	if fs.NArg() != 0 {
		return usageError(stderr, fs, "takes no positional arguments")
	}
	root, code := repoRoot(stderr)
	if code != 0 {
		return code
	}
	selected, err := initcmd.SelectHosts(root, *hosts, stdin, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ytif init: %v\n", err)
		return gate.ExitUsage
	}
	return initcmd.Run(root, version, selected, stdout, stderr)
}

func runGuard(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("guard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("host", "", "cursor for Cursor's beforeShellExecution format (default: PreToolUse)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return gate.ExitPass
		}
		return gate.ExitUsage
	}
	if fs.NArg() != 0 {
		return usageError(stderr, fs, "takes no positional arguments")
	}
	switch *host {
	case "", "claude-code", "codex":
		guard.Run(stdin, stdout, stderr)
	case "cursor":
		guard.RunCursor(stdin, stdout, stderr)
	default:
		return usageError(stderr, fs, "unknown host: "+*host)
	}
	return gate.ExitPass
}
