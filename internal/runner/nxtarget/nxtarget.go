// Package nxtarget runs the Nx targets a repository marks as verifications.
//
// Marking a target with metadata.ytif declares a verification, the way an
// exported verifyXxx does; gate membership and the contract stay in the
// ledger. Discovery reads the graph Nx resolves, so a target a plugin infers
// counts like a declared one. The key's unit is the project root, which gate
// scope places through the graph like any other unit.
package nxtarget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/undervoke/ytif/internal/affected"
	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/proc"
	"github.com/undervoke/ytif/internal/runner/nodetool"
)

// Runner is the runner name this source owns.
const Runner = inventory.NxTargetRunner

// Source discovers marked targets and runs the selected ones. One target
// invocation is one verdict: Nx owns what the target runs, its dependencies
// and cache included. Keys run one at a time within this source; other
// sources run alongside it.
type Source struct {
	tasks map[check.Key]task
}

// task is the exact graph project and target a discovered key runs.
type task struct{ project, target string }

func (t task) String() string { return t.project + ":" + t.target }

// Runners lists the runner names this source owns.
func (*Source) Runners() []string { return []string{Runner} }

// Discover returns every marked target in the resolved project graph.
func (s *Source) Discover(ctx context.Context, repo check.Repo) ([]check.Key, []check.Invocation, error) {
	projects, invs, err := affected.Projects(ctx, repo)
	if err != nil {
		return nil, invs, err
	}
	s.tasks = map[check.Key]task{}
	var keys []check.Key
	for _, p := range projects {
		unit := p.Root
		if unit == "" {
			unit = "."
		}
		for name, target := range p.Targets {
			if !marked(target) {
				continue
			}
			k := check.Key{Runner: Runner, Unit: unit, Name: name}
			s.tasks[k] = task{project: p.Name, target: name}
			keys = append(keys, k)
		}
	}
	return keys, invs, nil
}

func marked(target json.RawMessage) bool {
	var t struct {
		Metadata struct {
			Ytif bool `json:"ytif"`
		} `json:"metadata"`
	}
	return json.Unmarshal(target, &t) == nil && t.Metadata.Ytif
}

// Run executes keys in order. Every key gets a result or an invocation
// explaining why it has none. Project and target go as separate options:
// Nx resolves a joined project:target against the default project first, so
// a same-named target there would run and its verdict land on this key.
func (s *Source) Run(ctx context.Context, repo check.Repo, keys []check.Key, in check.Input) (check.Report, error) {
	bin, err := nodetool.Bin(repo.Root, ".", "nx")
	if err != nil {
		return check.Report{}, err
	}
	var rep check.Report
	for _, k := range keys {
		t, ok := s.tasks[k]
		if !ok {
			rep.Invocations = append(rep.Invocations, check.Invocation{
				Runner: Runner, Unit: k.Unit, What: "run", Keys: []check.Key{k},
				Err: fmt.Errorf("nx target %s is not marked in the project graph", k),
			})
			continue
		}
		cmd := exec.CommandContext(ctx, bin, "run", "--project="+t.project, "--target="+t.target)
		cmd.Dir = repo.Root
		cmd.Stdout, cmd.Stderr = repo.Log, repo.Log
		cmd.Env = in.ProcessEnvironment(cmd.Environ(), []check.Key{k})
		elapsed, err := proc.Run(ctx, cmd)
		inv := check.Invocation{Runner: Runner, Unit: t.String(), What: "run", Elapsed: elapsed, Keys: []check.Key{k}}
		var exit *exec.ExitError
		failed := errors.As(err, &exit)
		switch {
		case errors.Is(err, proc.ErrInterrupted):
			inv.Err, inv.Interrupted = err, true
		case err != nil && !failed:
			inv.Err = err
		default:
			outcome := check.Pass
			if failed {
				outcome = check.Fail
			}
			rep.Results = append(rep.Results, check.Result{Key: k, Outcome: outcome, Elapsed: elapsed, Timed: true, Ended: time.Now().UTC()})
		}
		rep.Invocations = append(rep.Invocations, inv)
	}
	return rep, nil
}
