// Package checkrun executes the project's entire pre-gate command list, without
// test discovery or preparation. Exclusive tokens are owner-declared mutexes,
// not dependency edges or a promise about undeclared shared resources.
package checkrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/proc"
	"github.com/undervoke/ytif/internal/record"
)

type Options struct {
	Jobs    int
	Records string
	Stdout  io.Writer
	Stderr  io.Writer
}

type outcome struct {
	index       int
	elapsed     time.Duration
	err         error
	started     bool
	interrupted bool
}

// Run validates the whole list before launching any command. It aggregates
// ordinary failures; interruption stops scheduling and cancels running groups.
func Run(ctx context.Context, root string, o Options) int {
	if o.Jobs < 1 {
		o.Jobs = 1
	}
	inv, err := inventory.LoadInventory(root)
	if err != nil {
		fmt.Fprintf(o.Stderr, "ytif check: %v\n", err)
		return 2
	}
	entries := inv.Commands
	names := map[string]bool{}
	inspector := newInspector(root)
	for n, e := range entries {
		if strings.TrimSpace(e.Name) == "" || len(e.Run) == 0 || e.Run[0] == "" {
			fmt.Fprintf(o.Stderr, "ytif check: commands[%d] needs name and run argv\n", n)
			return 2
		}
		if names[e.Name] {
			fmt.Fprintf(o.Stderr, "ytif check: duplicate name %q would combine separate results\n", e.Name)
			return 2
		}
		names[e.Name] = true
		inspector.visits = 0
		if why := inspector.script(commandText(e.Run), workingDir(root, e.Cwd)); why != "" {
			fmt.Fprintf(o.Stderr, "ytif check: %s runs %s; builds and tests belong at the gates, and check cannot invoke ytif gates, itself, or the user allowance\n", e.Name, why)
			return 2
		}
	}
	if o.Records == "" {
		common, err := gitx.CommonDir(root)
		if err != nil {
			fmt.Fprintf(o.Stderr, "ytif check: %v\n", err)
			return 2
		}
		o.Records = record.DefaultPath(common)
	}
	fmt.Fprintf(o.Stdout, "ytif check · %d commands · jobs %d\n", len(entries), o.Jobs)
	results := execute(ctx, root, entries, o)
	lines := make([]record.Line, 0, len(entries)*2)
	attempt, now := record.NewAttempt(), time.Now().UTC()
	contextName := "local"
	if ci := strings.ToLower(os.Getenv("CI")); ci != "" && ci != "0" && ci != "false" {
		contextName = "ci"
	}
	exit := 0
	for n, r := range results {
		e := entries[n]
		if !r.started || r.interrupted {
			fmt.Fprintf(o.Stdout, "interrupted %s\n", e.Name)
			exit = 1
			continue
		}
		status := "pass"
		detail := ""
		if r.err != nil {
			status, detail, exit = "fail", r.err.Error(), 1
		}
		fmt.Fprintf(o.Stdout, "%s %s · %s", status, e.Name, r.elapsed.Round(time.Millisecond))
		if detail != "" {
			fmt.Fprintf(o.Stdout, " · %s", detail)
		}
		fmt.Fprintln(o.Stdout)
		unit := e.Cwd
		if unit == "" {
			unit = "."
		}
		line := record.Line{Time: now, Attempt: attempt, Gate: "check", Context: contextName,
			Scope: "command", Runner: "command", Unit: unit, Kind: record.KindResult,
			Key:     (check.Key{Runner: "command", Unit: unit, Name: e.Name}).String(),
			Outcome: status, ElapsedMS: record.Millis(r.elapsed), Detail: detail}
		lines = append(lines, line)
		line.Kind, line.Key, line.Unit, line.What = record.KindInvocation, "", e.Name, "run"
		lines = append(lines, line)
	}
	if ctx.Err() != nil {
		exit = 1
	}
	if err := record.Append(o.Records, lines); err != nil {
		fmt.Fprintf(o.Stderr, "ytif check: records not written: %v\n", err)
		return 2
	}
	return exit
}

func workingDir(root, dir string) string {
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Join(root, dir)
}

// execute acquires every token in the scheduler before starting a command.
// Workers never hold a partial token set while waiting for another token.
func execute(ctx context.Context, root string, entries []inventory.Command, o Options) []outcome {
	results := make([]outcome, len(entries))
	pending := make([]int, len(entries))
	for i := range pending {
		pending[i] = i
	}
	done := make(chan outcome, len(entries))
	busy := map[string]bool{}
	running := 0
	var logMu sync.Mutex
	for len(pending) > 0 || running > 0 {
		if ctx.Err() != nil {
			pending = nil
		}
		for p := 0; p < len(pending) && running < o.Jobs && ctx.Err() == nil; {
			i := pending[p]
			e := entries[i]
			locked := false
			for _, token := range e.Exclusive {
				locked = locked || busy[token]
			}
			if locked {
				p++
				continue
			}
			for _, token := range e.Exclusive {
				busy[token] = true
			}
			pending = append(pending[:p], pending[p+1:]...)
			running++
			go func() {
				log := &lineWriter{mu: &logMu, dst: o.Stdout, prefix: "[" + e.Name + "] "}
				cmd := exec.CommandContext(ctx, e.Run[0], e.Run[1:]...)
				cmd.Dir = workingDir(root, e.Cwd)
				cmd.Stdout, cmd.Stderr = log, log
				elapsed, err := proc.Run(ctx, cmd)
				log.flush()
				done <- outcome{index: i, started: true, elapsed: elapsed, err: err, interrupted: errors.Is(err, proc.ErrInterrupted)}
			}()
		}
		if running == 0 {
			break
		}
		r := <-done
		results[r.index] = r
		running--
		for _, token := range entries[r.index].Exclusive {
			delete(busy, token)
		}
	}
	return results
}
