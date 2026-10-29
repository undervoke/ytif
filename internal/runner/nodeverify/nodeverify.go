// Package nodeverify discovers and runs Node checks: exported verifyXxx
// functions in tracked *.verify.mjs files.
//
// A check has the shape
//
//	export async function verifyXxx({ files, signal, gate, profile, selection, report }, out)
//
// where files are the gate's files, signal aborts when the gate is
// interrupted, gate and profile name the run, selection carries the plan's
// requested and resolved scope with its decision, comparison and changed
// paths, report({scope, targets}) optionally records the scope actually
// run, and out.write(text) writes findings as path:line: message.
// A check fails by throwing. Exports are read statically; an export that
// could carry a check name ytif cannot see fails discovery. Each homogeneous
// process group runs one generated dispatcher that imports the modules and
// calls its checks in order.
package nodeverify

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/dispatch"
	"github.com/undervoke/ytif/internal/proc"
	"github.com/undervoke/ytif/internal/runner/nodetool"
)

const (
	Runner = "node-verify"
	suffix = ".verify.mjs"
)

//go:embed dispatch.mjs
var dispatcher []byte

// Source is the Node verify host.
type Source struct{}

func (*Source) Runners() []string { return []string{Runner} }

func (*Source) Discover(ctx context.Context, repo check.Repo) ([]check.Key, []check.Invocation, error) {
	var keys []check.Key
	var errs []error
	for _, unit := range nodetool.Configs(repo.Tracked, func(base string) bool { return strings.HasSuffix(base, suffix) }) {
		names, err := exports(repo.Root, unit)
		if err != nil {
			errs = append(errs, err)
		}
		for _, n := range names {
			keys = append(keys, check.Key{Runner: Runner, Unit: unit, Name: n})
		}
	}
	return keys, nil, errors.Join(errs...)
}

// exports returns the file's checks in source order.
func exports(root, unit string) ([]string, error) {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(unit)))
	if err != nil {
		return nil, err
	}
	// Parsed as written: a transform may move export declarations into an
	// export list.
	ast, err := js.Parse(parse.NewInputBytes(src), js.Options{})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", unit, err)
	}
	var names []string
	var errs []error
	unnamed := func(what string) {
		errs = append(errs, fmt.Errorf("%s: %s can export a check ytif cannot name; export each check as a function declaration", unit, what))
	}
	// name decodes an export name as written, which may be quoted or
	// escaped; one ytif cannot decode fails discovery.
	name := func(raw []byte) string {
		n, ok := exportName(raw)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: export name %s uses an escape ytif cannot read", unit, raw))
		}
		return n
	}
	for _, st := range ast.List {
		e, ok := st.(*js.ExportStmt)
		if !ok || e.Default {
			continue
		}
		switch d := e.Decl.(type) {
		case *js.FuncDecl:
			if d.Name == nil {
				continue
			}
			n := name(d.Name.Data)
			if !isCheckName(n) {
				continue
			}
			if d.Generator {
				errs = append(errs, fmt.Errorf("%s: %s is a generator; a check is a plain or async function", unit, n))
				continue
			}
			names = append(names, n)
		case *js.VarDecl:
			for _, b := range d.List {
				v, ok := b.Binding.(*js.Var)
				switch {
				case !ok:
					unnamed("a destructuring export")
				case isCheckName(name(v.Data)):
					unnamed("export of variable " + name(v.Data))
				}
			}
		case *js.ClassDecl:
			if d.Name != nil && isCheckName(name(d.Name.Data)) {
				unnamed("export of class " + name(d.Name.Data))
			}
		case nil:
			for _, a := range e.List {
				if string(a.Binding) == "*" && a.Name == nil {
					unnamed("export *")
				} else if n := name(a.Binding); isCheckName(n) {
					unnamed("export of " + n + " by name")
				}
			}
		}
	}
	return names, errors.Join(errs...)
}

// exportName decodes an identifier or string-literal export name. Only
// \uXXXX and \u{X} escapes are decoded; ok is false for any other escape.
func exportName(raw []byte) (string, bool) {
	s := string(raw)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		s = s[1 : len(s)-1]
	}
	if !strings.Contains(s, `\`) {
		return s, true
	}
	var b strings.Builder
	for len(s) > 0 {
		i := strings.IndexByte(s, '\\')
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		s = s[i:]
		if !strings.HasPrefix(s, `\u`) {
			return string(raw), false
		}
		var hex string
		if strings.HasPrefix(s, `\u{`) {
			end := strings.IndexByte(s, '}')
			if end < 0 {
				return string(raw), false
			}
			hex, s = s[3:end], s[end+1:]
		} else if len(s) >= 6 {
			hex, s = s[2:6], s[6:]
		} else {
			return string(raw), false
		}
		r, err := strconv.ParseUint(hex, 16, 32)
		if err != nil || r > unicode.MaxRune {
			return string(raw), false
		}
		b.WriteRune(rune(r))
	}
	return b.String(), true
}

// isCheckName mirrors go-verify's VerifyXxx rule: verify, optionally
// followed by a name that does not start with a lowercase letter.
func isCheckName(name string) bool {
	rest, ok := strings.CutPrefix(name, "verify")
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return !unicode.IsLower(r)
}

// nodeCheck is one check the dispatcher calls, with the plan that chose
// its execution. Files stay the gate's files; Changed and Comparison are
// offered only when narrowed: a resolved full check carries null
// comparison and changed.
type nodeCheck struct {
	Module    string
	Name      string
	Requested check.TargetScope
	Resolved  check.TargetScope
	Run       bool
	Reason    string
	Changed   []string
}

// nodeRequest is what one dispatcher process receives. Gate and Profile
// always travel; Comparison is present only for narrowed groups, null for
// resolved full. EnvKeys carries check.RestoreKeys for per-call restore.
type nodeRequest struct {
	Files      []string
	Gate       string
	Profile    string
	Comparison *check.Comparison
	EnvKeys    []string
	Checks     []nodeCheck
}

// planView returns the execution view for one key. A missing plan runs as
// full; callers filter Run=false keys before grouping.
func planView(in check.Input, k check.Key) (requested, resolved check.TargetScope, run bool, reason string, changed []string) {
	requested, resolved, run = check.ScopeFull, check.ScopeFull, true
	if p := in.Plans[k]; p != nil {
		requested, resolved, run, reason, changed = p.Requested, p.Resolved, p.Run, p.Reason, p.Changed
	}
	return requested, resolved, run, reason, changed
}

// Run executes keys in key order, split into maximal contiguous groups
// sharing one projected process environment. Each group runs one
// dispatcher sequentially, so A(full),B(affected),C(full) stays three
// processes in order. Cancellation stops later groups.
func (*Source) Run(ctx context.Context, repo check.Repo, keys []check.Key, in check.Input) (check.Report, error) {
	keys = append([]check.Key(nil), keys...)
	sort.Slice(keys, func(i, j int) bool { return keys[i].Less(keys[j]) })
	var rep check.Report
	var runnable []check.Key
	for _, k := range keys {
		if p := in.Plans[k]; p != nil && !p.Run {
			rep.Skipped = append(rep.Skipped, k)
			continue
		}
		runnable = append(runnable, k)
	}
	if len(runnable) == 0 {
		return rep, nil
	}
	// The input fixes gate, profile, comparison and target, so one projected
	// process context is exactly one homogeneous scope: a missing plan runs
	// as full but groups apart from actual full.
	var groups [][]check.Key
	var previousScope check.TargetScope
	var previousKnown bool
	for i, k := range runnable {
		scope, known := in.HomogeneousScope([]check.Key{k})
		if i > 0 && known == previousKnown && scope == previousScope {
			groups[len(groups)-1] = append(groups[len(groups)-1], k)
		} else {
			groups = append(groups, []check.Key{k})
		}
		previousScope, previousKnown = scope, known
	}
	for gi, gkeys := range groups {
		if gi > 0 && ctx.Err() != nil {
			var pending []check.Key
			for _, rest := range groups[gi:] {
				pending = append(pending, rest...)
			}
			rep.Invocations = append(rep.Invocations, check.Invocation{
				Runner: Runner, What: "run", Err: proc.ErrInterrupted, Keys: pending, Interrupted: true,
			})
			break
		}
		req := nodeRequest{Files: in.Files, Gate: in.Gate, Profile: in.Profile, EnvKeys: check.RestoreKeys}
		names := make([]string, 0, len(gkeys))
		allFull := true
		for _, k := range gkeys {
			requested, resolved, run, reason, changed := planView(in, k)
			if resolved != check.ScopeFull {
				allFull = false
			} else {
				changed = nil
			}
			names = append(names, k.Unit+":"+k.Name)
			req.Checks = append(req.Checks, nodeCheck{
				Module: filepath.Join(repo.Root, filepath.FromSlash(k.Unit)), Name: k.Name,
				Requested: requested, Resolved: resolved, Run: run, Reason: reason, Changed: changed,
			})
		}
		if !allFull {
			req.Comparison = in.Comparison
		}
		work := filepath.Join(repo.Scratch, "nodeverify", fmt.Sprintf("group-%d", gi))
		results := filepath.Join(work, "results.jsonl")
		script := filepath.Join(work, "dispatch.mjs")
		body, err := json.Marshal(req)
		if err == nil {
			err = os.MkdirAll(work, 0o755)
		}
		if err == nil {
			err = os.WriteFile(script, dispatcher, 0o644)
		}
		if err != nil {
			rep.Invocations = append(rep.Invocations, check.Invocation{
				Runner: Runner, What: "run", Elapsed: 0, Err: err, Keys: append([]check.Key(nil), gkeys...),
				Interrupted: errors.Is(err, proc.ErrInterrupted),
			})
			continue
		}
		cmd := exec.CommandContext(ctx, "node", script, results)
		cmd.Dir = repo.Root
		cmd.Stdin = bytes.NewReader(body)
		cmd.Stdout, cmd.Stderr = repo.Log, repo.Log
		cmd.Env = in.ProcessEnvironment(cmd.Environ(), gkeys)
		elapsed, runErr := proc.Run(ctx, cmd)
		out, err := dispatch.Results(results, gkeys, names, runErr)
		rep.Results = append(rep.Results, out...)
		rep.Invocations = append(rep.Invocations, check.Invocation{
			Runner: Runner, What: "run", Elapsed: elapsed, Err: err, Keys: append([]check.Key(nil), gkeys...),
			Interrupted: errors.Is(err, proc.ErrInterrupted),
		})
	}
	return rep, nil
}
