// Package nodetool holds what the Vitest and Playwright sources share: their
// tracked config files, the project's local runner binary, repository-relative
// units, and folding one test's outcomes across the projects it runs in.
package nodetool

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/check"
)

// Parallel bounds the configs listed or run at once.
const Parallel = 4

// Configs returns the tracked files whose base name match accepts, sorted.
func Configs(tracked []string, match func(base string) bool) []string {
	var out []string
	for _, f := range tracked {
		if inNodeModules(f) || !match(path.Base(f)) {
			continue
		}
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func inNodeModules(p string) bool {
	for _, el := range strings.Split(p, "/") {
		if el == "node_modules" {
			return true
		}
	}
	return false
}

// Bin returns the project's own runner binary: the nearest
// node_modules/.bin/name from the config directory dir up to the repository
// root. ytif never falls back to a global install or a package manager.
func Bin(root, dir, name string) (string, error) {
	for d := dir; ; d = path.Dir(d) {
		p := filepath.Join(root, filepath.FromSlash(d), "node_modules", ".bin", name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
		if d == "." || d == "/" {
			return "", fmt.Errorf("no node_modules/.bin/%s from %s up to the repository root; install the project's dependencies", name, dir)
		}
	}
}

// Tracked returns the tracked files as a set. A runner lists the test files
// it finds on disk; ytif keeps only the tracked ones, like every other runner.
func Tracked(tracked []string) map[string]bool {
	set := make(map[string]bool, len(tracked))
	for _, f := range tracked {
		set[f] = true
	}
	return set
}

// Unit returns abs relative to root with slashes. A path outside root is an
// error, since its check could not belong to this repository.
func Unit(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%s is outside the repository", abs)
	}
	return filepath.ToSlash(rel), nil
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

// Plain removes terminal escapes that reporters keep in failure messages
// whatever the color settings.
func Plain(s string) string { return ansi.ReplaceAllString(s, "") }

// ShortHash names scratch files after a unit.
func ShortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// Status is one project's reported result for a test.
type Status int

const (
	Passed Status = iota
	Failed
	Skipped
)

// Tally folds one test's results across the projects it ran in: any failure
// fails it, otherwise any pass passes it, otherwise it was skipped.
type Tally struct {
	seen, failed, passed bool
	elapsed              time.Duration
	output               []string
}

// Add records one project's result.
func (t *Tally) Add(s Status, elapsed time.Duration, output string) {
	t.seen = true
	t.elapsed += elapsed
	switch s {
	case Failed:
		t.failed = true
		if output = Plain(output); output != "" {
			t.output = append(t.output, output)
		}
	case Passed:
		t.passed = true
	}
}

// Seen reports whether any project reported the test.
func (t *Tally) Seen() bool { return t != nil && t.seen }

// Apply adds the folded outcome for k to rep.
func (t *Tally) Apply(rep *check.Report, k check.Key) {
	switch {
	case t.failed:
		rep.Results = append(rep.Results, check.Result{Key: k, Outcome: check.Fail, Elapsed: t.elapsed, Timed: true, Output: strings.Join(t.output, "\n---\n")})
	case t.passed:
		rep.Results = append(rep.Results, check.Result{Key: k, Outcome: check.Pass, Elapsed: t.elapsed, Timed: true})
	default:
		rep.Skipped = append(rep.Skipped, k)
	}
}

// Failed reports whether the folded outcome is a failure.
func (t *Tally) Failed() bool { return t != nil && t.failed }
