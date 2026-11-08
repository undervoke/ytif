// Package record reads the bounded tail of local records for the agent guard.
//
// Recent returns result and invocation lines newest first without taking the
// maintenance lock, so a hook never blocks on retention. It reads the active
// file, then segments newest first, then the legacy file, stopping once the
// byte budget is spent. Files that vanish mid-read are skipped. Lines whose
// kind is not result or invocation are filtered before decoding, since the
// guard never prices selection, dispatch, reconcile, or prior guard lines.
package record

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/undervoke/ytif/internal/storage"
)

// RecentMaxBytes bounds one guard read of local records.
const RecentMaxBytes = 32 << 20

// Recent reads result and invocation lines newest first within maxBytes. A
// non-positive budget uses RecentMaxBytes. It never takes the maintenance
// lock; a concurrent rotation may duplicate or drop a boundary batch, which
// the guard treats as estimation error. Malformed lines count; selection and
// other kinds are filtered before decoding. A file longer than its share
// contributes only its newest bytes, and an attempt split by that cut is
// dropped so a partial success is never priced as complete.
func Recent(common string, maxBytes int64) ([]Line, int, error) {
	if maxBytes <= 0 {
		maxBytes = RecentMaxBytes
	}
	dir := RecordsDir(common)
	names, listErr := listSegments(dir)
	if listErr != nil {
		names = nil
	}
	sort.Strings(names)
	var paths []string
	paths = append(paths, DefaultPath(common))
	for i := len(names) - 1; i >= 0; i-- {
		paths = append(paths, filepath.Join(dir, names[i]))
	}
	paths = append(paths, LegacyPath(common))
	var out []Line
	malformed := 0
	var errs []error
	var spent int64
	visited := 0
	type tail struct {
		lines     []Line
		truncated bool
	}
	var tails []tail
	for _, p := range paths {
		if spent >= maxBytes {
			break
		}
		allow := maxBytes - spent
		lines, bad, truncated, window, rerr := scanRecent(p, allow)
		visited++
		malformed += bad
		if rerr != nil {
			errs = append(errs, rerr)
		}
		// A truncated first line without an attempt identifies nothing to
		// drop by group, so drop just that line.
		if truncated && len(lines) > 0 && lines[0].Attempt == "" {
			lines = lines[1:]
		}
		tails = append(tails, tail{lines, truncated})
		out = append(out, lines...)
		spent += window
	}
	incomplete := listErr != nil || visited < len(paths)
	drop := map[string]bool{}
	cutBoundary := false
	for _, t := range tails {
		cutBoundary = cutBoundary || t.truncated
		if t.truncated && len(t.lines) > 0 && t.lines[0].Attempt != "" {
			drop[t.lines[0].Attempt] = true
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if incomplete && !cutBoundary && len(out) > 0 {
		if last := out[len(out)-1].Attempt; last != "" {
			drop[last] = true
		} else {
			out = out[:len(out)-1]
		}
	}
	if len(drop) > 0 {
		kept := out[:0]
		for _, l := range out {
			if l.Attempt != "" && drop[l.Attempt] {
				continue
			}
			kept = append(kept, l)
		}
		out = kept
	}
	return out, malformed, errors.Join(errs...)
}

// scanRecent returns the result and invocation lines in the newest maxBytes
// of path, in file order. When the file is longer, the window starts at its
// newest bytes and the first line is skipped unless the cut lands exactly on
// a line boundary. A torn final line without a trailing newline is skipped
// rather than counted. The reported window bounds the bytes read.
func scanRecent(path string, maxBytes int64) (out []Line, malformed int, truncated bool, window int64, err error) {
	if maxBytes <= 0 {
		return nil, 0, false, 0, nil
	}
	f, err := storage.Open(path, os.O_RDONLY, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, false, 0, nil
	}
	if err != nil {
		return nil, 0, false, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, false, 0, err
	}
	size := fi.Size()
	if size <= 0 {
		return nil, 0, false, 0, nil
	}
	var offset int64
	if size > maxBytes {
		offset = size - maxBytes
		truncated = true
	}
	window = size - offset
	endsNewline := true
	var last [1]byte
	if _, rerr := f.ReadAt(last[:], size-1); rerr == nil {
		endsNewline = last[0] == '\n'
	}
	var reader io.Reader
	if offset == 0 {
		if _, serr := f.Seek(0, io.SeekStart); serr != nil {
			return nil, 0, false, window, serr
		}
		reader = io.LimitReader(f, window)
	} else {
		aligned := false
		var prev [1]byte
		if _, rerr := f.ReadAt(prev[:], offset-1); rerr == nil {
			aligned = prev[0] == '\n'
		}
		if _, serr := f.Seek(offset, io.SeekStart); serr != nil {
			return nil, 0, truncated, window, serr
		}
		lr := io.LimitReader(f, window)
		if aligned {
			reader = lr
		} else {
			br := bufio.NewReader(lr)
			_, rerr := br.ReadBytes('\n')
			if rerr != nil {
				if errors.Is(rerr, io.EOF) {
					return nil, 0, truncated, window, nil
				}
				return nil, 0, truncated, window, rerr
			}
			reader = br
		}
	}
	sc := bufio.NewScanner(reader)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lastAction := 0 // 1 appended, 2 malformed; filtered and blank lines leave no trace
	for sc.Scan() {
		lastAction = 0
		raw := sc.Bytes()
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		if !bytes.Contains(raw, []byte(`"result"`)) && !bytes.Contains(raw, []byte(`"invocation"`)) {
			continue
		}
		var l Line
		if json.Unmarshal(raw, &l) != nil {
			malformed++
			lastAction = 2
			continue
		}
		if l.Kind != KindResult && l.Kind != KindInvocation {
			continue
		}
		out = append(out, l)
		lastAction = 1
	}
	if serr := sc.Err(); serr != nil {
		return out, malformed, truncated, window, serr
	}
	if !endsNewline && lastAction == 1 && len(out) > 0 {
		out = out[:len(out)-1]
	} else if !endsNewline && lastAction == 2 && malformed > 0 {
		malformed--
	}
	return out, malformed, truncated, window, nil
}
