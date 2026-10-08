// Package dispatch reads the results file of a check dispatcher: a
// generated program that runs selected checks in one process and appends one
// JSON line as each check starts and as it ends.
package dispatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/proc"
)

// Event is one line of a results file. Fields are untagged; JSON decoding
// matches them case-insensitively.
type Event struct {
	I         int
	Start     bool
	Outcome   string
	ElapsedNS int64
	EndNS     int64 // Unix time of the outcome
	Output    string
}

// Results reads the results file of a dispatcher that ran keys, named
// names, in order, and ended with runErr. It returns their outcomes and the
// error the outcomes do not explain.
func Results(path string, keys []check.Key, names []string, runErr error) ([]check.Result, error) {
	events, running, readErr := read(path, len(keys))
	var results []check.Result
	for _, e := range events {
		r := check.Result{Key: keys[e.I], Outcome: check.Outcome(e.Outcome), Output: e.Output}
		if e.EndNS != 0 {
			r.Ended = time.Unix(0, e.EndNS)
		}
		if r.Outcome != check.Blocked {
			r.Elapsed, r.Timed = time.Duration(e.ElapsedNS), true
		}
		results = append(results, r)
	}
	// A check may end the process itself, even with status 0; the start
	// event without a result names it.
	exit := "exited"
	if runErr != nil {
		exit = runErr.Error()
	}
	var err error
	switch {
	case errors.Is(runErr, proc.ErrInterrupted):
		err = runErr
	case readErr != nil:
		err = readErr
	case running >= 0:
		err = fmt.Errorf("dispatcher %s while running %s", exit, names[running])
	case len(events) < len(keys):
		err = fmt.Errorf("dispatcher %s after %d of %d checks", exit, len(events), len(keys))
	case runErr != nil:
		err = fmt.Errorf("dispatcher %s", exit)
	}
	return results, err
}

// read returns the finished events and the index of a check that started
// without finishing, or -1.
func read(path string, n int) ([]Event, int, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, -1, nil
	}
	if err != nil {
		return nil, -1, err
	}
	var done []Event
	running := -1
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil || e.I < 0 || e.I >= n {
			return done, running, fmt.Errorf("dispatcher wrote an unreadable result line: %.200q", line)
		}
		if e.Start {
			running = e.I
			continue
		}
		switch check.Outcome(e.Outcome) {
		case check.Pass, check.Fail, check.Blocked:
		default:
			return done, running, fmt.Errorf("dispatcher reported outcome %q", e.Outcome)
		}
		if e.I == running {
			running = -1
		}
		done = append(done, e)
	}
	return done, running, nil
}
