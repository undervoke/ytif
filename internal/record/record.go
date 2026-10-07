// Package record appends execution records and computes statistics from them
// on demand. Records are append-only JSON lines; nothing is aggregated or
// rewritten, so a query always reflects the raw observations.
package record

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Line kinds.
const (
	KindResult     = "result"     // one check outcome
	KindInvocation = "invocation" // one build or process with its wall time
	KindReconcile  = "reconcile"  // one reconciliation finding
	KindDispatch   = "dispatch"   // a selected check that produced no outcome
	KindGuard      = "guard"      // one refused agent command
)

// Result outcomes for KindResult lines. A skip is a check that ran and
// reported a skip, so it carries no fresh verdict and no elapsed time. A
// cached pass replays an earlier pass: it ends a fail run like a pass but
// carries no fresh timing, so the gate must omit elapsed_ms on it.
const (
	OutcomePass    = "pass"
	OutcomeFail    = "fail"
	OutcomeBlocked = "blocked"
	OutcomeSkip    = "skip"
	OutcomeCached  = "cached"
)

// Line is one record.
type Line struct {
	Time      time.Time `json:"time"`
	Attempt   string    `json:"attempt"`
	Kind      string    `json:"kind"`
	Gate      string    `json:"gate,omitempty"`
	Context   string    `json:"context,omitempty"`
	Scope     string    `json:"scope,omitempty"`
	Key       string    `json:"key,omitempty"`
	Runner    string    `json:"runner,omitempty"`
	Unit      string    `json:"unit,omitempty"`
	What      string    `json:"what,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
	ElapsedMS *int64    `json:"elapsed_ms,omitempty"` // nil when unknown
	Detail    string    `json:"detail,omitempty"`
	// Provenance of the run. Empty means unknown: a record written before
	// provenance was collected. The board shows such records as historical.
	Commit   string `json:"commit,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	Profile  string `json:"profile,omitempty"`
}

// Millis converts d for ElapsedMS.
func Millis(d time.Duration) *int64 {
	ms := d.Milliseconds()
	return &ms
}

// DefaultPath is the local record file inside the git common directory.
func DefaultPath(commonDir string) string {
	return filepath.Join(commonDir, "ytif", "records.jsonl")
}

// NewAttempt returns an identifier shared by every line of one gate run.
func NewAttempt() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Append writes lines to path in one write, creating the file if needed.
func Append(path string, lines []Line) error {
	if len(lines) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Read loads every line from paths in order. Missing files are skipped;
// malformed lines are counted, not fatal, because one torn write must not hide
// the rest of the history.
func Read(paths []string) (lines []Line, malformed int, err error) {
	for _, p := range paths {
		f, err := os.Open(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for sc.Scan() {
			if len(bytes.TrimSpace(sc.Bytes())) == 0 {
				continue
			}
			var l Line
			if json.Unmarshal(sc.Bytes(), &l) != nil {
				malformed++
				continue
			}
			lines = append(lines, l)
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, 0, fmt.Errorf("%s: %w", p, err)
		}
	}
	return lines, malformed, nil
}
