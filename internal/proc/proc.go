// Package proc runs the processes sources start so that cancelling a gate
// stops everything they started, including their own children.
package proc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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
// exits.
func Run(cmd *exec.Cmd) error {
	isolate(cmd)
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
