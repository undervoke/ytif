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

// Run runs each project's selected methods in one dotnet test process.
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

func (s *Source) runProject(ctx context.Context, repo check.Repo, unit string, keys []check.Key) check.Report {
	// Select every listed row exactly: adapters differ in whether a row's
	// name carries arguments, and contains-filters on arguments break some.
	var clauses []string
	for _, k := range keys {
		rows := s.rows[k]
		if len(rows) == 0 {
			rows = []string{k.Name}
		}
		for _, row := range rows {
			clauses = append(clauses, "FullyQualifiedName="+filterEscaper.Replace(row))
		}
	}
	trx := filepath.Join(repo.Scratch, "dotnet", shortHash(unit)+".trx")
	_ = os.MkdirAll(filepath.Dir(trx), 0o755)
	filter := msbuildEscaper.Replace(strings.Join(clauses, "|"))
	args := []string{filepath.Join(repo.Root, filepath.FromSlash(unit)), "--filter", filter, "--logger", "trx;LogFileName=" + trx}
	if s.built[unit] {
		args = append(args, "--no-build")
	}
	_, elapsed, runErr := run(ctx, repo.Root, "test", args...)

	inv := check.Invocation{Runner: Runner, Unit: unit, What: "run", Elapsed: elapsed, Keys: keys, Interrupted: errors.Is(runErr, proc.ErrInterrupted)}
	if inv.Interrupted {
		inv.Err = runErr
		return check.Report{Invocations: []check.Invocation{inv}}
	}
	methods, err := readTRX(trx)
	if err != nil {
		inv.Err = err
		if runErr != nil {
			inv.Err = fmt.Errorf("%v; %w", err, runErr)
		}
		return check.Report{Invocations: []check.Invocation{inv}}
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
	unresolved := false
	for _, k := range keys {
		if m := methods[k.Name]; m != nil {
			report(k, m)
		} else {
			unresolved = true
		}
	}
	// The filter may also have run methods it does not name; report the
	// discovered ones, which are the selection's co-runs.
	var unexpected []string
	for _, name := range slices.Sorted(maps.Keys(methods)) {
		if reported[name] {
			continue
		}
		k := check.Key{Runner: Runner, Unit: unit, Name: name}
		if _, discovered := s.rows[k]; discovered {
			report(k, methods[name])
			inv.Keys = append(inv.Keys, k)
		} else if methods[name].failed {
			unexpected = append(unexpected, name)
		}
	}
	reportedFail := false
	for _, r := range rep.Results {
		reportedFail = reportedFail || r.Outcome == check.Fail
	}
	switch {
	case unresolved:
		inv.Err = errors.New("dotnet test reported no result for some selected methods")
		if runErr != nil {
			inv.Err = fmt.Errorf("%w: %v", inv.Err, runErr)
		}
	case len(unexpected) > 0:
		inv.Err = fmt.Errorf("dotnet test ran and failed methods it did not discover: %s", strings.Join(unexpected, ", "))
	case runErr != nil && !reportedFail:
		inv.Err = fmt.Errorf("dotnet test failed outside its tests: %w", runErr)
	}
	rep.Invocations = append(rep.Invocations, inv)
	return rep
}

type methodResult struct {
	passed, failed bool
	elapsed        time.Duration
	ended          time.Time // the last row's end; zero when any row's is unknown
	unknownEnd     bool
	output         strings.Builder
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
	start := time.Now()
	err := proc.Run(cmd)
	elapsed := time.Since(start)
	if err != nil {
		return stdout.Bytes(), elapsed, fmt.Errorf("dotnet %s: %w\n%s", verb, err, strings.TrimSpace(stderr.String()+"\n"+stdout.String()))
	}
	return stdout.Bytes(), elapsed, nil
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}
