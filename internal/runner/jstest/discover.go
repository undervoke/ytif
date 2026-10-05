// Package jstest registers bun:test and node:test tests from static test
// files and runs the selected ones per file.
//
// A file's runner is the test module it imports. Tests must be registered
// statically: describe/suite and test/it calls at the top level or directly
// inside a suite callback, each named by a non-blank string literal without
// control characters. Anything the static tree cannot name — computed names,
// .each tables, registration inside loops, helpers, or hooks, or a test
// function used other than by calling it — makes discovery fail rather than
// guess.
package jstest

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

const (
	Bun  = "bun-test"
	Node = "node-test"
)

// Separator joins a test's suite names and its own name into a key name.
const Separator = " > "

var testFile = regexp.MustCompile(`\.test\.(ts|tsx|js|jsx|mjs|cjs|mts|cts)$`)

// node is a suite or a test in one file's static tree.
type node struct {
	name     string
	suite    bool
	parent   *node
	children []*node
}

// path returns the suite names and the node's own name, outermost first.
func (n *node) path() []string {
	var p []string
	for ; n != nil && n.parent != nil; n = n.parent {
		p = append([]string{n.name}, p...)
	}
	return p
}

// file is one test file's static tree.
type file struct {
	unit   string
	runner string
	root   *node
	tests  []*node
}

func isTestFile(p string) bool {
	if !testFile.MatchString(p) {
		return false
	}
	for _, el := range strings.Split(p, "/") {
		if el == "node_modules" {
			return false
		}
	}
	return true
}

// parseFile builds the static tree of one test file. It returns the file's
// runner once the imports name one, even when the tree is incomplete.
func parseFile(root, unit string) (*file, string, error) {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(unit)))
	if err != nil {
		return nil, "", err
	}
	loader := api.LoaderJS
	switch path.Ext(unit) {
	case ".ts", ".mts", ".cts":
		loader = api.LoaderTS
	case ".tsx":
		loader = api.LoaderTSX
	case ".jsx":
		loader = api.LoaderJSX
	}
	out := api.Transform(string(src), api.TransformOptions{
		Loader: loader, Target: api.ESNext, Charset: api.CharsetUTF8, Sourcefile: unit, LogLevel: api.LogLevelSilent,
	})
	if len(out.Errors) > 0 {
		e := out.Errors[0]
		if e.Location != nil {
			return nil, "", fmt.Errorf("%s:%d: %s", unit, e.Location.Line, e.Text)
		}
		return nil, "", fmt.Errorf("%s: %s", unit, e.Text)
	}
	ast, err := js.Parse(parse.NewInputBytes(out.Code), js.Options{})
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", unit, err)
	}

	// Imports are not declarations in the parser's scopes: every use of an
	// imported name resolves to the module scope's undeclared variable.
	imported := map[string]*js.Var{}
	for _, v := range ast.BlockStmt.Scope.Undeclared {
		imported[string(v.Data)] = v
	}
	w := &walker{unit: unit, roles: map[*js.Var]string{}}
	bind := func(local, role string) {
		if v := imported[local]; v != nil {
			w.roles[v] = role
		}
	}
	var runners []string
	for _, st := range ast.List {
		imp, ok := st.(*js.ImportStmt)
		if !ok {
			continue
		}
		runner := ""
		switch unquoted(imp.Module) {
		case "bun:test":
			runner = Bun
		case "node:test":
			runner = Node
		default:
			continue
		}
		runners = append(runners, runner)
		// node:test's default export is test, which carries every export;
		// bun:test's is the module namespace.
		w.defaultRole = "namespace"
		if runner == Node {
			w.defaultRole = "test"
		}
		if imp.Default != nil {
			bind(string(imp.Default), w.defaultRole)
		}
		for _, a := range imp.List {
			name, local := string(a.Name), string(a.Binding)
			if name == "" {
				name = local
			}
			switch {
			case name == "*":
				bind(local, "namespace")
			case name == "default":
				bind(local, w.defaultRole)
			case exportRole(name) != "":
				bind(local, exportRole(name))
			}
		}
	}
	switch {
	case len(runners) == 0:
		return nil, "", fmt.Errorf("%s: imports neither bun:test nor node:test", unit)
	case slices.Contains(runners, Bun) && slices.Contains(runners, Node):
		return nil, "", fmt.Errorf("%s: imports both bun:test and node:test", unit)
	}

	f := &file{unit: unit, runner: runners[0], root: &node{suite: true}}
	w.file = f
	w.stmts(ast.List, f.root)
	if len(w.errs) > 0 {
		return nil, f.runner, errors.Join(w.errs...)
	}
	return f, f.runner, nil
}

// exportRole classifies a test module export.
func exportRole(name string) string {
	switch name {
	case "describe", "suite", "xdescribe":
		return "suite"
	case "test", "it", "xit", "xtest":
		return "test"
	}
	return ""
}

// modifiers keep a registration's role: describe.skip(...), test.only(...).
var modifiers = map[string]bool{
	"skip": true, "only": true, "todo": true, "concurrent": true, "serial": true, "failing": true,
}

// conditionals take a condition first: test.if(cond)("name", fn).
var conditionals = map[string]bool{
	"if": true, "skipIf": true, "todoIf": true, "runIf": true, "failingIf": true,
}

type walker struct {
	unit        string
	file        *file
	roles       map[*js.Var]string // imported binding → suite | test | namespace
	defaultRole string             // the role of the test module's default export
	errs        []error
}

// object returns the role of an expression that holds test exports: an
// imported binding, or the default export reached through the namespace.
func (w *walker) object(e js.IExpr) string {
	switch x := e.(type) {
	case *js.Var:
		return w.roles[binding(x)]
	case *js.DotExpr:
		if propName(x.Y) == "default" && w.object(x.X) == "namespace" {
			return w.defaultRole
		}
	}
	return ""
}

// binding resolves a use of a name to the variable it refers to; a locally
// declared name is its own variable, so it never resolves to an import.
func binding(v *js.Var) *js.Var {
	for v.Link != nil {
		v = v.Link
	}
	return v
}

func (w *walker) fail(format string, args ...any) {
	w.errs = append(w.errs, fmt.Errorf("%s: "+format, append([]any{w.unit}, args...)...))
}

// stmts registers the statically placed suites and tests of one body. A
// returned registration counts too: () => test(...) is such a body.
func (w *walker) stmts(list []js.IStmt, parent *node) {
	for _, st := range list {
		var value js.IExpr
		switch x := st.(type) {
		case *js.ExprStmt:
			value = x.Value
		case *js.ReturnStmt:
			value = x.Value
		}
		if call, ok := unwrap(value).(*js.CallExpr); ok && w.register(call, parent) {
			continue
		}
		w.dynamic(st)
	}
}

// unwrap removes await, void, and grouping around a registration call.
func unwrap(e js.IExpr) js.IExpr {
	for {
		switch x := e.(type) {
		case *js.UnaryExpr:
			if x.Op != js.AwaitToken && x.Op != js.VoidToken {
				return e
			}
			e = x.X
		case *js.GroupExpr:
			e = x.X
		default:
			return e
		}
	}
}

// role resolves a callee to suite or test, reporting .each tables.
func (w *walker) role(callee js.IExpr) (string, bool) {
	switch x := callee.(type) {
	case *js.Var:
		r := w.object(x)
		return r, r == "suite" || r == "test"
	case *js.DotExpr:
		prop := propName(x.Y)
		if w.object(x.X) != "" {
			// ns.test, and node's test.describe: an export reached
			// through the module or through test, which carries them.
			if r := exportRole(prop); r != "" {
				return r, true
			}
		}
		if r := w.object(x); r == "suite" || r == "test" {
			return r, true // ns.default
		}
		r, ok := w.role(x.X)
		if !ok {
			return "", false
		}
		if modifiers[prop] {
			return r, true
		}
		if prop == "each" {
			return r + ".each", true
		}
	case *js.CallExpr:
		// test.if(cond)("name", fn) and friends
		if d, ok := x.X.(*js.DotExpr); ok && conditionals[propName(d.Y)] {
			return w.role(d.X)
		}
		// test.each(table)("name", fn)
		if d, ok := x.X.(*js.DotExpr); ok && propName(d.Y) == "each" {
			if r, ok := w.role(d.X); ok {
				return r + ".each", true
			}
		}
	}
	return "", false
}

// registrationMember reports a property that reaches, selects, modifies, or
// rebinds a registration, as opposed to helpers such as mock or expect.
func registrationMember(prop string) bool {
	return exportRole(prop) != "" || modifiers[prop] || conditionals[prop] || rebinders[prop] || prop == "each"
}

// rebinders call or alias a function out of the static tree's sight.
var rebinders = map[string]bool{"bind": true, "call": true, "apply": true}

// innerArgs returns the arguments a callee chain takes itself, such as the
// condition of test.if(cond).
func innerArgs(callee js.IExpr) []js.Arg {
	var out []js.Arg
	for {
		switch x := callee.(type) {
		case *js.CallExpr:
			out = append(out, x.Args.List...)
			callee = x.X
		case *js.DotExpr:
			callee = x.X
		default:
			return out
		}
	}
}

func propName(e js.IExpr) string {
	switch y := e.(type) {
	case js.LiteralExpr:
		return string(y.Data)
	case *js.LiteralExpr:
		return string(y.Data)
	case *js.Var:
		return string(y.Data)
	}
	return ""
}

// register adds one suite or test call; it returns false for other calls.
func (w *walker) register(call *js.CallExpr, parent *node) bool {
	r, ok := w.role(call.X)
	if !ok {
		return false
	}
	if strings.HasSuffix(r, ".each") {
		w.fail("%s.each cannot be named statically; write one call per case", strings.TrimSuffix(r, ".each"))
		return true
	}
	w.args(innerArgs(call.X))
	if len(call.Args.List) == 0 {
		w.fail("a %s call without a name", r)
		return true
	}
	name, ok := literal(call.Args.List[0].Value)
	switch {
	case !ok:
		w.fail("a %s name must be a string literal: %s", r, call.Args.List[0].Value.String())
	case strings.TrimFunc(name, isJSSpace) == "":
		// Node matches names after trimming them, so a blank name would
		// select every test in the file.
		w.fail("a %s name must not be blank", r)
		ok = false
	case strings.ContainsFunc(name, isControl):
		// Reporters rewrite these characters ambiguously, so the result
		// could not be told apart from another test's.
		w.fail("%s %s: names must not contain control characters or line breaks", r, strconv.Quote(name))
		ok = false
	}
	if !ok {
		w.args(call.Args.List[1:])
		return true
	}
	n := &node{name: name, suite: r == "suite", parent: parent}
	parent.children = append(parent.children, n)
	rest := call.Args.List[1:]
	if !n.suite {
		w.file.tests = append(w.file.tests, n)
		w.args(rest)
		return true
	}
	for i, a := range rest {
		if body, ok := callbackBody(a.Value); ok {
			w.args(rest[:i])
			w.stmts(body, n)
			w.args(rest[i+1:])
			return true
		}
	}
	w.args(rest)
	return true
}

func (w *walker) args(list []js.Arg) {
	for _, a := range list {
		w.dynamic(a.Value)
	}
}

func callbackBody(e js.IExpr) ([]js.IStmt, bool) {
	switch f := e.(type) {
	case *js.ArrowFunc:
		return f.Body.List, true
	case *js.FuncDecl:
		return f.Body.List, true
	}
	return nil, false
}

// dynamic reports registrations a static tree cannot place, such as tests
// registered in loops, helpers, hooks, or other tests, and test functions
// used other than by calling them, which could register out of sight.
func (w *walker) dynamic(n js.INode) {
	js.Walk(&dynamicVisitor{w: w}, n)
}

type dynamicVisitor struct {
	w     *walker
	stack []js.INode // ancestors of the node being entered
}

func (v *dynamicVisitor) Enter(n js.INode) js.IVisitor {
	switch x := n.(type) {
	case *js.CallExpr:
		if r, ok := v.w.role(x.X); ok {
			name := "unnamed"
			if len(x.Args.List) > 0 {
				if s, ok := literal(x.Args.List[0].Value); ok {
					name = strconv.Quote(s)
				}
			}
			v.w.fail("%s %s is registered inside a block, loop, helper, hook, or test; register it at the top level or directly in a suite", strings.TrimSuffix(r, ".each"), name)
			// The callee is accounted for; its arguments may hold more.
			for _, a := range append(innerArgs(x.X), x.Args.List...) {
				js.Walk(v, a.Value)
			}
			return nil
		}
	case *js.Var:
		if v.w.roles[binding(x)] != "" {
			// Only a helper member, such as test.mock, may follow the
			// binding or the default export reached through it.
			expr, i := js.IExpr(x), len(v.stack)-1
			for ; i >= 0; i-- {
				d, ok := v.stack[i].(*js.DotExpr)
				if !ok || d.X != expr || propName(d.Y) != "default" || v.w.object(expr) != "namespace" {
					break
				}
				expr = d
			}
			var parent js.INode
			if i >= 0 {
				parent = v.stack[i]
			}
			if d, ok := parent.(*js.DotExpr); !ok || d.X != expr || registrationMember(propName(d.Y)) {
				v.w.fail("%s is used other than by calling it; call test functions directly", x.Data)
			}
		}
	}
	v.stack = append(v.stack, n)
	return v
}

func (v *dynamicVisitor) Exit(js.INode) { v.stack = v.stack[:len(v.stack)-1] }

// isJSSpace reports what String.prototype.trim removes.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\ufeff', '\u2028', '\u2029':
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

func isControl(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
}

// jsTrim is String.prototype.trim.
func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

// literal returns the value of a string literal or a template literal
// without substitutions.
func literal(e js.IExpr) (string, bool) {
	switch x := e.(type) {
	case js.LiteralExpr:
		return literal(&x)
	case *js.LiteralExpr:
		if x.TokenType != js.StringToken {
			return "", false
		}
		return unquote(x.Data)
	case *js.TemplateExpr:
		if x.Tag != nil || len(x.List) > 0 || len(x.Tail) < 2 {
			return "", false
		}
		return unescape(x.Tail[1 : len(x.Tail)-1])
	case *js.GroupExpr:
		return literal(x.X)
	}
	return "", false
}

func unquoted(b []byte) string {
	s, _ := unquote(b)
	return s
}

func unquote(b []byte) (string, bool) {
	if len(b) < 2 || (b[0] != '"' && b[0] != '\'') || b[len(b)-1] != b[0] {
		return "", false
	}
	return unescape(b[1 : len(b)-1])
}

// unescape decodes JavaScript string escapes.
func unescape(b []byte) (string, bool) {
	var units []uint16
	add := func(r rune) {
		units = append(units, utf16.Encode([]rune{r})...)
	}
	s := string(b)
	for i := 0; i < len(s); {
		r, size := rune(s[i]), 1
		if r >= 0x80 {
			r, size = decodeRune(s[i:])
		}
		if r != '\\' {
			add(r)
			i += size
			continue
		}
		i++
		if i >= len(s) {
			return "", false
		}
		c := s[i]
		i++
		switch c {
		case 'n':
			add('\n')
		case 't':
			add('\t')
		case 'r':
			add('\r')
		case 'b':
			add('\b')
		case 'f':
			add('\f')
		case 'v':
			add('\v')
		case '0':
			add(0)
		case '\r':
			if i < len(s) && s[i] == '\n' {
				i++
			}
		case '\n':
		case 'x':
			if i+2 > len(s) {
				return "", false
			}
			v, err := strconv.ParseUint(s[i:i+2], 16, 8)
			if err != nil {
				return "", false
			}
			add(rune(v))
			i += 2
		case 'u':
			if i < len(s) && s[i] == '{' {
				end := strings.IndexByte(s[i:], '}')
				if end < 0 {
					return "", false
				}
				v, err := strconv.ParseUint(s[i+1:i+end], 16, 32)
				if err != nil {
					return "", false
				}
				add(rune(v))
				i += end + 1
				continue
			}
			if i+4 > len(s) {
				return "", false
			}
			v, err := strconv.ParseUint(s[i:i+4], 16, 16)
			if err != nil {
				return "", false
			}
			units = append(units, uint16(v)) // surrogate halves pair up in utf16.Decode
			i += 4
		default:
			// \' \" \\ \` and identity escapes; a line separator escape is
			// a continuation.
			r, size := decodeRune(s[i-1:])
			i += size - 1
			if r != ' ' && r != ' ' {
				add(r)
			}
		}
	}
	return string(utf16.Decode(units)), true
}

func decodeRune(s string) (rune, int) {
	return utf8.DecodeRuneInString(s)
}
