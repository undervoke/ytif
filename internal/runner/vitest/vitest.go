// Package vitest registers the tests of each tracked Vitest config through
// Vitest's own collection (vitest list) and runs the selected ones by
// location.
//
// A key names a test by its file and its suite and test titles. A test that
// several of a config's projects run is one key, failing when any project
// fails it. A name repeated within one file and project cannot be told apart,
// so it fails discovery. Listing loads every test file, so a config's listing
// is cached by the tracked paths, the tracked content under its directory, the
// tracked lockfiles, and the listed test files outside its directory.
package vitest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/proc"
	"github.com/undervoke/ytif/internal/runner/nodetool"
)

const Runner = "vitest-test"

// cacheFormat changes whenever a cached listing's meaning changes.
const cacheFormat = "ytif vitest list 2"

var (
	vitestConfig = regexp.MustCompile(`^vitest\.config\.(ts|mts|cts|js|mjs|cjs)$`)
	viteConfig   = regexp.MustCompile(`^vite\.config\.(ts|mts|cts|js|mjs|cjs)$`)
	lockfile     = map[string]bool{"pnpm-lock.yaml": true, "package-lock.json": true, "yarn.lock": true, "bun.lock": true, "bun.lockb": true}
)

// test is one listed test: its key and the line a location filter selects.
type test struct {
	Unit string `json:"unit"`
	Name string `json:"name"`
	Line int    `json:"line"`
}

func (t test) key() check.Key { return check.Key{Runner: Runner, Unit: t.Unit, Name: t.Name} }

// entry is a cached listing. Outside holds the content hash of each listed
// test file outside the config's directory, which the cache key leaves out;
// a hit requires them unchanged.
type entry struct {
	Tests   []test            `json:"tests"`
	Outside map[string]string `json:"outside,omitempty"`
}

// config is one Vitest config and the tests it lists.
type config struct {
	file  string // repository-relative config path
	tests []test
}

func (c *config) dir() string { return path.Dir(c.file) }

// Source lists and runs the tests of every tracked Vitest config. Discover
// keeps each key's config and line for Run.
type Source struct {
	configs []*config
	owner   map[check.Key]*config
	line    map[check.Key]int
}

func (s *Source) Runners() []string { return []string{Runner} }

// Discover lists every config. A config that cannot be listed, a file two
// configs list, and a repeated name make discovery incomplete.
func (s *Source) Discover(ctx context.Context, repo check.Repo) ([]check.Key, []check.Invocation, error) {
	files, errs := configFiles(repo)
	cache := cacheDir(repo.Root)
	s.configs = make([]*config, len(files))
	invs := make([]*check.Invocation, len(files))
	lerrs := make([]error, len(files))
	sem := make(chan struct{}, nodetool.Parallel)
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c := &config{file: f}
			c.tests, invs[i], lerrs[i] = list(ctx, repo, f, cache)
			s.configs[i] = c
		}()
	}
	wg.Wait()

	s.owner, s.line = map[check.Key]*config{}, map[check.Key]int{}
	var keys []check.Key
	var listing []check.Invocation
	claimed := map[string]string{} // unit → config
	for i, c := range s.configs {
		if invs[i] != nil {
			listing = append(listing, *invs[i])
		}
		if lerrs[i] != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.file, lerrs[i]))
		}
		for _, t := range c.tests {
			if other, ok := claimed[t.Unit]; ok && other != c.file {
				errs = append(errs, fmt.Errorf("%s: listed by both %s and %s; one test file belongs to one config", t.Unit, other, c.file))
				continue
			}
			claimed[t.Unit] = c.file
			k := t.key()
			s.owner[k], s.line[k] = c, t.Line
			keys = append(keys, k)
		}
	}
	return keys, listing, errors.Join(errs...)
}

// configFiles returns the tracked Vitest configs: vitest.config.*, and a
// vite.config.* that mentions vitest where no vitest.config.* sits beside it,
// as Vitest itself prefers the former.
func configFiles(repo check.Repo) ([]string, []error) {
	own := map[string]bool{}
	for _, f := range nodetool.Configs(repo.Tracked, vitestConfig.MatchString) {
		own[path.Dir(f)] = true
	}
	var out []string
	var errs []error
	for _, f := range nodetool.Configs(repo.Tracked, func(b string) bool { return vitestConfig.MatchString(b) || viteConfig.MatchString(b) }) {
		if viteConfig.MatchString(path.Base(f)) {
			if own[path.Dir(f)] {
				continue
			}
			src, err := os.ReadFile(filepath.Join(repo.Root, filepath.FromSlash(f)))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !bytes.Contains(src, []byte("vitest")) {
				continue
			}
		}
		out = append(out, f)
	}
	return out, errs
}

// cacheDir is where listings are kept across gates and worktrees, or "" when
// the git common directory cannot be found.
func cacheDir(root string) string {
	common, err := gitx.CommonDir(root)
	if err != nil {
		return ""
	}
	return filepath.Join(common, "ytif", "cache", "vitest")
}

// list returns a config's tests from the cache, or lists them and caches the
// complete listing.
func list(ctx context.Context, repo check.Repo, file, cache string) ([]test, *check.Invocation, error) {
	var cached string
	if cache != "" {
		if h, err := cacheKey(repo, file); err == nil {
			cached = filepath.Join(cache, h+".json")
			var e entry
			if data, err := os.ReadFile(cached); err == nil && json.Unmarshal(data, &e) == nil && e.current(repo.Root) {
				return e.Tests, nil, nil
			}
		}
	}

	dir := path.Dir(file)
	bin, err := nodetool.Bin(repo.Root, dir, "vitest")
	if err != nil {
		return nil, nil, err
	}
	out := filepath.Join(repo.Scratch, "vitest", nodetool.ShortHash(file)+".list.json")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, bin, "list", "--config", path.Base(file), "--json="+out, "--includeTaskLocation")
	cmd.Dir = filepath.Join(repo.Root, filepath.FromSlash(dir))
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "FORCE_COLOR=0")
	var console bytes.Buffer
	cmd.Stdout, cmd.Stderr = &console, &console
	start := time.Now()
	runErr := proc.Run(cmd)
	inv := &check.Invocation{Runner: Runner, Unit: file, What: "list", Elapsed: time.Since(start), Interrupted: errors.Is(runErr, proc.ErrInterrupted)}
	if runErr != nil {
		inv.Err = fmt.Errorf("vitest list: %v\n%s", runErr, strings.TrimSpace(console.String()))
		return nil, inv, inv.Err
	}
	tests, err := readList(repo.Root, nodetool.Tracked(repo.Tracked), out)
	if err != nil {
		return tests, inv, err
	}
	if cached != "" {
		if e, err := outside(repo.Root, dir, tests); err == nil {
			_ = store(cached, e)
		}
	}
	return tests, inv, nil
}

// readList reads vitest list's JSON: one entry per test and project.
func readList(root string, tracked map[string]bool, file string) ([]test, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("no vitest list output: %w", err)
	}
	var entries []struct {
		Name        string `json:"name"`
		File        string `json:"file"`
		ProjectName string `json:"projectName"`
		Location    *struct {
			Line int `json:"line"`
		} `json:"location"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("read vitest list output: %w", err)
	}
	var tests []test
	var errs []error
	seen := map[test]bool{}
	repeats := map[string]int{}
	for _, e := range entries {
		unit, err := nodetool.Unit(root, e.File)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !tracked[unit] {
			continue
		}
		if e.Location == nil {
			errs = append(errs, fmt.Errorf("%s: %q has no location", unit, e.Name))
			continue
		}
		repeats[e.ProjectName+"\x00"+unit+"\x00"+e.Name]++
		t := test{Unit: unit, Name: e.Name, Line: e.Location.Line}
		if !seen[t] {
			seen[t] = true
			tests = append(tests, t)
		}
	}
	for id, n := range repeats {
		if n > 1 {
			p := strings.SplitN(id, "\x00", 3)
			where := ""
			if p[0] != "" {
				where = " in project " + strconv.Quote(p[0])
			}
			errs = append(errs, fmt.Errorf("%s: %q is named %d times%s; give each test a unique name", p[1], p[2], n, where))
		}
	}
	sort.Slice(tests, func(i, j int) bool {
		if tests[i].Unit != tests[j].Unit {
			return tests[i].Unit < tests[j].Unit
		}
		return tests[i].Name < tests[j].Name
	})
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return tests, errors.Join(errs...)
}

// cacheKey hashes what a config's listing depends on as far as ytif can see:
// the config path, every tracked path, so a test file added or removed
// anywhere refreshes it, and the content of every tracked file under the
// config's directory and of every tracked lockfile.
func cacheKey(repo check.Repo, file string) (string, error) {
	dir := path.Dir(file)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", cacheFormat, file)
	for _, f := range repo.Tracked {
		fmt.Fprintf(h, "%s\x00", f)
		if !under(dir, f) && !lockfile[path.Base(f)] {
			continue
		}
		sum, err := fileHash(repo.Root, f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00", sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func under(dir, f string) bool { return dir == "." || strings.HasPrefix(f, dir+"/") }

func fileHash(root, f string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// outside records the listed test files outside dir for a cached listing.
func outside(root, dir string, tests []test) (entry, error) {
	e := entry{Tests: tests}
	for _, t := range tests {
		if under(dir, t.Unit) || e.Outside[t.Unit] != "" {
			continue
		}
		sum, err := fileHash(root, t.Unit)
		if err != nil {
			return e, err
		}
		if e.Outside == nil {
			e.Outside = map[string]string{}
		}
		e.Outside[t.Unit] = sum
	}
	return e, nil
}

// current reports whether the listed test files outside the config's
// directory still have the content the listing was made from.
func (e entry) current(root string) bool {
	for f, want := range e.Outside {
		if got, err := fileHash(root, f); err != nil || got != want {
			return false
		}
	}
	return true
}

// store writes a listing under its key. Worktrees sharing a key may differ
// only in the files outside the config's directory, which a reader checks,
// so a rename that replaces another worktree's listing costs a miss at most.
func store(name string, e entry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), name)
}

// Run runs each config's selected tests in one process, selected by location.
func (s *Source) Run(ctx context.Context, repo check.Repo, keys []check.Key, in check.Input) (check.Report, error) {
	if s.owner == nil {
		if _, _, err := s.Discover(ctx, repo); err != nil && len(s.owner) == 0 {
			return check.Report{}, err
		}
	}
	byConfig := map[*config][]check.Key{}
	var order []*config
	for _, k := range keys {
		c := s.owner[k]
		if c == nil {
			continue // not discovered; the gate reports these keys as without outcome
		}
		if byConfig[c] == nil {
			order = append(order, c)
		}
		byConfig[c] = append(byConfig[c], k)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].file < order[j].file })

	reports := make([]check.Report, len(order))
	sem := make(chan struct{}, nodetool.Parallel)
	var wg sync.WaitGroup
	for i, c := range order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			reports[i] = s.runConfig(ctx, repo, c, byConfig[c])
		}()
	}
	wg.Wait()

	var rep check.Report
	for _, r := range reports {
		rep.Results = append(rep.Results, r.Results...)
		rep.Skipped = append(rep.Skipped, r.Skipped...)
		rep.Invocations = append(rep.Invocations, r.Invocations...)
	}
	return rep, nil
}

func (s *Source) runConfig(ctx context.Context, repo check.Repo, c *config, keys []check.Key) check.Report {
	dir := filepath.Join(repo.Root, filepath.FromSlash(c.dir()))
	selected := map[check.Key]bool{}
	var filters []string
	have := map[string]bool{}
	for _, k := range keys {
		selected[k] = true
		rel, err := filepath.Rel(dir, filepath.Join(repo.Root, filepath.FromSlash(k.Unit)))
		if err != nil {
			rel = k.Unit
		}
		f := filepath.ToSlash(rel) + ":" + strconv.Itoa(s.line[k])
		if !have[f] {
			have[f] = true
			filters = append(filters, f)
		}
	}
	sort.Strings(filters)
	// Tests a location filter cannot separate from the selection run too,
	// and their outcomes are reported with it.
	ran := append([]check.Key(nil), keys...)
	for _, k := range keys {
		for _, o := range s.coRuns(k) {
			if !selected[o] && !slices.Contains(ran, o) {
				ran = append(ran, o)
			}
		}
	}

	fail := func(err error) check.Report {
		return check.Report{Invocations: []check.Invocation{{Runner: Runner, Unit: c.file, What: "run", Err: err, Keys: keys}}}
	}
	bin, err := nodetool.Bin(repo.Root, c.dir(), "vitest")
	if err != nil {
		return fail(err)
	}
	report := filepath.Join(repo.Scratch, "vitest", nodetool.ShortHash(c.file)+".run.json")
	if err := os.MkdirAll(filepath.Dir(report), 0o755); err != nil {
		return fail(err)
	}
	args := append([]string{"run", "--config", path.Base(c.file), "--reporter=json", "--outputFile=" + report}, filters...)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "FORCE_COLOR=0")
	var console bytes.Buffer
	cmd.Stdout, cmd.Stderr = &console, &console
	start := time.Now()
	runErr := proc.Run(cmd)
	elapsed := time.Since(start)

	tallies, fileErrs, readErr := readRun(repo.Root, report)
	var rep check.Report
	unresolved := false
	anyFailed := false
	for i, k := range ran {
		t := tallies[k]
		if !t.Seen() {
			unresolved = unresolved || i < len(keys)
			continue
		}
		t.Apply(&rep, k)
		anyFailed = anyFailed || t.Failed()
	}
	inv := check.Invocation{Runner: Runner, Unit: c.file, What: "run", Elapsed: elapsed, Keys: ran, Interrupted: errors.Is(runErr, proc.ErrInterrupted)}
	switch {
	case inv.Interrupted:
		inv.Err = fmt.Errorf("vitest run %s: %v", c.file, runErr)
	case unresolved || (runErr != nil && !anyFailed):
		cause := "reported no result for some selected tests"
		switch {
		case len(fileErrs) > 0:
			cause = strings.Join(fileErrs, "\n")
		case readErr != nil:
			cause = readErr.Error()
		case runErr != nil:
			cause = runErr.Error()
		}
		if !unresolved {
			cause = "failed outside its tests: " + cause
		}
		inv.Err = fmt.Errorf("vitest run %s: %s\n%s", c.file, cause, strings.TrimSpace(console.String()))
	}
	rep.Invocations = append(rep.Invocations, inv)
	return rep
}

// readRun reads the JSON reporter's output into one tally per key, and the
// file-level errors it reports, such as a file that failed to load.
func readRun(root, file string) (map[check.Key]*nodetool.Tally, []string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, nil, fmt.Errorf("no vitest json report: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	var doc struct {
		TestResults []struct {
			Name             string `json:"name"`
			Message          string `json:"message"`
			AssertionResults []struct {
				AncestorTitles  []string `json:"ancestorTitles"`
				Title           string   `json:"title"`
				Status          string   `json:"status"`
				Duration        *float64 `json:"duration"`
				FailureMessages []string `json:"failureMessages"`
			} `json:"assertionResults"`
		} `json:"testResults"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("read vitest json report: %w", err)
	}
	tallies := map[check.Key]*nodetool.Tally{}
	var fileErrs []string
	for _, tr := range doc.TestResults {
		unit, err := nodetool.Unit(root, tr.Name)
		if err != nil {
			fileErrs = append(fileErrs, err.Error())
			continue
		}
		if strings.TrimSpace(tr.Message) != "" {
			fileErrs = append(fileErrs, unit+": "+strings.TrimSpace(tr.Message))
		}
		for _, a := range tr.AssertionResults {
			k := check.Key{Runner: Runner, Unit: unit, Name: strings.Join(append(append([]string(nil), a.AncestorTitles...), a.Title), " > ")}
			st := nodetool.Skipped
			switch a.Status {
			case "passed":
				st = nodetool.Passed
			case "failed":
				st = nodetool.Failed
			}
			var d time.Duration
			if a.Duration != nil {
				d = time.Duration(*a.Duration * float64(time.Millisecond))
			}
			if tallies[k] == nil {
				tallies[k] = &nodetool.Tally{}
			}
			tallies[k].Add(st, d, strings.Join(a.FailureMessages, "\n"))
		}
	}
	return tallies, fileErrs, nil
}

// coRuns returns the tests of k's config in k's file on k's line, which a
// location filter runs together with it.
func (s *Source) coRuns(k check.Key) []check.Key {
	c := s.owner[k]
	if c == nil {
		return nil
	}
	var out []check.Key
	for _, t := range c.tests {
		if o := t.key(); o != k && t.Unit == k.Unit && t.Line == s.line[k] {
			out = append(out, o)
		}
	}
	return out
}

// CoRuns maps each test to the tests on its line in its file.
func (s *Source) CoRuns() map[check.Key][]check.Key {
	out := map[check.Key][]check.Key{}
	for k := range s.owner {
		if others := s.coRuns(k); len(others) > 0 {
			out[k] = others
		}
	}
	return out
}
