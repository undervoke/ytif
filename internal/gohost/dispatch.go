package gohost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/proc"
)

// group is what one dispatcher runs: the selected checks of one import
// domain in one module, with their providers, providers first.
type group struct {
	module string   // repo-relative module root
	domain string   // repo-relative root allowed to import every package
	tags   []string // the go command's own build tags in the module
	funcs  []*fn
}

// groups closes keys over their providers and splits them by domain.
func groups(pkgs []*pkg, keys []check.Key) []*group {
	byKey := map[check.Key]*fn{}
	for _, p := range pkgs {
		for _, f := range p.Funcs {
			byKey[f.Key] = f
		}
	}
	need := map[*fn]bool{}
	var add func(*fn)
	add = func(f *fn) {
		if need[f] {
			return
		}
		need[f] = true
		for _, d := range f.Deps {
			add(d)
		}
	}
	for _, k := range keys {
		if f := byKey[k]; f != nil {
			add(f)
		}
	}
	var out []*group
	index := map[[2]string]*group{}
	for _, p := range pkgs {
		for _, f := range p.Funcs {
			if !need[f] {
				continue
			}
			id := [2]string{p.Module, p.Domain}
			g := index[id]
			if g == nil {
				g = &group{module: p.Module, domain: p.Domain, tags: p.BuildTags}
				index[id] = g
				out = append(out, g)
			}
			g.funcs = append(g.funcs, f)
		}
	}
	return out
}

func (g *group) keys() []check.Key {
	keys := make([]check.Key, len(g.funcs))
	for i, f := range g.funcs {
		keys[i] = f.Key
	}
	return keys
}

// event is one line of the dispatcher's results file. Fields are untagged;
// JSON decoding matches them case-insensitively.
type event struct {
	I         int
	Start     bool
	Outcome   string
	ElapsedNS int64
	EndNS     int64 // Unix time of the outcome
	Output    string
}

// run builds the group's dispatcher through an overlay, runs it from the
// repository root, and reads its results file.
func (g *group) run(ctx context.Context, repo check.Repo, in check.Input) check.Report {
	keys := g.keys()
	var rep check.Report
	invocation := func(what string, elapsed time.Duration, err error) {
		rep.Invocations = append(rep.Invocations, check.Invocation{
			Runner: Runner, Unit: g.domain, What: what, Elapsed: elapsed, Err: err, Keys: keys,
			Interrupted: errors.Is(err, proc.ErrInterrupted),
		})
	}

	name := "zz_ytif_dispatch_" + shortHash(g.module+"\x00"+g.domain)
	work := filepath.Join(repo.Scratch, "gohost", name)
	bin, elapsed, err := g.build(ctx, repo, name, work)
	invocation("build", elapsed, err)
	if err != nil {
		return rep
	}

	results := filepath.Join(work, "results.jsonl")
	req, err := json.Marshal(struct{ Files []string }{in.Files})
	if err != nil {
		invocation("run", 0, err)
		return rep
	}
	cmd := exec.CommandContext(ctx, bin, results)
	cmd.Dir = repo.Root
	cmd.Stdin = bytes.NewReader(req)
	cmd.Stdout, cmd.Stderr = repo.Log, repo.Log
	start := time.Now()
	runErr := proc.Run(cmd)
	elapsed = time.Since(start)

	events, running, readErr := readEvents(results, len(g.funcs))
	for _, e := range events {
		r := check.Result{Key: g.funcs[e.I].Key, Outcome: check.Outcome(e.Outcome), Output: e.Output}
		if e.EndNS != 0 {
			r.Ended = time.Unix(0, e.EndNS)
		}
		if r.Outcome != check.Blocked {
			r.Elapsed, r.Timed = time.Duration(e.ElapsedNS), true
		}
		rep.Results = append(rep.Results, r)
	}
	// A check may end the process itself, even with status 0; the start
	// event without a result names it.
	exit := "exited"
	if runErr != nil {
		exit = runErr.Error()
	}
	switch {
	case errors.Is(runErr, proc.ErrInterrupted):
		err = runErr
	case readErr != nil:
		err = readErr
	case running >= 0:
		err = fmt.Errorf("dispatcher %s while running %s", exit, g.funcs[running].Name)
	case len(events) < len(g.funcs):
		err = fmt.Errorf("dispatcher %s after %d of %d checks", exit, len(events), len(g.funcs))
	case runErr != nil:
		err = fmt.Errorf("dispatcher %s", exit)
	}
	invocation("run", elapsed, err)
	return rep
}

// build writes the dispatcher source and overlay under work and compiles the
// dispatcher from the module root.
func (g *group) build(ctx context.Context, repo check.Repo, name, work string) (string, time.Duration, error) {
	src, err := generate(g)
	if err != nil {
		return "", 0, fmt.Errorf("generate dispatcher: %w", err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", 0, err
	}
	gen := filepath.Join(work, "main.go")
	if err := os.WriteFile(gen, src, 0o644); err != nil {
		return "", 0, err
	}
	virtual := filepath.Join(repo.Root, filepath.FromSlash(g.domain), name)
	overlay, err := json.Marshal(map[string]map[string]string{"Replace": {filepath.Join(virtual, "main.go"): gen}})
	if err != nil {
		return "", 0, err
	}
	overlayFile := filepath.Join(work, "overlay.json")
	if err := os.WriteFile(overlayFile, overlay, 0o644); err != nil {
		return "", 0, err
	}

	modRoot := filepath.Join(repo.Root, filepath.FromSlash(g.module))
	pkgDir, err := filepath.Rel(modRoot, virtual)
	if err != nil {
		return "", 0, err
	}
	mod := "readonly"
	if _, err := os.Stat(filepath.Join(modRoot, "vendor", "modules.txt")); err == nil {
		mod = "vendor"
	}
	bin := filepath.Join(work, "dispatch")
	// A command-line -tags replaces GOFLAGS tags, so carry them over.
	tags := strings.Join(append(append([]string(nil), g.tags...), Tag), ",")
	cmd := exec.CommandContext(ctx, "go", "build", "-tags", tags, "-buildvcs=false", "-mod="+mod,
		"-overlay", overlayFile, "-o", bin, "./"+filepath.ToSlash(pkgDir))
	cmd.Dir = modRoot
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	err = proc.Run(cmd)
	elapsed := time.Since(start)
	if err != nil {
		return "", elapsed, fmt.Errorf("go build: %w\n%s", err, strings.TrimSpace(out.String()))
	}
	return bin, elapsed, nil
}

// readEvents returns the finished events and the index of a check that
// started without finishing, or -1.
func readEvents(path string, n int) ([]event, int, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, -1, nil
	}
	if err != nil {
		return nil, -1, err
	}
	var done []event
	running := -1
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e event
		if err := json.Unmarshal(line, &e); err != nil || e.I < 0 || e.I >= n {
			return done, running, fmt.Errorf("dispatcher wrote an unreadable result line: %.200q", line)
		}
		if e.Start {
			running = e.I
			continue
		}
		switch check.Outcome(e.Outcome) {
		case check.Pass, check.Fail, check.Blocked:
		default:
			return done, running, fmt.Errorf("dispatcher reported outcome %q", e.Outcome)
		}
		if e.I == running {
			running = -1
		}
		done = append(done, e)
	}
	return done, running, nil
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// generate writes the dispatcher. Generic call helpers infer every parameter
// and provided type from the check itself, so the generated code never names
// a project type.
func generate(g *group) ([]byte, error) {
	var b strings.Builder
	b.WriteString(dispatchHeader)
	alias := map[string]string{}
	for _, f := range g.funcs {
		if _, ok := alias[f.Pkg.ImportPath]; !ok {
			alias[f.Pkg.ImportPath] = "p" + strconv.Itoa(len(alias))
			fmt.Fprintf(&b, "\t%s %q\n", alias[f.Pkg.ImportPath], f.Pkg.ImportPath)
		}
	}
	b.WriteString(")\n\nvar entries = []entry{\n")

	index := map[*fn]int{}
	helpers := map[string]bool{}
	for i, f := range g.funcs {
		index[f] = i
		helper := fmt.Sprintf("callE%d", len(f.Args))
		if f.Provides != "" {
			helper = fmt.Sprintf("callV%d", len(f.Args))
		}
		helpers[helper] = true
		args := make([]string, len(f.Args))
		for j, a := range f.Args {
			if a.Files {
				args[j] = "-1"
			} else {
				args[j] = strconv.Itoa(index[a.Dep]) // providers precede dependents
			}
		}
		fmt.Fprintf(&b, "\t{name: %q, call: %s(%s.%s), args: []int{%s}},\n",
			f.Name, helper, alias[f.Pkg.ImportPath], f.Name, strings.Join(args, ", "))
	}
	b.WriteString("}\n")

	names := make([]string, 0, len(helpers))
	for h := range helpers {
		names = append(names, h)
	}
	sort.Strings(names)
	for _, h := range names {
		n, _ := strconv.Atoi(h[len("callE"):])
		b.WriteString(helperSource(h[len("call")] == 'V', n))
	}
	b.WriteString(dispatchRuntime)
	return format.Source([]byte(b.String()))
}

// helperSource returns callE<n> or callV<n>, which adapt a check with n
// middle parameters to the dispatcher's caller type.
func helperSource(provides bool, n int) string {
	tparams := make([]string, 0, n+1)
	params := []string{"context.Context"}
	args := []string{"ctx"}
	for i := 1; i <= n; i++ {
		t := "A" + strconv.Itoa(i)
		tparams = append(tparams, t)
		params = append(params, t)
		args = append(args, fmt.Sprintf("as[%s](a[%d])", t, i-1))
	}
	params = append(params, "io.Writer")
	args = append(args, "w")
	kind, result, body := "E", "error", "return nil, f("+strings.Join(args, ", ")+")"
	if provides {
		tparams = append(tparams, "T")
		kind, result = "V", "(T, error)"
		body = "v, err := f(" + strings.Join(args, ", ") + ")\n\t\treturn v, err"
	}
	tp := ""
	if len(tparams) > 0 {
		tp = "[" + strings.Join(tparams, ", ") + " any]"
	}
	return fmt.Sprintf(`
func call%s%d%s(f func(%s) %s) caller {
	return func(ctx context.Context, a []any, w io.Writer) (any, error) {
		%s
	}
}
`, kind, n, tp, strings.Join(params, ", "), result, body)
}

const dispatchHeader = `// Code generated by ytif. DO NOT EDIT.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"time"

`

// dispatchRuntime runs the entries in order. It needs only Go 1.18.
const dispatchRuntime = `
type caller func(context.Context, []any, io.Writer) (any, error)

type entry struct {
	name string
	call caller
	args []int // -1 passes the files in scope; others index the providing entry
}

type event struct {
	I         int
	Start     bool
	Outcome   string
	ElapsedNS int64
	EndNS     int64
	Output    string
}

func as[T any](v any) T {
	if v == nil {
		var zero T
		return zero
	}
	return v.(T)
}

// invoke runs one check. A check that does not return normally panicked,
// even when the panic value is nil.
func invoke(ctx context.Context, call caller, args []any, w io.Writer) (v any, err error) {
	returned := false
	defer func() {
		if !returned {
			err = fmt.Errorf("panic: %v\n%s", recover(), debug.Stack())
		}
	}()
	v, err = call(ctx, args, w)
	returned = true
	return v, err
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "ytif dispatcher: want the results file as the only argument")
		os.Exit(2)
	}
	var req struct{ Files []string }
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		fmt.Fprintln(os.Stderr, "ytif dispatcher: read request:", err)
		os.Exit(2)
	}
	out, err := os.OpenFile(os.Args[1], os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ytif dispatcher:", err)
		os.Exit(2)
	}
	emit := func(e event) {
		line, err := json.Marshal(e)
		if err == nil {
			_, err = out.Write(append(line, '\n'))
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "ytif dispatcher: write results:", err)
			os.Exit(2)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	values := make([]any, len(entries))
	passed := make([]bool, len(entries))
	for i, e := range entries {
		args := make([]any, len(e.args))
		blocker := ""
		for j, a := range e.args {
			switch {
			case a < 0:
				args[j] = append([]string{}, req.Files...)
			case passed[a]:
				args[j] = values[a]
			case blocker == "":
				blocker = entries[a].name
			}
		}
		if blocker != "" {
			emit(event{I: i, Outcome: "blocked", EndNS: time.Now().UnixNano(), Output: blocker + " did not pass"})
			continue
		}
		emit(event{I: i, Start: true})
		var w bytes.Buffer
		start := time.Now()
		v, err := invoke(ctx, e.call, args, &w)
		elapsed := time.Since(start)
		outcome := "pass"
		if err != nil {
			outcome = "fail"
			if w.Len() > 0 && !bytes.HasSuffix(w.Bytes(), []byte("\n")) {
				w.WriteByte('\n')
			}
			w.WriteString(err.Error())
		} else {
			values[i], passed[i] = v, true
		}
		emit(event{I: i, Outcome: outcome, ElapsedNS: int64(elapsed), EndNS: time.Now().UnixNano(), Output: w.String()})
	}
	if err := out.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "ytif dispatcher: write results:", err)
		os.Exit(2)
	}
}
`
