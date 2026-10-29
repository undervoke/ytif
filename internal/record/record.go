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
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/storage"
)

// Line kinds.
const (
	KindSelection  = "selection"  // gate membership and execution selection
	KindResult     = "result"     // one check outcome
	KindInvocation = "invocation" // one build or process with its wall time
	KindReconcile  = "reconcile"  // one reconciliation finding
	KindDispatch   = "dispatch"   // a selected check that produced no outcome
	KindGuard      = "guard"      // one refused agent command
)

// Line is one record.
type Line struct {
	Time      time.Time  `json:"time"`
	Attempt   string     `json:"attempt"`
	Kind      string     `json:"kind"`
	Gate      string     `json:"gate,omitempty"`
	Scope     string     `json:"scope,omitempty"`
	Key       string     `json:"key,omitempty"`
	Runner    string     `json:"runner,omitempty"`
	Unit      string     `json:"unit,omitempty"`
	What      string     `json:"what,omitempty"`
	Outcome   string     `json:"outcome,omitempty"`
	ElapsedMS *int64     `json:"elapsed_ms,omitempty"` // nil when unknown
	Detail    string     `json:"detail,omitempty"`
	Selection *Selection `json:"selection,omitempty"`
}

// CI artifact names. A CI workflow uploads the file under the artifact name,
// and the board reads that artifact from the repository it names.
const (
	ArtifactName = "ytif-records"
	ArtifactFile = "ytif-records.jsonl"
)

// Selection carries final plans once per attempt, after execution-time widening
// and verifier reports. It describes scheduling, not unreported verifier work.
type Selection struct {
	Profile          string              `json:"profile,omitempty"`
	Comparison       *check.Comparison   `json:"comparison,omitempty"`
	Target           *gitx.TargetContext `json:"target,omitempty"`
	ContextSource    string              `json:"context_source,omitempty"`
	CompareHead      string              `json:"compare_head,omitempty"`
	Plans            []check.Plan        `json:"plans,omitempty"`
	Branch           string              `json:"branch,omitempty"` // empty when HEAD was detached
	Base             string              `json:"base,omitempty"`
	Head             string              `json:"head,omitempty"`
	Fallback         string              `json:"fallback,omitempty"`
	DirtyDigest      string              `json:"dirty_digest,omitempty"`
	Dirty            bool                `json:"dirty,omitempty"`
	Bases            []string            `json:"bases,omitempty"`
	Tips             []string            `json:"tips,omitempty"`
	MergeParents     []string            `json:"merge_parents,omitempty"`
	RevisionMismatch bool                `json:"revision_mismatch,omitempty"`
	Registered       int                 `json:"registered"`
	Eligible         []string            `json:"eligible"`
	Selected         []string            `json:"selected"`
	Omissions        []Omission          `json:"omissions,omitempty"`
	Completed        bool                `json:"completed"`
}

type Omission struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// Millis converts d for ElapsedMS.
func Millis(d time.Duration) *int64 {
	ms := d.Milliseconds()
	return &ms
}

// DefaultPath is the active managed record file inside the git common
// directory. Closed segments live beside it in the same directory.
func DefaultPath(commonDir string) string {
	return filepath.Join(commonDir, "ytif", "records", "active.jsonl")
}

// LegacyPath is the pre-rotation record file. New writers never append to
// it; it stays readable until explicitly handled and is never pruned by
// maintenance.
func LegacyPath(commonDir string) string {
	return filepath.Join(commonDir, "ytif", "records.jsonl")
}

// NewAttempt returns an identifier shared by every line of one gate run.
func NewAttempt() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Append writes lines to path in one write, creating the file if needed.
// It holds a shared lock while writing and reopens when the inode was
// unlinked between open and lock, so an unlink under an exclusive lock
// never absorbs a concurrent batch.
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
	for {
		f, err := storage.Open(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if err := storage.Shared(f); err != nil {
			f.Close()
			return err
		}
		ok, err := storage.Linked(f)
		if err != nil {
			storage.Unlock(f)
			return err
		}
		if !ok {
			storage.Unlock(f)
			continue
		}
		_, werr := f.Write(buf.Bytes())
		return errors.Join(werr, storage.Unlock(f))
	}
}

// Read loads every line from paths in order. Missing files are skipped;
// malformed lines are counted, not fatal, because one torn write must not hide
// the rest of the history.
func Read(paths []string) (lines []Line, malformed int, err error) {
	for _, p := range paths {
		f, err := storage.Open(p, os.O_RDONLY, 0)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		var bad int
		lines, bad, err = scanInto(f, lines)
		f.Close()
		if err != nil {
			return nil, 0, fmt.Errorf("%s: %w", p, err)
		}
		malformed += bad
	}
	return lines, malformed, nil
}

// scanInto appends the valid lines r holds. Malformed lines count; a read
// failure reports alongside whatever was already scanned.
func scanInto(r io.Reader, lines []Line) ([]Line, int, error) {
	bad, err := eachRecord(r, func(l Line) { lines = append(lines, l) })
	return lines, bad, err
}

// eachRecord runs fn on every decodable line. Blank lines skip; malformed
// ones count. Maintenance summaries share this loop so scanner behavior
// stays identical everywhere.
func eachRecord(r io.Reader, fn func(Line)) (int, error) {
	var malformed int
	sc := bufio.NewScanner(r)
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
		fn(l)
	}
	return malformed, sc.Err()
}
