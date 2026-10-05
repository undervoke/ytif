// Package gotest registers what go test runs — tests, fuzz targets, and
// examples with output — and runs the selected ones per package.
//
// Discovery mirrors go test's own source rules without compiling: files
// that the go command builds in the module, top-level TestXxx(*T) and FuzzXxx(*F),
// TestMain(*M) excluded, and examples whose doc carries an output comment.
package gotest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/doc"
	"go/parser"
	"go/token"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gomod"
	"github.com/undervoke/ytif/internal/proc"
)

const Runner = "go-test"

// parallel bounds concurrent go test processes; each also parallelizes its
// own build.
const parallel = 4

// Source is the go test runner. Discover records each package's module root.
type Source struct {
	modules map[string]string // package dir → module root
}

func (*Source) Runners() []string { return []string{Runner} }

func (s *Source) Discover(ctx context.Context, repo check.Repo) ([]check.Key, []check.Invocation, error) {
	mods := gomod.Modules(repo.Tracked)
	byDir := map[string][]string{}
	for _, f := range repo.Tracked {
		if strings.HasSuffix(f, "_test.go") && !gomod.Excluded(f) {
			byDir[path.Dir(f)] = append(byDir[path.Dir(f)], path.Base(f))
		}
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	s.modules = map[string]string{}
	var keys []check.Key
	var errs []error
	contexts := map[string]*build.Context{} // by module; nil when go env failed
	for _, dir := range dirs {
		mod, ok := gomod.Owner(mods, dir)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: test files outside any Go module", dir))
			continue
		}
		bctx, seen := contexts[mod]
		if !seen {
			c, err := gomod.BuildContext(ctx, filepath.Join(repo.Root, filepath.FromSlash(mod)))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", mod, err))
			} else {
				bctx = &c
			}
			contexts[mod] = bctx
		}
		if bctx == nil {
			continue
		}
		names, err := testNames(*bctx, repo.Root, dir, byDir[dir])
		if err != nil {
			errs = append(errs, err)
		}
		if len(names) > 0 {
			s.modules[dir] = mod
		}
		for _, n := range names {
			keys = append(keys, check.Key{Runner: Runner, Unit: dir, Name: n})
		}
	}
	return keys, nil, errors.Join(errs...)
}

func testNames(ctx build.Context, root, dir string, files []string) ([]string, error) {
	abs := filepath.Join(root, filepath.FromSlash(dir))
	fset := token.NewFileSet()
	var names []string
	var errs []error
	for _, name := range files {
		ok, err := ctx.MatchFile(abs, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path.Join(dir, name), err))
			continue
		}
		if !ok {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(abs, name), nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil {
				continue
			}
			n := fd.Name.Name
			arg := ""
			switch {
			case n == "TestMain" && !isTestFunc(fd, "T"):
				continue // go test's TestMain(m *testing.M)
			case isTest(n, "Test"):
				arg = "T"
			case isTest(n, "Fuzz"):
				arg = "F"
			default:
				continue
			}
			if !isTestFunc(fd, arg) || fd.Type.TypeParams.NumFields() > 0 {
				errs = append(errs, fmt.Errorf("%s:%d: wrong signature for %s, must be: func %s(%s *testing.%s)",
					path.Join(dir, name), fset.Position(fd.Pos()).Line, n, n, strings.ToLower(arg), arg))
				continue
			}
			names = append(names, n)
		}
		for _, ex := range doc.Examples(f) {
			if ex.Output != "" || ex.EmptyOutput {
				names = append(names, "Example"+ex.Name)
			}
		}
	}
	return names, errors.Join(errs...)
}

// isTest mirrors go test: prefix, then nothing or a non-lowercase rune.
func isTest(name, prefix string) bool {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return !unicode.IsLower(r)
}

// isTestFunc mirrors go test: one parameter of type *X or *pkg.X, and no
// results. Like go test, it cannot tell which package X comes from.
func isTestFunc(fd *ast.FuncDecl, arg string) bool {
	t := fd.Type
	if t.Results.NumFields() > 0 || t.Params == nil || len(t.Params.List) != 1 || len(t.Params.List[0].Names) > 1 {
		return false
	}
	ptr, ok := t.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	switch x := ptr.X.(type) {
	case *ast.Ident:
		return x.Name == arg
	case *ast.SelectorExpr:
		return x.Sel.Name == arg
	}
	return false
}

// Run runs each package's selected tests in its own go test process.
func (s *Source) Run(ctx context.Context, repo check.Repo, keys []check.Key, in check.Input) (check.Report, error) {
	if s.modules == nil {
		if _, _, err := s.Discover(ctx, repo); err != nil && len(s.modules) == 0 {
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
		mod, ok := s.modules[unit]
		if !ok {
			continue // not discovered; the gate reports these keys as without outcome
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			reports[i] = runPackage(ctx, repo, mod, unit, byUnit[unit])
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

// event is one go test -json line (test2json, plus build events since Go 1.24).
type event struct {
	Time    time.Time
	Action  string
	Test    string
	Output  string
	Elapsed float64
}

func runPackage(ctx context.Context, repo check.Repo, mod, dir string, keys []check.Key) check.Report {
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = regexp.QuoteMeta(k.Name)
	}
	pkg := "./" + gomod.Rel(mod, dir)
	cmd := exec.CommandContext(ctx, "go", "test", "-json", "-run", "^("+strings.Join(names, "|")+")$", pkg)
	cmd.Dir = filepath.Join(repo.Root, filepath.FromSlash(mod))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	waitErr := proc.Run(cmd)
	elapsed := time.Since(start)

	type outcome struct {
		action  string
		elapsed float64
		ended   time.Time
		output  strings.Builder
	}
	tests := map[string]*outcome{}
	var pkgOut strings.Builder
	pkgFailed, cached := false, false
	sc := bufio.NewScanner(&stdout)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			pkgOut.WriteString(sc.Text() + "\n")
			continue
		}
		if e.Test == "" {
			pkgOut.WriteString(e.Output)
			pkgFailed = pkgFailed || e.Action == "fail"
			cached = cached || (strings.HasPrefix(e.Output, "ok ") && strings.Contains(e.Output, "\t(cached)"))
			continue
		}
		top, _, sub := strings.Cut(e.Test, "/")
		o := tests[top]
		if o == nil {
			o = &outcome{}
			tests[top] = o
		}
		o.output.WriteString(e.Output)
		if !sub && (e.Action == "pass" || e.Action == "fail" || e.Action == "skip") {
			o.action, o.elapsed, o.ended = e.Action, e.Elapsed, e.Time
		}
	}

	var rep check.Report
	unresolved := false
	for _, k := range keys {
		o := tests[k.Name]
		switch {
		case o == nil || o.action == "":
			unresolved = true
		case o.action == "skip":
			rep.Skipped = append(rep.Skipped, k)
		default:
			// A cached result replays an earlier run's duration; this run
			// did not measure it, though the invocation's wall time stands.
			res := check.Result{Key: k, Outcome: check.Pass, Timed: !cached, Elapsed: time.Duration(o.elapsed * float64(time.Second)), Ended: o.ended}
			if o.action == "fail" {
				res.Outcome, res.Output = check.Fail, o.output.String()
			}
			rep.Results = append(rep.Results, res)
		}
	}
	testFailed := false
	for _, o := range tests {
		testFailed = testFailed || o.action == "fail"
	}
	inv := check.Invocation{Runner: Runner, Unit: dir, What: "run", Elapsed: elapsed, Keys: keys, Interrupted: errors.Is(waitErr, proc.ErrInterrupted)}
	output := strings.TrimSpace(pkgOut.String() + stderr.String())
	switch {
	case inv.Interrupted:
		inv.Err = fmt.Errorf("go test %s: %v", pkg, waitErr)
	case unresolved:
		cause := "exited without reporting every selected test"
		if waitErr != nil {
			cause = waitErr.Error()
		}
		// A test that ended the process wrote its diagnostics before
		// reporting an outcome; keep them.
		var unfinished strings.Builder
		for _, k := range keys {
			if o := tests[k.Name]; o != nil && o.action == "" {
				fmt.Fprintf(&unfinished, "%s did not finish:\n%s\n", k.Name, strings.TrimRight(o.output.String(), "\n"))
			}
		}
		inv.Err = fmt.Errorf("go test %s: %s\n%s%s", pkg, cause, unfinished.String(), output)
	case (pkgFailed || waitErr != nil) && !testFailed:
		// TestMain or package setup failed after every test passed.
		inv.Err = fmt.Errorf("go test %s failed outside its tests\n%s", pkg, output)
	}
	rep.Invocations = append(rep.Invocations, inv)
	return rep
}
