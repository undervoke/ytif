// Package proc runs the processes sources start so that cancelling a gate
// stops everything they started, including their own children.
package proc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

// grace is how long a cancelled process group has to exit after an
// interrupt before it is killed.
const grace = 10 * time.Second

// ErrInterrupted marks a process that the cancellation of its context
// stopped or kept from starting; what it reported may describe the
// interruption rather than its work.
var ErrInterrupted = errors.New("interrupted")

type limitKey struct{}
type environmentKey struct{}

// WithEnvironment carries a gate's execution context into all source processes.
func WithEnvironment(ctx context.Context, env []string) context.Context {
	return context.WithValue(ctx, environmentKey{}, append([]string(nil), env...))
}

// WithLimit returns a context under which at most n processes started by
// Run run at once.
func WithLimit(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, limitKey{}, make(chan struct{}, n))
}

// Run waits for a slot of ctx's limit, if it has one, then starts cmd in its
// own process group, interrupts the group when cmd's context is cancelled,
// and kills whatever remains of the group once cmd exits. The slot is held
// until then. Elapsed counts from the slot, so waiting is not attributed to
// cmd. cmd runs without the hook repository's git location.
func Run(ctx context.Context, cmd *exec.Cmd) (time.Duration, error) {
	if slots, ok := ctx.Value(limitKey{}).(chan struct{}); ok {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-ctx.Done():
			return 0, fmt.Errorf("%w: %v", ErrInterrupted, ctx.Err())
		}
	}
	start := time.Now()
	isolate(cmd)
	cmd.Env = withoutRepositoryEnv(cmd.Environ())
	if env, ok := ctx.Value(environmentKey{}).([]string); ok {
		cmd.Env = append(cmd.Env, env...)
	}
	var cancelled atomic.Bool
	if cancel := cmd.Cancel; cancel != nil {
		cmd.Cancel = func() error {
			cancelled.Store(true)
			return cancel()
		}
	}
	cmd.WaitDelay = grace
	if err := cmd.Start(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return time.Since(start), fmt.Errorf("%w: %v", ErrInterrupted, err)
		}
		return time.Since(start), err
	}
	err := cmd.Wait()
	reap(cmd)
	elapsed := time.Since(start)
	if cancelled.Load() {
		return elapsed, fmt.Errorf("%w: %v", ErrInterrupted, err)
	}
	return elapsed, err
}

// repositoryEnv lists git's local environment variables (git rev-parse
// --local-env-vars). Gates run inside git hooks, which set them to the
// hook's repository, and some to paths that only resolve there, such as a
// pathspec commit's temporary index. A check that runs git in a repository
// of its own would otherwise read or write the hook's. Git itself drops
// them when it enters another repository.
var repositoryEnv = map[string]bool{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_CONFIG":                       true,
	"GIT_CONFIG_PARAMETERS":            true,
	"GIT_CONFIG_COUNT":                 true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_DIR":                          true,
	"GIT_WORK_TREE":                    true,
	"GIT_IMPLICIT_WORK_TREE":           true,
	"GIT_GRAFT_FILE":                   true,
	"GIT_INDEX_FILE":                   true,
	"GIT_NO_REPLACE_OBJECTS":           true,
	"GIT_REPLACE_REF_BASE":             true,
	"GIT_PREFIX":                       true,
	"GIT_SHALLOW_FILE":                 true,
	"GIT_COMMON_DIR":                   true,
}

func withoutRepositoryEnv(env []string) []string {
	kept := env[:0:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !repositoryEnv[name] {
			kept = append(kept, kv)
		}
	}
	return kept
}
