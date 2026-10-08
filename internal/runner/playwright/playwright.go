// Package playwright registers the tests of each tracked Playwright config
// through Playwright's own listing (playwright test --list) and runs the
// selected ones by location.
//
// A key names a test by its spec file and its describe and test titles. A
// test that several projects run is one key, failing when any project fails
// it. A name repeated within one file and project cannot be told apart, so it
// fails discovery.
package playwright

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/proc"
	"github.com/undervoke/ytif/internal/runner/nodetool"
)

const Runner = "playwright-test"

// Separator joins describe titles and a test title into a key name.
const Separator = " > "

var configFile = regexp.MustCompile(`^playwright\.config\.(ts|mts|cts|js|mjs|cjs)$`)

// test is one listed test: its key and the line a location filter selects.
type test struct {
	unit, name string
	line       int
}

func (t test) key() check.Key { return check.Key{Runner: Runner, Unit: t.unit, Name: t.name} }

// config is one Playwright config and the tests it lists.
type config struct {
	file  string // repository-relative config path
	tests []test
}

// Source lists and runs the tests of every tracked Playwright config.
// Discover keeps each key's config and line for Run.
type Source struct {
	configs []*config
	owner   map[check.Key]*config
	line    map[check.Key]int
}

func (s *Source) Runners() []string { return []string{Runner} }

// Discover lists every config. A config that cannot be listed, a file two
// configs list, and a repeated name make discovery incomplete.
func (s *Source) Discover(ctx context.Context, repo check.Repo) ([]check.Key, []check.Invocation, error) {
	s.configs = nil
	s.owner, s.line = map[check.Key]*config{}, map[check.Key]int{}
	var keys []check.Key
	var invs []check.Invocation
	var errs []error
	claimed := map[string]string{} // unit → config
	for _, f := range nodetool.Configs(repo.Tracked, configFile.MatchString) {
		c := &config{file: f}
		tests, inv, err := list(ctx, repo, f)
		if inv != nil {
			invs = append(invs, *inv)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f, err))
		}
		for _, t := range tests {
			if other, ok := claimed[t.unit]; ok && other != f {
				errs = append(errs, fmt.Errorf("%s: listed by both %s and %s; one spec file belongs to one config", t.unit, other, f))
				continue
			}
			claimed[t.unit] = f
			c.tests = append(c.tests, t)
			k := t.key()
			s.owner[k], s.line[k] = c, t.line
			keys = append(keys, k)
		}
		s.configs = append(s.configs, c)
	}
	return keys, invs, errors.Join(errs...)
}

// report is the JSON reporter's document, for listings and runs alike.
type report struct {
	Config struct {
		RootDir string `json:"rootDir"`
	} `json:"config"`
	Suites []suite    `json:"suites"`
	Errors []errorDoc `json:"errors"`
}

type suite struct {
	Title  string  `json:"title"`
	Specs  []spec  `json:"specs"`
	Suites []suite `json:"suites"`
}

type spec struct {
	Title string `json:"title"`
	File  string `json:"file"`
	Line  int    `json:"line"`
	Tests []struct {
		ProjectName string `json:"projectName"`
		Status      string `json:"status"`
		Results     []struct {
			Duration float64    `json:"duration"`
			Errors   []errorDoc `json:"errors"`
		} `json:"results"`
	} `json:"tests"`
}

type errorDoc struct {
	Message string `json:"message"`
}

// walk calls fn for every spec with its describe titles; a top-level suite
// is a file, whose title is not part of a name.
func (r *report) walk(fn func(titles []string, sp spec)) {
	var visit func(s suite, titles []string)
	visit = func(s suite, titles []string) {
		for _, sp := range s.Specs {
			fn(titles, sp)
		}
		for _, sub := range s.Suites {
			visit(sub, append(append([]string(nil), titles...), sub.Title))
		}
	}
	for _, file := range r.Suites {
		visit(file, nil)
	}
}

func (r *report) errorText() string {
	var msgs []string
	for _, e := range r.Errors {
		msgs = append(msgs, strings.TrimSpace(e.Message))
	}
	return strings.Join(msgs, "\n")
}

// invoke runs playwright test with args from the config's directory and
// reads the JSON report it writes.
func invoke(ctx context.Context, repo check.Repo, file, what string, args ...string) (*report, check.Invocation, string, error) {
	dir := path.Dir(file)
	inv := check.Invocation{Runner: Runner, Unit: file, What: what}
	bin, err := nodetool.Bin(repo.Root, dir, "playwright")
	if err != nil {
		inv.Err = err
		return nil, inv, "", err
	}
	out := filepath.Join(repo.Scratch, "playwright", nodetool.ShortHash(file)+"."+what+".json")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		inv.Err = err
		return nil, inv, "", err
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"test", "--config", path.Base(file), "--reporter=json"}, args...)...)
	cmd.Dir = filepath.Join(repo.Root, filepath.FromSlash(dir))
	cmd.Env = append(os.Environ(), "PLAYWRIGHT_JSON_OUTPUT_FILE="+out, "NO_COLOR=1", "FORCE_COLOR=0")
	var console bytes.Buffer
	cmd.Stdout, cmd.Stderr = &console, &console
	start := time.Now()
	runErr := proc.Run(cmd)
	inv.Elapsed, inv.Interrupted = time.Since(start), errors.Is(runErr, proc.ErrInterrupted)
	var r report
	data, err := os.ReadFile(out)
	if err == nil {
		err = json.Unmarshal(data, &r)
	}
	if err != nil {
		return nil, inv, console.String(), fmt.Errorf("read playwright json report: %w", err)
	}
	return &r, inv, console.String(), runErr
}

// list returns a config's tests.
func list(ctx context.Context, repo check.Repo, file string) ([]test, *check.Invocation, error) {
	r, inv, console, err := invoke(ctx, repo, file, "list", "--list")
	switch {
	case r == nil:
		inv.Err = fmt.Errorf("playwright --list: %v\n%s", err, strings.TrimSpace(console))
		return nil, &inv, inv.Err
	case len(r.Errors) > 0:
		inv.Err = fmt.Errorf("playwright --list reported errors:\n%s", r.errorText())
		return nil, &inv, inv.Err
	case err != nil:
		inv.Err = fmt.Errorf("playwright --list: %v\n%s", err, strings.TrimSpace(console))
		return nil, &inv, inv.Err
	}
	var tests []test
	var errs []error
	seen := map[test]bool{}
	repeats := map[string]int{}
	tracked := nodetool.Tracked(repo.Tracked)
	r.walk(func(titles []string, sp spec) {
		unit, err := nodetool.Unit(repo.Root, filepath.Join(r.Config.RootDir, filepath.FromSlash(sp.File)))
		if err != nil {
			errs = append(errs, err)
			return
		}
		if !tracked[unit] {
			return
		}
		t := test{unit: unit, name: strings.Join(append(append([]string(nil), titles...), sp.Title), Separator), line: sp.Line}
		for _, pt := range sp.Tests {
			repeats[pt.ProjectName+"\x00"+t.unit+"\x00"+t.name]++
		}
		if !seen[t] {
			seen[t] = true
			tests = append(tests, t)
		}
	})
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
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return tests, &inv, errors.Join(errs...)
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
	var rep check.Report
	for _, c := range order {
		if ctx.Err() != nil {
			break
		}
		r := s.runConfig(ctx, repo, c, byConfig[c])
		rep.Results = append(rep.Results, r.Results...)
		rep.Skipped = append(rep.Skipped, r.Skipped...)
		rep.Invocations = append(rep.Invocations, r.Invocations...)
	}
	return rep, nil
}

func (s *Source) runConfig(ctx context.Context, repo check.Repo, c *config, keys []check.Key) check.Report {
	selected := map[check.Key]bool{}
	var filters []string
	have := map[string]bool{}
	for _, k := range keys {
		selected[k] = true
		// Playwright matches a filter against the absolute spec path.
		f := filepath.Join(repo.Root, filepath.FromSlash(k.Unit)) + ":" + strconv.Itoa(s.line[k])
		if !have[f] {
			have[f] = true
			filters = append(filters, f)
		}
	}
	sort.Strings(filters)
	ran := append([]check.Key(nil), keys...)
	for _, k := range keys {
		for _, o := range s.coRuns(k) {
			if !selected[o] && !slices.Contains(ran, o) {
				ran = append(ran, o)
			}
		}
	}

	r, inv, console, runErr := invoke(ctx, repo, c.file, "run", filters...)
	inv.Keys = ran
	var rep check.Report
	tallies := map[check.Key]*nodetool.Tally{}
	if r != nil {
		r.walk(func(titles []string, sp spec) {
			unit, err := nodetool.Unit(repo.Root, filepath.Join(r.Config.RootDir, filepath.FromSlash(sp.File)))
			if err != nil {
				return
			}
			k := check.Key{Runner: Runner, Unit: unit, Name: strings.Join(append(append([]string(nil), titles...), sp.Title), Separator)}
			for _, pt := range sp.Tests {
				st := nodetool.Skipped
				switch pt.Status {
				case "expected", "flaky":
					st = nodetool.Passed
				case "unexpected":
					st = nodetool.Failed
				}
				var d time.Duration
				var msgs []string
				for _, res := range pt.Results {
					d += time.Duration(res.Duration * float64(time.Millisecond))
					for _, e := range res.Errors {
						msgs = append(msgs, strings.TrimSpace(e.Message))
					}
				}
				if tallies[k] == nil {
					tallies[k] = &nodetool.Tally{}
				}
				tallies[k].Add(st, d, strings.Join(msgs, "\n"))
			}
		})
	}
	unresolved, anyFailed := false, false
	for i, k := range ran {
		t := tallies[k]
		if !t.Seen() {
			unresolved = unresolved || i < len(keys)
			continue
		}
		t.Apply(&rep, k)
		anyFailed = anyFailed || t.Failed()
	}
	switch {
	case inv.Interrupted:
		inv.Err = fmt.Errorf("playwright test %s: %v", c.file, runErr)
	case inv.Err != nil:
	case r != nil && len(r.Errors) > 0:
		inv.Err = fmt.Errorf("playwright test %s reported errors:\n%s", c.file, r.errorText())
	case unresolved || (runErr != nil && !anyFailed):
		cause := "reported no result for some selected tests"
		if runErr != nil {
			cause = runErr.Error()
		}
		if !unresolved {
			cause = "failed outside its tests: " + cause
		}
		inv.Err = fmt.Errorf("playwright test %s: %s\n%s", c.file, cause, strings.TrimSpace(console))
	}
	rep.Invocations = append(rep.Invocations, inv)
	return rep
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
		if o := t.key(); o != k && t.unit == k.Unit && t.line == s.line[k] {
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
