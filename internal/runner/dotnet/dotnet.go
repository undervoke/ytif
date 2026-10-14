// Package dotnet registers the test methods of VSTest projects — tracked
// .csproj files that reference Microsoft.NET.Test.Sdk — and runs the selected
// ones with dotnet test.
//
// A key names a method by its fully qualified name without argument lists;
// the rows of a parameterized method or fixture share its key and outcome.
package dotnet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/proc"
)

const Runner = "dotnet-test"

// Source is the dotnet test runner. Discover builds each test project, so a
// following Run skips the build.
type Source struct {
	built map[string]bool        // unit → built by this process
	rows  map[check.Key][]string // the listed fully qualified names of each method's rows
}

func (*Source) Runners() []string { return []string{Runner} }

// CoRuns maps each method to the methods of its project whose names differ
// only in case: some adapters, xUnit's among them, match a filter's names
// case-insensitively, so selecting one runs the others.
func (s *Source) CoRuns() map[check.Key][]check.Key {
	byFold := map[string][]check.Key{}
	for k := range s.rows {
		f := k.Unit + "\x00" + strings.ToLower(k.Name)
		byFold[f] = append(byFold[f], k)
	}
	co := map[check.Key][]check.Key{}
	for _, ks := range byFold {
		slices.SortFunc(ks, func(a, b check.Key) int { return strings.Compare(a.Name, b.Name) })
		for _, k := range ks {
			for _, o := range ks {
				if o != k {
					co[k] = append(co[k], o)
				}
			}
		}
	}
	return co
}

func (s *Source) Discover(ctx context.Context, repo check.Repo) ([]check.Key, []check.Invocation, error) {
	s.built = map[string]bool{}
	s.rows = map[check.Key][]string{}
	var keys []check.Key
	var invs []check.Invocation
	units, errs := testProjects(repo)
	for _, unit := range units {
		names, rows, pinvs, err := s.list(ctx, repo, unit)
		invs = append(invs, pinvs...)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", unit, err))
		}
		for _, n := range names {
			k := check.Key{Runner: Runner, Unit: unit, Name: n}
			keys = append(keys, k)
			s.rows[k] = rows[n]
		}
	}
	return keys, invs, errors.Join(errs...)
}

// testProjects returns the tracked projects that reference the test SDK. A
// project that cannot be read fails discovery rather than vanishing.
func testProjects(repo check.Repo) ([]string, []error) {
	var units []string
	var errs []error
	for _, f := range repo.Tracked {
		if path.Ext(f) != ".csproj" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo.Root, filepath.FromSlash(f)))
		if err == nil {
			var ok bool
			ok, err = referencesTestSdk(src)
			if ok {
				units = append(units, f)
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f, err))
		}
	}
	sort.Strings(units)
	return units, errs
}

// referencesTestSdk reports whether a project file has a PackageReference
// that includes Microsoft.NET.Test.Sdk. MSBuild item types and NuGet ids
// ignore case.
func referencesTestSdk(src []byte) (bool, error) {
	dec := xml.NewDecoder(bytes.NewReader(src))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("read project: %w", err)
		}
		el, ok := tok.(xml.StartElement)
		if !ok || !strings.EqualFold(el.Name.Local, "PackageReference") {
			continue
		}
		for _, a := range el.Attr {
			if a.Name.Local != "Include" {
				continue
			}
			for _, id := range strings.Split(a.Value, ";") {
				if strings.EqualFold(strings.TrimSpace(id), "Microsoft.NET.Test.Sdk") {
					return true, nil
				}
			}
		}
	}
}

// list builds the project and lists its tests by method, keeping each
// method's listed rows for exact selection.
func (s *Source) list(ctx context.Context, repo check.Repo, unit string) ([]string, map[string][]string, []check.Invocation, error) {
	proj := filepath.Join(repo.Root, filepath.FromSlash(unit))
	var invs []check.Invocation
	invocation := func(what string, elapsed time.Duration, err error) {
		invs = append(invs, check.Invocation{Runner: Runner, Unit: unit, What: what, Elapsed: elapsed, Err: err, Interrupted: errors.Is(err, proc.ErrInterrupted)})
	}

	out, elapsed, err := run(ctx, repo.Root, "build", proj, "-t:Build", "-getProperty:TargetPath", "-getProperty:TargetFrameworks")
	if err != nil {
		invocation("build", elapsed, err)
		return nil, nil, invs, err
	}
	var props struct {
		Properties struct{ TargetPath, TargetFrameworks string }
	}
	if err := json.Unmarshal(out, &props); err != nil {
		err = fmt.Errorf("read build properties: %w", err)
		invocation("build", elapsed, err)
		return nil, nil, invs, err
	}
	invocation("build", elapsed, nil)
	dll := props.Properties.TargetPath
	if dll == "" {
		return nil, nil, invs, fmt.Errorf("multi-targeted test projects (%s) are not supported", props.Properties.TargetFrameworks)
	}
	s.built[unit] = true

	listing := filepath.Join(repo.Scratch, "dotnet", shortHash(unit)+".tests.txt")
	if err := os.MkdirAll(filepath.Dir(listing), 0o755); err != nil {
		return nil, nil, invs, err
	}
	_, elapsed, err = run(ctx, repo.Root, "vstest", dll, "--ListFullyQualifiedTests", "--ListTestsTargetPath:"+listing)
	invocation("list", elapsed, err)
	if err != nil {
		return nil, nil, invs, err
	}
	data, err := os.ReadFile(listing)
	if err != nil {
		return nil, nil, invs, fmt.Errorf("read test listing: %w", err)
	}
	rows := map[string][]string{}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		name := method(line)
		if name == "" {
			continue
		}
		if rows[name] == nil {
			names = append(names, name)
		}
		if !slices.Contains(rows[name], line) {
			rows[name] = append(rows[name], line)
		}
	}
	return names, rows, invs, nil
}

// method removes every argument list from a fully qualified name — a
// parameterized method's and a parameterized fixture's — leaving the
// method's name. Parentheses inside quoted arguments do not count.
func method(fqn string) string {
	var b strings.Builder
	depth := 0
	var quote byte
	for i := 0; i < len(fqn); i++ {
		c := fqn[i]
		if depth == 0 {
			if c == '(' {
				depth = 1
			} else {
				b.WriteByte(c)
			}
			continue
		}
		switch {
		case quote != 0 && c == '\\':
			i++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
		}
	}
	return b.String()
}

// Run runs each project's selected methods.
func (s *Source) Run(ctx context.Context, repo check.Repo, keys []check.Key, in check.Input) (check.Report, error) {
	byUnit := map[string][]check.Key{}
	var units []string
	for _, k := range keys {
		if byUnit[k.Unit] == nil {
			units = append(units, k.Unit)
		}
		byUnit[k.Unit] = append(byUnit[k.Unit], k)
	}
	sort.Strings(units)
	var rep check.Report
	for _, unit := range units {
		r := s.runProject(ctx, repo, unit, byUnit[unit])
		rep.Results = append(rep.Results, r.Results...)
		rep.Skipped = append(rep.Skipped, r.Skipped...)
		rep.Invocations = append(rep.Invocations, r.Invocations...)
	}
	return rep, nil
}

// filterEscaper escapes VSTest filter operators inside a value.
var filterEscaper = strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`, `&`, `\&`, `|`, `\|`, `=`, `\=`, `!`, `\!`, `~`, `\~`)

// msbuildEscaper escapes what MSBuild would otherwise read in the property
// that carries the filter: separators, quotes, and its own escapes.
var msbuildEscaper = strings.NewReplacer(`%`, `%25`, `;`, `%3B`, `,`, `%2C`, `"`, `%22`)

// maxArgFilter bounds the filter one dotnet test invocation carries on its
// command line: Windows limits a whole command line to 32,767 characters and
// Linux one argument to 131,072 bytes.
const maxArgFilter = 30000

// batch is one dotnet test invocation's share of a selection.
type batch struct {
	keys    []check.Key
	clauses []string // MSBuild-escaped; none means every method
}

// runProject runs the selected methods of one project. A selection of every
// discovered method needs no filter; a longer filter than maxArgFilter is
// split across invocations, each with its own results file, so the project's
// own settings apply to every run. A method whose rows span invocations gets
// one outcome folded from all of them, and none unless each reported it.
func (s *Source) runProject(ctx context.Context, repo check.Repo, unit string, keys []check.Key) check.Report {
	base := filepath.Join(repo.Scratch, "dotnet", shortHash(unit))
	_ = os.MkdirAll(filepath.Dir(base), 0o755)
	batches := []batch{{keys: keys}}
	if !s.selectsAll(unit, keys) {
		batches = s.batches(keys)
	}

	type ran struct {
		inv        check.Invocation
		methods    map[string]*methodResult
		runErr     error
		unresolved bool // the invocation reported no result for some of its keys
	}
	runs := make([]ran, len(batches))
	complete := map[check.Key]bool{}
	for _, k := range keys {
		complete[k] = true
	}
	merged := map[string]*methodResult{}
	for i, b := range batches {
		trx := base + ".trx"
		if len(batches) > 1 {
			trx = fmt.Sprintf("%s-%d.trx", base, i)
		}
		args := []string{filepath.Join(repo.Root, filepath.FromSlash(unit)), "--logger", "trx;LogFileName=" + trx}
		if len(b.clauses) > 0 {
			args = append(args, "--filter", strings.Join(b.clauses, "|"))
		}
		if s.built[unit] {
			args = append(args, "--no-build")
		}
		_, elapsed, runErr := run(ctx, repo.Root, "test", args...)
		r := ran{inv: check.Invocation{Runner: Runner, Unit: unit, What: "run", Elapsed: elapsed, Keys: b.keys, Interrupted: errors.Is(runErr, proc.ErrInterrupted)}, runErr: runErr}
		if r.inv.Interrupted {
			r.inv.Err = runErr
		} else if methods, err := readTRX(trx); err != nil {
			r.inv.Err = err
			if runErr != nil {
				r.inv.Err = fmt.Errorf("%v; %w", err, runErr)
			}
		} else {
			r.methods = methods
			for name, m := range methods {
				if prev := merged[name]; prev != nil {
					prev.add(m)
				} else {
					merged[name] = m
				}
			}
		}
		for _, k := range b.keys {
			if r.inv.Err != nil || r.methods[k.Name] == nil {
				complete[k] = false
				r.unresolved = r.unresolved || r.inv.Err == nil
			}
		}
		runs[i] = r
	}

	var rep check.Report
	reported := map[string]bool{}
	report := func(k check.Key, m *methodResult) {
		reported[k.Name] = true
		switch {
		case m.failed:
			rep.Results = append(rep.Results, check.Result{Key: k, Outcome: check.Fail, Elapsed: m.elapsed, Timed: true, Output: m.output.String(), Ended: m.ended})
		case m.passed:
			rep.Results = append(rep.Results, check.Result{Key: k, Outcome: check.Pass, Elapsed: m.elapsed, Timed: true, Ended: m.ended})
		default:
			rep.Skipped = append(rep.Skipped, k)
		}
	}
	for _, k := range keys {
		if complete[k] {
			report(k, merged[k.Name])
		}
	}
	// The filter may also have run methods it does not name; report the
	// discovered ones, which are the selection's co-runs, once each.
	for i := range runs {
		r := &runs[i]
		if r.inv.Err != nil {
			rep.Invocations = append(rep.Invocations, r.inv)
			continue
		}
		var unexpected []string
		for _, name := range slices.Sorted(maps.Keys(r.methods)) {
			k := check.Key{Runner: Runner, Unit: unit, Name: name}
			_, discovered := s.rows[k]
			switch {
			case discovered && !reported[name] && !slices.Contains(keys, k):
				report(k, merged[name])
				r.inv.Keys = append(r.inv.Keys, k)
			case !discovered && r.methods[name].failed:
				unexpected = append(unexpected, name)
			}
		}
		reportedFail := false
		for _, k := range r.inv.Keys {
			reportedFail = reportedFail || r.methods[k.Name] != nil && r.methods[k.Name].failed
		}
		switch {
		case r.unresolved:
			r.inv.Err = errors.New("dotnet test reported no result for some selected methods")
			if r.runErr != nil {
				r.inv.Err = fmt.Errorf("%w: %v", r.inv.Err, r.runErr)
			}
		case len(unexpected) > 0:
			r.inv.Err = fmt.Errorf("dotnet test ran and failed methods it did not discover: %s", strings.Join(unexpected, ", "))
		case r.runErr != nil && !reportedFail:
			r.inv.Err = fmt.Errorf("dotnet test failed outside its tests: %w", r.runErr)
		}
		rep.Invocations = append(rep.Invocations, r.inv)
	}
	return rep
}

// selectsAll reports whether keys hold every method discovered in unit.
func (s *Source) selectsAll(unit string, keys []check.Key) bool {
	n := 0
	for k := range s.rows {
		if k.Unit == unit {
			n++
		}
	}
	return n > 0 && n == len(keys)
}

// batches splits the filter of keys so that each batch fits maxArgFilter.
// Methods whose names differ only in case stay in one batch, since a filter
// naming one may run the others, unless together they exceed the bound; a
// method's rows are split only then too.
func (s *Source) batches(keys []check.Key) []batch {
	var groups [][]check.Key
	at := map[string]int{}
	for _, k := range keys {
		f := strings.ToLower(k.Name)
		if i, ok := at[f]; ok {
			groups[i] = append(groups[i], k)
			continue
		}
		at[f] = len(groups)
		groups = append(groups, []check.Key{k})
	}
	var out []batch
	var cur batch
	size := 0
	flush := func() {
		if len(cur.clauses) > 0 {
			out = append(out, cur)
		}
		cur, size = batch{}, 0
	}
	for _, g := range groups {
		clauses := make([][]string, len(g))
		total := 0
		for i, k := range g {
			clauses[i] = s.clauses(k)
			for _, c := range clauses[i] {
				total += len(c) + 1 // and its separator
			}
		}
		if size+total > maxArgFilter {
			flush()
		}
		for i, k := range g {
			for _, c := range clauses[i] {
				if size > 0 && size+len(c)+1 > maxArgFilter {
					flush()
				}
				if n := len(cur.keys); n == 0 || cur.keys[n-1] != k {
					cur.keys = append(cur.keys, k)
				}
				cur.clauses = append(cur.clauses, c)
				size += len(c) + 1
			}
		}
	}
	flush()
	return out
}

// clauses selects every listed row of k exactly, MSBuild-escaped: adapters
// differ in whether a row's name carries arguments, and contains-filters on
// arguments break some.
func (s *Source) clauses(k check.Key) []string {
	rows := s.rows[k]
	if len(rows) == 0 {
		rows = []string{k.Name}
	}
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = msbuildEscaper.Replace("FullyQualifiedName=" + filterEscaper.Replace(row))
	}
	return out
}

type methodResult struct {
	passed, failed bool
	elapsed        time.Duration
	ended          time.Time // the last row's end; zero when any row's is unknown
	unknownEnd     bool
	output         strings.Builder
}

// add folds another invocation's rows of the same method into m.
func (m *methodResult) add(o *methodResult) {
	m.passed, m.failed = m.passed || o.passed, m.failed || o.failed
	m.elapsed += o.elapsed
	switch {
	case m.unknownEnd || o.unknownEnd:
		m.ended, m.unknownEnd = time.Time{}, true
	case o.ended.After(m.ended):
		m.ended = o.ended
	}
	m.output.WriteString(o.output.String())
}

type trxRun struct {
	Results []struct {
		TestID   string `xml:"testId,attr"`
		TestName string `xml:"testName,attr"`
		Outcome  string `xml:"outcome,attr"`
		Duration string `xml:"duration,attr"`
		EndTime  string `xml:"endTime,attr"`
		Message  string `xml:"Output>ErrorInfo>Message"`
		Stack    string `xml:"Output>ErrorInfo>StackTrace"`
	} `xml:"Results>UnitTestResult"`
	Definitions []struct {
		ID     string `xml:"id,attr"`
		Method struct {
			ClassName string `xml:"className,attr"`
			Name      string `xml:"name,attr"`
		} `xml:"TestMethod"`
	} `xml:"TestDefinitions>UnitTest"`
}

// readTRX folds every result row into its method: any failed row fails the
// method; otherwise any passed row passes it; otherwise it was skipped.
func readTRX(file string) (map[string]*methodResult, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("dotnet test wrote no results: %w", err)
	}
	var run trxRun
	if err := xml.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(file), err)
	}
	names := map[string]string{}
	for _, d := range run.Definitions {
		names[d.ID] = method(d.Method.ClassName + "." + d.Method.Name)
	}
	methods := map[string]*methodResult{}
	for _, r := range run.Results {
		name, ok := names[r.TestID]
		if !ok {
			continue
		}
		m := methods[name]
		if m == nil {
			m = &methodResult{}
			methods[name] = m
		}
		m.elapsed += duration(r.Duration)
		if end, err := time.Parse(time.RFC3339Nano, r.EndTime); err == nil && !m.unknownEnd {
			if end.After(m.ended) {
				m.ended = end
			}
		} else {
			m.ended, m.unknownEnd = time.Time{}, true
		}
		switch r.Outcome {
		case "Passed":
			m.passed = true
		case "NotExecuted":
		default:
			m.failed = true
			fmt.Fprintf(&m.output, "%s: %s\n%s\n", r.TestName, strings.TrimSpace(r.Message), strings.TrimSpace(r.Stack))
		}
	}
	return methods, nil
}

// duration reads a TRX hh:mm:ss.fffffff value; malformed values count as 0.
func duration(v string) time.Duration {
	parts := strings.Split(v, ":")
	if len(parts) != 3 {
		return 0
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec*float64(time.Second))
}

// run executes one dotnet command with English, banner-free output and
// returns its stdout; a failure carries stderr and stdout.
func run(ctx context.Context, dir, verb string, args ...string) ([]byte, time.Duration, error) {
	cmd := exec.CommandContext(ctx, "dotnet", append([]string{verb}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "DOTNET_CLI_UI_LANGUAGE=en", "DOTNET_NOLOGO=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	elapsed, err := proc.Run(ctx, cmd)
	if err != nil {
		return stdout.Bytes(), elapsed, fmt.Errorf("dotnet %s: %w\n%s", verb, err, strings.TrimSpace(stderr.String()+"\n"+stdout.String()))
	}
	return stdout.Bytes(), elapsed, nil
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}
