package jstest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/proc"
)

const parallel = 4

// Source runs the test files of one runner, Bun or Node. Discover keeps
// each file's tree for Run.
type Source struct {
	Runner string
	files  map[string]*file
}

func (s *Source) Runners() []string { return []string{s.Runner} }

// Discover parses every test file. A file whose runner cannot be told makes
// both runners' discovery incomplete.
func (s *Source) Discover(ctx context.Context, repo check.Repo) ([]check.Key, []check.Invocation, error) {
	s.files = map[string]*file{}
	var keys []check.Key
	var errs []error
	for _, unit := range repo.Tracked {
		if !isTestFile(unit) {
			continue
		}
		f, runner, err := parseFile(repo.Root, unit)
		if runner != "" && runner != s.Runner {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		s.files[unit] = f
		for _, t := range f.tests {
			keys = append(keys, check.Key{Runner: f.runner, Unit: unit, Name: strings.Join(t.path(), Separator)})
		}
	}
	return keys, nil, errors.Join(errs...)
}

// Run runs each file's selected tests in one process per file.
func (s *Source) Run(ctx context.Context, repo check.Repo, keys []check.Key, in check.Input) (check.Report, error) {
	if s.files == nil {
		if _, _, err := s.Discover(ctx, repo); err != nil && len(s.files) == 0 {
			return check.Report{}, err
		}
	}
	byUnit := map[string][]check.Key{}
	var units []string
	for _, k := range keys {
		if byUnit[k.Unit] == nil {
			units = append(units, k.Unit)
		}
		byUnit[k.Unit] = append(byUnit[k.Unit], k)
	}
	sort.Strings(units)

	reports := make([]check.Report, len(units))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, unit := range units {
		f := s.files[unit]
		if f == nil {
			continue // not discovered; the gate reports these keys as without outcome
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			reports[i] = runFile(ctx, repo, f, byUnit[unit])
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

// outcome is one test's reported result, keyed by its path.
type outcome struct {
	fail, skip bool
	elapsed    time.Duration
	output     string
}

func runFile(ctx context.Context, repo check.Repo, f *file, keys []check.Key) check.Report {
	selected := map[string]bool{}
	for _, k := range keys {
		selected[k.Name] = true
	}
	alts := map[string]bool{}
	var list []string
	for _, t := range f.tests {
		if selected[strings.Join(t.path(), Separator)] {
			if a := alternative(f, t); !alts[a] {
				alts[a] = true
				list = append(list, regexp.QuoteMeta(a))
			}
		}
	}
	pattern := "^(" + strings.Join(list, "|") + ")$"
	// Tests the pattern cannot separate from the selection run too, and
	// their outcomes are reported with it.
	ran := append([]check.Key(nil), keys...)
	for _, t := range f.tests {
		if name := strings.Join(t.path(), Separator); !selected[name] && runs(f, t, alts) {
			ran = append(ran, check.Key{Runner: f.runner, Unit: f.unit, Name: name})
		}
	}

	report := filepath.Join(repo.Scratch, "jstest", shortHash(f.unit))
	if err := os.MkdirAll(filepath.Dir(report), 0o755); err != nil {
		return check.Report{Invocations: []check.Invocation{{Runner: f.runner, Unit: f.unit, What: "run", Err: err, Keys: keys}}}
	}
	var cmd *exec.Cmd
	if f.runner == Bun {
		report += ".xml"
		cmd = exec.CommandContext(ctx, "bun", "test", "./"+f.unit, "--test-name-pattern", pattern, "--reporter=junit", "--reporter-outfile="+report)
	} else {
		report += ".tap"
		cmd = exec.CommandContext(ctx, "node", "--test", "--test-reporter=tap", "--test-reporter-destination="+report, "--test-name-pattern="+pattern, "./"+f.unit)
	}
	cmd.Dir = repo.Root
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "FORCE_COLOR=0")
	var console bytes.Buffer
	cmd.Stdout, cmd.Stderr = &console, &console
	start := time.Now()
	runErr := proc.Run(cmd)
	elapsed := time.Since(start)

	var results map[string]*outcome
	var readErr error
	if f.runner == Bun {
		results, readErr = readJUnit(report, console.String())
	} else {
		results, readErr = readTAP(report)
	}

	var rep check.Report
	unresolved := false
	for i, k := range ran {
		o := results[k.Name]
		switch {
		case o == nil:
			unresolved = unresolved || i < len(keys)
		case o.skip:
			rep.Skipped = append(rep.Skipped, k)
		case o.fail:
			rep.Results = append(rep.Results, check.Result{Key: k, Outcome: check.Fail, Elapsed: o.elapsed, Timed: true, Output: o.output})
		default:
			rep.Results = append(rep.Results, check.Result{Key: k, Outcome: check.Pass, Elapsed: o.elapsed, Timed: true})
		}
	}
	// Only failures that reach the rail explain a failed process; a hook
	// failure reported against a suite does not.
	anyFailed := false
	for _, r := range rep.Results {
		anyFailed = anyFailed || r.Outcome == check.Fail
	}
	inv := check.Invocation{Runner: f.runner, Unit: f.unit, What: "run", Elapsed: elapsed, Keys: ran, Interrupted: errors.Is(runErr, proc.ErrInterrupted)}
	switch {
	case inv.Interrupted:
		inv.Err = fmt.Errorf("%s %s: %v", cmd.Args[0], f.unit, runErr)
	case unresolved:
		cause := "reported no result for some selected tests"
		switch {
		case readErr != nil:
			cause = readErr.Error()
		case runErr != nil:
			cause = runErr.Error()
		}
		inv.Err = fmt.Errorf("%s %s: %s\n%s", cmd.Args[0], f.unit, cause, strings.TrimSpace(console.String()))
	case runErr != nil && !anyFailed:
		// Hooks or module code failed while no reported test failed; the
		// runner may report the failure against a suite or the file.
		var outside []string
		for name, o := range results {
			if o.fail && !slices.ContainsFunc(ran, func(k check.Key) bool { return k.Name == name }) {
				outside = append(outside, name+":\n"+o.output)
			}
		}
		sort.Strings(outside)
		inv.Err = fmt.Errorf("%s %s failed outside its tests: %v\n%s", cmd.Args[0], f.unit, runErr,
			strings.TrimSpace(strings.Join(append(outside, console.String()), "\n")))
	}
	rep.Invocations = append(rep.Invocations, inv)
	return rep
}

// alternative is the name a runner's pattern compares for t: bun the
// space-joined suite and test names, node the same names trimmed.
func alternative(f *file, t *node) string {
	full := strings.Join(t.path(), " ")
	if f.runner == Node {
		return jsTrim(full)
	}
	return full
}

// nodeRoot names node's root test, the ancestor of every test.
const nodeRoot = "<root>"

// runs reports whether an anchored pattern of alts runs t. Bun matches a
// test's full name only. Node matches the test's own name and its trimmed
// full name, and the same of every ancestor, root included.
func runs(f *file, t *node, alts map[string]bool) bool {
	if f.runner == Bun {
		return alts[strings.Join(t.path(), " ")]
	}
	for n := t; n != nil && n.parent != nil; n = n.parent {
		if alts[n.name] || alts[jsTrim(strings.Join(n.path(), " "))] {
			return true
		}
	}
	return alts[nodeRoot]
}

type junitSuite struct {
	Name   string       `xml:"name,attr"`
	Suites []junitSuite `xml:"testsuite"`
	Cases  []struct {
		Name    string    `xml:"name,attr"`
		Time    string    `xml:"time,attr"`
		Failure *struct{} `xml:"failure"`
		Error   *struct{} `xml:"error"`
		Skipped *struct{} `xml:"skipped"`
	} `xml:"testcase"`
}

// readJUnit reads bun's report: one suite per file, nested suites per
// describe. Bun reports a failure without its message, so the console block
// that precedes each "(fail) path" line becomes the output.
func readJUnit(file, console string) (map[string]*outcome, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("no junit report: %w", err)
	}
	var doc struct {
		Suites []junitSuite `xml:"testsuite"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("read junit report: %w", err)
	}
	failures := failureBlocks(console)
	results := map[string]*outcome{}
	var walk func(s junitSuite, path []string)
	walk = func(s junitSuite, path []string) {
		for _, c := range s.Cases {
			name := strings.Join(append(append([]string(nil), path...), c.Name), Separator)
			secs, _ := strconv.ParseFloat(c.Time, 64)
			o := &outcome{elapsed: time.Duration(secs * float64(time.Second))}
			switch {
			case c.Failure != nil || c.Error != nil:
				o.fail, o.output = true, failures[name]
			case c.Skipped != nil:
				o.skip = true
			}
			results[name] = o
		}
		for _, sub := range s.Suites {
			walk(sub, append(append([]string(nil), path...), sub.Name))
		}
	}
	for _, fileSuite := range doc.Suites {
		walk(fileSuite, nil)
	}
	return results, nil
}

var bunStatus = regexp.MustCompile(`^\((pass|fail|skip|todo)\) (.*?)(?: \[[0-9.]+m?s\])?$`)

func failureBlocks(console string) map[string]string {
	blocks := map[string]string{}
	var block []string
	for _, line := range strings.Split(console, "\n") {
		if strings.HasPrefix(line, "bun test v") {
			continue // the version banner precedes the first file
		}
		m := bunStatus.FindStringSubmatch(line)
		if m == nil {
			block = append(block, line)
			continue
		}
		if m[1] == "fail" {
			blocks[m[2]] = strings.Trim(strings.Join(block, "\n"), "\n")
		}
		block = nil
	}
	return blocks
}

var tapResult = regexp.MustCompile(`^(not ok|ok) \d+(?: - (.*))?$`)

// readTAP reads node's TAP report. "# Subtest:" lines name the open suites
// and tests at each indentation level; a result line closes one of them.
func readTAP(file string) (map[string]*outcome, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("no TAP report: %w", err)
	}
	results := map[string]*outcome{}
	var stack []string
	var last *outcome
	var yaml []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		raw := sc.Text()
		trimmed := strings.TrimLeft(raw, " ")
		level := (len(raw) - len(trimmed)) / 4
		switch {
		case last != nil && yaml != nil:
			if trimmed == "..." {
				applyYAML(last, yaml)
				last, yaml = nil, nil
			} else {
				yaml = append(yaml, trimmed)
			}
		case last != nil && trimmed == "---":
			yaml = []string{}
		case strings.HasPrefix(trimmed, "# Subtest: "):
			stack = append(stack[:min(level, len(stack))], tapUnescape(strings.TrimPrefix(trimmed, "# Subtest: ")))
		default:
			m := tapResult.FindStringSubmatch(trimmed)
			if m == nil {
				continue
			}
			name, directive := splitDirective(m[2])
			path := append(append([]string(nil), stack[:min(level, len(stack))]...), name)
			o := &outcome{fail: m[1] == "not ok"}
			switch strings.ToUpper(strings.SplitN(directive, " ", 2)[0]) {
			case "SKIP", "TODO":
				o.fail, o.skip = false, true
			}
			results[strings.Join(path, Separator)] = o
			last, yaml = o, nil
		}
	}
	return results, sc.Err()
}

// splitDirective separates "name # SKIP reason" at the first unescaped " # ".
func splitDirective(s string) (string, string) {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i:i+3] == " # " {
			return tapUnescape(s[:i]), s[i+3:]
		}
	}
	return tapUnescape(s), ""
}

// tapUnescaper undoes node's escaping of names; names never hold the
// control characters it also escapes, which discovery rejects.
var tapUnescaper = strings.NewReplacer(`\\`, `\`, `\#`, "#")

func tapUnescape(s string) string { return tapUnescaper.Replace(s) }

// applyYAML reads duration and, for failures, keeps the diagnostic block.
func applyYAML(o *outcome, lines []string) {
	for _, l := range lines {
		if v, ok := strings.CutPrefix(l, "duration_ms: "); ok {
			if ms, err := strconv.ParseFloat(v, 64); err == nil {
				o.elapsed = time.Duration(ms * float64(time.Millisecond))
			}
		}
	}
	if o.fail {
		o.output = strings.Join(lines, "\n")
	}
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// CoRuns maps each test to the other tests its anchored name pattern also
// runs, which must be placed no later than it.
func (s *Source) CoRuns() map[check.Key][]check.Key {
	out := map[check.Key][]check.Key{}
	for _, f := range s.files {
		for _, t := range f.tests {
			key := check.Key{Runner: f.runner, Unit: f.unit, Name: strings.Join(t.path(), Separator)}
			alts := map[string]bool{alternative(f, t): true}
			for _, o := range f.tests {
				if o != t && runs(f, o, alts) {
					out[key] = append(out[key], check.Key{Runner: f.runner, Unit: f.unit, Name: strings.Join(o.path(), Separator)})
				}
			}
		}
	}
	return out
}
