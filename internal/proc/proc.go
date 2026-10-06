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

// Run starts cmd in its own process group, interrupts the group when cmd's
// context is cancelled, and kills whatever remains of the group once cmd
// exits. cmd runs without the hook repository's git location.
func Run(cmd *exec.Cmd) error {
	isolate(cmd)
	cmd.Env = withoutRepositoryEnv(cmd.Environ())
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
			return fmt.Errorf("%w: %v", ErrInterrupted, err)
		}
		return err
	}
	err := cmd.Wait()
	reap(cmd)
	if cancelled.Load() {
		return fmt.Errorf("%w: %v", ErrInterrupted, err)
	}
	return err
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
