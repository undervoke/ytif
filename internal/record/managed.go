// Managed retention for local records: rotation plus whole-segment
// collection, reported through the shared receipt.
package record

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/storage"
)

// RecordsDir holds the active file and its closed segments.
func RecordsDir(common string) string {
	return filepath.Join(common, "ytif", "records")
}

// segTimeFmt renders segment-name timestamps; hints only, age and order
// always come from the records themselves.
const segTimeFmt = "20060102T150405Z"

// AppendManaged appends to the managed active file and triggers
// maintenance past the segment threshold. Append errors propagate;
// maintenance failures warn, a busy maintainer skips quietly.
func AppendManaged(common string, lines []Line, warn io.Writer) error {
	if len(lines) == 0 {
		return nil
	}
	if err := Append(DefaultPath(common), lines); err != nil {
		return err
	}
	fi, err := os.Stat(DefaultPath(common))
	if err != nil || fi.Size() <= storage.SegmentBytes {
		return nil
	}
	if _, merr := Maintain(common, time.Now().UTC()); merr != nil {
		if errors.Is(merr, storage.ErrBusy) {
			return nil
		}
		if warn == nil {
			warn = io.Discard
		}
		fmt.Fprintf(warn, "ytif: records maintenance: %v\n", merr)
	}
	return nil
}

// Maintain rotates an oversized active file, drops whole segments older
// than the age boundary, then oldest-by-content segments while over the
// byte budget. It takes the maintenance lock without waiting; a held lock
// yields storage.ErrBusy and changes nothing. An invalid policy defers
// every mutation and only reports sizes. Every pass persists a receipt,
// and the returned boundary is cumulative across passes.
func Maintain(common string, now time.Time) (storage.Pool, error) {
	now = now.UTC()
	dir := RecordsDir(common)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return storage.Pool{LastRun: now, Error: err.Error()}, err
	}
	lk, err := storage.MaintenanceLock(common, false)
	if err != nil {
		return storage.Pool{}, err
	}
	defer storage.Unlock(lk)

	prior, receiptErr := storage.ReadReceipt(common)
	pool := storage.Pool{LastRun: now, Boundary: prior.Local.Boundary}
	pool.LegacyBytes = fileSize(LegacyPath(common))
	policy, loadErr := storage.Load(common)

	segs, err := listSegments(dir)
	if err != nil {
		return failReceipt(common, policy, &pool, err)
	}
	pool.ManagedBytes = sizedTotal(dir, segs)
	if receiptErr != nil {
		return failReceipt(common, policy, &pool, receiptErr)
	}
	if loadErr != nil {
		return failReceipt(common, policy, &pool, loadErr)
	}

	if fileSize(DefaultPath(common)) > storage.SegmentBytes {
		if err := rotateActive(DefaultPath(common), dir, now); err != nil {
			return failReceipt(common, policy, &pool, err)
		}
		if segs, err = listSegments(dir); err != nil {
			return failReceipt(common, policy, &pool, err)
		}
		pool.ManagedBytes = sizedTotal(dir, segs)
	}

	cutoff := now.AddDate(0, 0, -policy.Days)
	beforeDelete := func(st segStats) error {
		pool.Boundary = maxKnown(pool.Boundary, st)
		return storage.UpdateReceipt(common, "local", "", policy, pool)
	}
	var kept []string
	for _, name := range segs {
		deleted, st, err := evictIfOld(filepath.Join(dir, name), cutoff, beforeDelete)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return failReceipt(common, policy, &pool, err)
		}
		if !deleted {
			kept = append(kept, name)
			continue
		}
		pool.ReclaimedBytes += st.size
		pool.ReclaimedUnits++
		pool.Boundary = maxKnown(pool.Boundary, st)
	}
	segs = kept
	pool.ManagedBytes = sizedTotal(dir, segs)

	for pool.ManagedBytes > policy.LocalMaxBytes {
		victim, gone, st, err := pickVictim(dir, segs, beforeDelete)
		segs = dropNames(segs, gone)
		if err != nil {
			return failReceipt(common, policy, &pool, err)
		}
		if victim == "" {
			break
		}
		segs = dropNames(segs, []string{victim})
		pool.ReclaimedBytes += st.size
		pool.ReclaimedUnits++
		pool.Boundary = maxKnown(pool.Boundary, st)
		pool.ManagedBytes = sizedTotal(dir, segs)
	}
	if pool.ManagedBytes > policy.LocalMaxBytes {
		pool.Overage = pool.ManagedBytes - policy.LocalMaxBytes
	}
	return finishReceipt(common, policy, &pool)
}

// ReadManaged reads the legacy file, every segment ordered by oldest
// content, and the active file last. It opens the set under the
// maintenance lock, releases it, then reads length-capped snapshots, so a
// concurrent rotation or unlink cannot drop a path. Unreadable files are
// skipped and reported; readable ones are kept.
func ReadManaged(common string) ([]Line, int, error) {
	lk, err := storage.MaintenanceLock(common, true)
	if err != nil {
		return nil, 0, err
	}
	dir := RecordsDir(common)
	names, rerr := listSegments(dir)
	type snapshot struct {
		f    *os.File
		n    int64
		role int // 0 legacy, 1 segment, 2 active
		name string
	}
	var snaps []snapshot
	var errs []error
	if rerr != nil {
		errs = append(errs, rerr)
	}
	open := func(path string, role int) {
		f, err := os.Open(path)
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			return
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			return
		}
		snaps = append(snaps, snapshot{f, fi.Size(), role, path})
	}
	open(LegacyPath(common), 0)
	for _, n := range names {
		open(filepath.Join(dir, n), 1)
	}
	open(DefaultPath(common), 2)
	storage.Unlock(lk)

	type chunk struct {
		lines []Line
		min   time.Time
		snap  snapshot
	}
	var chunks []chunk
	malformed := 0
	for _, s := range snaps {
		lines, bad, serr := scanInto(io.LimitReader(s.f, s.n), nil)
		s.f.Close()
		malformed += bad
		if serr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.name, serr))
		}
		var min time.Time
		for _, l := range lines {
			if l.Time.IsZero() {
				continue
			}
			if min.IsZero() || l.Time.Before(min) {
				min = l.Time
			}
		}
		chunks = append(chunks, chunk{lines, min, s})
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		if chunks[i].snap.role != chunks[j].snap.role {
			return chunks[i].snap.role < chunks[j].snap.role
		}
		if !chunks[i].min.Equal(chunks[j].min) {
			return chunks[i].min.Before(chunks[j].min)
		}
		return chunks[i].snap.name < chunks[j].snap.name
	})
	var lines []Line
	for _, c := range chunks {
		lines = append(lines, c.lines...)
	}
	return lines, malformed, errors.Join(errs...)
}

// segStats summarizes one segment's content.
type segStats struct {
	name     string
	min, max time.Time
	certain  bool
	size     int64
}

// summarize streams a segment: its oldest and newest valid times.
// Certainty needs a valid time on every line, since a malformed or
// timeless line could hide a newer observation.
func summarize(r io.Reader) (min, max time.Time, certain bool, err error) {
	certain, seen := true, false
	bad, serr := eachRecord(r, func(l Line) {
		if l.Time.IsZero() {
			certain = false
			return
		}
		if !seen || l.Time.Before(min) {
			min = l.Time
		}
		if !seen || l.Time.After(max) {
			max = l.Time
		}
		seen = true
	})
	if serr != nil {
		return min, max, false, serr
	}
	return min, max, certain && seen && bad == 0, nil
}

// listSegments returns the names maintenance owns; anything else in the
// directory is left alone.
func listSegments(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !matchSegment(e.Name()) {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// matchSegment accepts rotation outputs shaped
// <firstUTC>_<lastUTC>_<unique>.jsonl.
func matchSegment(name string) bool {
	base, ok := strings.CutSuffix(name, ".jsonl")
	if !ok {
		return false
	}
	first, rest, ok := strings.Cut(base, "_")
	if !ok {
		return false
	}
	last, uniq, ok := strings.Cut(rest, "_")
	if !ok || strings.Contains(uniq, "_") || uniq == "" {
		return false
	}
	if _, err := time.Parse(segTimeFmt, first); err != nil {
		return false
	}
	if _, err := time.Parse(segTimeFmt, last); err != nil {
		return false
	}
	_, err := hex.DecodeString(uniq)
	return err == nil
}

// sizedTotal sums segments plus active from stat calls.
func sizedTotal(dir string, segs []string) int64 {
	var total int64
	for _, n := range segs {
		total += fileSize(filepath.Join(dir, n))
	}
	return total + fileSize(filepath.Join(dir, "active.jsonl"))
}

// rotateActive renames an oversized active file to a segment named after
// its oldest and newest lines, falling back to now when unreadable.
func rotateActive(active, dir string, now time.Time) error {
	first, last := scanBounds(active, now)
	name := first.UTC().Format(segTimeFmt) + "_" + last.UTC().Format(segTimeFmt) + "_" + randSuffix() + ".jsonl"
	err := os.Rename(active, filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// scanBounds reports a file's oldest and newest valid times.
func scanBounds(path string, fallback time.Time) (time.Time, time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return fallback, fallback
	}
	defer f.Close()
	min, max, _, _ := summarize(f)
	if min.IsZero() {
		return fallback, fallback
	}
	return min, max
}

// evictIfOld unlinks a segment under its exclusive lock when its newest
// line is known and older than cutoff. Stats come from under the lock, so
// a batch racing the rotation is accounted.
func evictIfOld(path string, cutoff time.Time, beforeDelete func(segStats) error) (bool, segStats, error) {
	f, err := openExclusive(path)
	if err != nil {
		return false, segStats{}, err
	}
	defer storage.Unlock(f)
	st := inspectLocked(f)
	if !st.certain || !st.max.Before(cutoff) {
		return false, segStats{}, nil
	}
	if err := beforeDelete(st); err != nil {
		return false, segStats{}, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, segStats{}, err
	}
	return true, st, nil
}

// pickVictim chooses the oldest-by-content segment, rechecking the choice
// under lock since a late batch may have landed after the hint scan, then
// unlinks it. Gone files report as dropped names with no victim.
func pickVictim(dir string, segs []string, beforeDelete func(segStats) error) (string, []string, segStats, error) {
	infos, gone := hintScan(dir, segs)
	if len(infos) == 0 {
		return "", gone, segStats{}, nil
	}
	v := infos[0].name
	if len(infos) > 1 {
		auth, err := lockedInspect(filepath.Join(dir, v))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return "", append(gone, v), segStats{}, nil
			}
			return "", gone, segStats{}, err
		}
		if !auth.min.IsZero() && infos[1].min.Before(auth.min) {
			v = infos[1].name
		}
	}
	st, err := removeSegment(filepath.Join(dir, v), beforeDelete)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", append(gone, v), segStats{}, nil
		}
		return "", gone, segStats{}, err
	}
	return v, gone, st, nil
}

// hintScan summarizes every segment without inode locks for victim choice;
// content-unknown segments sort first so byte pressure sheds them before
// known-good history.
func hintScan(dir string, segs []string) ([]segStats, []string) {
	var infos []segStats
	var gone []string
	for _, n := range segs {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
			gone = append(gone, n)
			continue
		}
		st := scanFileStats(p)
		st.name = n
		infos = append(infos, st)
	}
	sort.Slice(infos, func(i, j int) bool {
		if !infos[i].min.Equal(infos[j].min) {
			return infos[i].min.Before(infos[j].min)
		}
		return infos[i].name < infos[j].name
	})
	return infos, gone
}

// scanFileStats summarizes one file without locking; a hint only.
func scanFileStats(path string) segStats {
	var st segStats
	f, err := os.Open(path)
	if err != nil {
		return st
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil {
		st.size = fi.Size()
	}
	st.min, st.max, st.certain, _ = summarize(f)
	return st
}

// lockedInspect summarizes a segment under its exclusive lock.
func lockedInspect(path string) (segStats, error) {
	f, err := openExclusive(path)
	if err != nil {
		return segStats{}, err
	}
	defer storage.Unlock(f)
	return inspectLocked(f), nil
}

// removeSegment unlinks a segment under its exclusive lock and reports
// the authoritative stats for the eviction boundary.
func removeSegment(path string, beforeDelete func(segStats) error) (segStats, error) {
	f, err := openExclusive(path)
	if err != nil {
		return segStats{}, err
	}
	defer storage.Unlock(f)
	st := inspectLocked(f)
	if err := beforeDelete(st); err != nil {
		return segStats{}, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return segStats{}, err
	}
	return st, nil
}

// openExclusive opens a segment and holds its exclusive lock.
func openExclusive(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := storage.Exclusive(f, true); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		storage.Unlock(f)
		return nil, err
	}
	return f, nil
}

// inspectLocked summarizes an exclusively held segment file.
func inspectLocked(f *os.File) segStats {
	var st segStats
	st.min, st.max, st.certain, _ = summarize(f)
	if fi, err := f.Stat(); err == nil {
		st.size = fi.Size()
	}
	return st
}

// maxKnown keeps the newest known eviction time.
func maxKnown(cur *storage.Boundary, st segStats) *storage.Boundary {
	if st.max.IsZero() {
		return cur
	}
	cand := &storage.Boundary{Time: st.max}
	if cur == nil || cur.Before(*cand) {
		return cand
	}
	return cur
}

// finishReceipt persists the pass and returns the cumulative boundary.
func finishReceipt(common string, policy storage.Policy, pool *storage.Pool) (storage.Pool, error) {
	if err := storage.UpdateReceipt(common, "local", "", policy, *pool); err != nil {
		pool.Error = joinErr(pool.Error, err.Error())
		return *pool, err
	}
	if rec, err := storage.ReadReceipt(common); err == nil && rec.Local.Boundary != nil {
		pool.Boundary = rec.Local.Boundary
	}
	return *pool, nil
}

// failReceipt persists a failed pass before reporting it.
func failReceipt(common string, policy storage.Policy, pool *storage.Pool, err error) (storage.Pool, error) {
	pool.Error = joinErr(pool.Error, err.Error())
	if rerr := storage.UpdateReceipt(common, "local", "", policy, *pool); rerr != nil {
		pool.Error = joinErr(pool.Error, "receipt: "+rerr.Error())
	} else if rec, rerr := storage.ReadReceipt(common); rerr == nil && rec.Local.Boundary != nil {
		pool.Boundary = rec.Local.Boundary
	}
	return *pool, err
}

func joinErr(prev, next string) string {
	if prev == "" {
		return next
	}
	return prev + "; " + next
}

func dropNames(segs, gone []string) []string {
	if len(gone) == 0 {
		return segs
	}
	drop := map[string]bool{}
	for _, g := range gone {
		drop[g] = true
	}
	kept := segs[:0]
	for _, n := range segs {
		if !drop[n] {
			kept = append(kept, n)
		}
	}
	return kept
}

// fileSize reports a path's size, zero when absent.
func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// randSuffix disambiguates segment names.
func randSuffix() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
