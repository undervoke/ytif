// Package checkrun runs the project's registered direct commands as the
// command runner.
//
// A command entry carries its own argv, so the source is self-registering:
// discovery returns the registered keys without starting any process.
package checkrun

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/proc"
)

// Runner is the runner name this source owns: the inventory's direct
// command runner.
const Runner = inventory.CommandRunner

// Source runs the registered direct commands a gate selects. Commands run
// with the gate's worker count and atomic exclusive tokens, and their
// outcomes aggregate the way the old whole-list lane reported them: one
// result and one run invocation per command, keyed command:unit:name.
type Source struct {
	entries []command
	jobs    int
}

// command is one registered direct command.
type command struct {
	key  check.Key
	name string
	run  []string
	// cwd is the registered directory; workingDir resolves it against the
	// repository root at run time.
	cwd       string
	exclusive []string
}

// NewSource keeps the command entries in registration order and ignores
// every other runner. jobs caps how many commands run at once; below 1 it
// means 1. The gate passes its process limit.
func NewSource(entries []inventory.Entry, jobs int) *Source {
	if jobs < 1 {
		jobs = 1
	}
	s := &Source{jobs: jobs}
	for _, e := range entries {
		if e.Runner != Runner {
			continue
		}
		s.entries = append(s.entries, command{
			key: e.Key(), name: e.Name, run: e.Run, cwd: e.Cwd, exclusive: e.Exclusive,
		})
	}
	return s
}

// Runners lists the runner names this source owns.
func (*Source) Runners() []string { return []string{Runner} }

// Discover returns the self-registered command keys. It starts no process.
func (s *Source) Discover(context.Context, check.Repo) ([]check.Key, []check.Invocation, error) {
	keys := make([]check.Key, 0, len(s.entries))
	for _, e := range s.entries {
		keys = append(keys, e.key)
	}
	return keys, nil, nil
}

// outcome is what one selected command did.
type outcome struct {
	elapsed time.Duration
	ended   time.Time // when the command finished, if it ran
	err     error
	// interrupted means cancellation stopped the command or kept it from
	// starting; it leaves an interrupted invocation and no result, so the
	// gate records nothing for it.
	interrupted bool
}

// picked is one selected key and the command registered for it, if any.
type picked struct {
	key check.Key
	cmd *command // nil when this source registers no such key
}

// Run executes the selected command keys in the requested order against in.
// Every selected key gets either a result or an invocation explaining why
// it has none. It writes no records itself: the gate owns the run's
// records.
func (s *Source) Run(ctx context.Context, repo check.Repo, keys []check.Key, in check.Input) (check.Report, error) {
	byKey := make(map[check.Key]*command, len(s.entries))
	for i := range s.entries {
		byKey[s.entries[i].key] = &s.entries[i]
	}
	sel := make([]picked, 0, len(keys))
	for _, k := range keys {
		sel = append(sel, picked{key: k, cmd: byKey[k]})
	}
	outs := s.execute(ctx, repo, sel, in)
	var rep check.Report
	for i, o := range outs {
		name := sel[i].key.Name
		if sel[i].cmd != nil {
			name = sel[i].cmd.name
		}
		rep.Invocations = append(rep.Invocations, check.Invocation{
			Runner: Runner, Unit: name, What: "run",
			Elapsed: o.elapsed, Err: o.err, Keys: []check.Key{sel[i].key}, Interrupted: o.interrupted,
		})
		if o.interrupted || sel[i].cmd == nil {
			// The invocation error above explains the missing outcome.
			continue
		}
		result := check.Pass
		if o.err != nil {
			result = check.Fail
		}
		rep.Results = append(rep.Results, check.Result{
			Key: sel[i].key, Outcome: result, Elapsed: o.elapsed, Timed: true, Ended: o.ended,
		})
	}
	return rep, nil
}

// execute runs the registered selections with the source's worker count.
// Exclusive tokens are owner-declared mutexes, not dependency edges:
// workers never hold a partial token set while waiting for another token.
// Selections the source does not register, or that carry no runnable argv,
// get an explanatory error without starting anything.
func (s *Source) execute(ctx context.Context, repo check.Repo, sel []picked, in check.Input) []outcome {
	outs := make([]outcome, len(sel))
	pending := make([]int, 0, len(sel))
	for i, q := range sel {
		switch {
		case q.cmd == nil:
			outs[i] = outcome{err: fmt.Errorf("command %s is not registered", q.key)}
		case len(q.cmd.run) == 0 || q.cmd.run[0] == "":
			outs[i] = outcome{err: fmt.Errorf("command %s needs run argv", q.cmd.name)}
		default:
			pending = append(pending, i)
		}
	}
	type done struct {
		index int
		out   outcome
	}
	finished := make(chan done, len(pending))
	busy := map[string]bool{}
	running := 0
	var logMu sync.Mutex
	for len(pending) > 0 || running > 0 {
		if ctx.Err() != nil {
			for _, i := range pending {
				outs[i] = outcome{interrupted: true}
			}
			pending = nil
		}
		for p := 0; p < len(pending) && running < s.jobs && ctx.Err() == nil; {
			i := pending[p]
			c := sel[i].cmd
			locked := false
			for _, token := range c.exclusive {
				locked = locked || busy[token]
			}
			if locked {
				p++
				continue
			}
			for _, token := range c.exclusive {
				busy[token] = true
			}
			pending = append(pending[:p], pending[p+1:]...)
			running++
			go func() {
				log := &lineWriter{mu: &logMu, dst: repo.Log, prefix: "[" + c.name + "] "}
				argv := c.run
				plan := in.Plans[c.key]
				if plan != nil && len(plan.Argv) > 0 {
					argv = plan.Argv
				}
				run := func(args []string) (time.Duration, error) {
					cmd := exec.CommandContext(ctx, args[0], args[1:]...)
					cmd.Dir = workingDir(repo.Root, c.cwd)
					cmd.Stdout, cmd.Stderr = log, log
					cmd.Env = in.ProcessEnvironment(cmd.Environ(), []check.Key{c.key})
					return proc.Run(ctx, cmd)
				}
				elapsed, err := run(argv)
				if errors.Is(err, syscall.E2BIG) && plan != nil && len(plan.Argv) > 0 {
					plan.Resolved, plan.Expansion, plan.Reason = check.ScopeFull, "argument-limit", "argument-limit"
					plan.Argv, plan.Changed, plan.Offered, plan.Targets = nil, nil, "full-domain", 0
					fmt.Fprintln(log, "ytif: argument limit; running the original full invocation")
					d, fallbackErr := run(c.run)
					elapsed, err = elapsed+d, fallbackErr
				}
				log.flush()
				finished <- done{index: i, out: outcome{
					elapsed: elapsed, ended: time.Now().UTC(),
					err:         err,
					interrupted: errors.Is(err, proc.ErrInterrupted),
				}}
			}()
		}
		if running == 0 {
			break
		}
		r := <-finished
		outs[r.index] = r.out
		running--
		for _, token := range sel[r.index].cmd.exclusive {
			delete(busy, token)
		}
	}
	return outs
}

// workingDir resolves a registered command directory against the
// repository root. An absolute directory is used as is.
func workingDir(root, dir string) string {
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Join(root, dir)
}

// AdmissionIssue is one malformed command entry whatever gate might run it.
type AdmissionIssue struct {
	Key check.Key // entry key
	// Location names the registration: verifications[i].
	Location string
	Detail   string
}

// ValidateCommands validates the shape of every command entry whatever gate
// might run it. Duplicate command keys are the inventory loader's finding,
// not this one's.
func ValidateCommands(entries []inventory.Entry) []AdmissionIssue {
	var out []AdmissionIssue
	for i, e := range entries {
		loc := e.Origin
		if loc == "" {
			loc = fmt.Sprintf("verifications[%d]", i)
		}
		if e.Runner == Runner {
			if strings.TrimSpace(e.Name) == "" || len(e.Run) == 0 || e.Run[0] == "" {
				out = append(out, AdmissionIssue{Key: e.Key(), Location: loc, Detail: "command entry needs a name and run argv"})
			}
		}
	}
	return out
}
