// Package targeting handles a small ESLint subset. Unsupported inputs keep
// the registered invocation; it does not reproduce Node module resolution.
package targeting

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"

	"github.com/undervoke/ytif/internal/inventory"
)

// Supported with no Targets means a resolved empty domain, not a fallback.
type Result struct {
	Supported bool
	Args      []string
	Targets   []string
	Reason    string
}

const argvBudget = 32 * 1024

func ESLint(root string, e inventory.Entry, changed []string) Result {
	r, err := plan(root, e, changed)
	if err != nil {
		return Result{Reason: err.Error()}
	}
	return r
}

func plan(root string, e inventory.Entry, changed []string) (Result, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Result{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Result{}, err
	}
	s := scan{root: root, changed: map[string]bool{}, seen: map[string]bool{}}
	cwd := at(root, e.Cwd)
	info, err := s.info(cwd)
	if err != nil {
		return Result{}, err
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("cwd is not a directory: %s", cwd)
	}
	prefix, tail, err := launcher(root, cwd, e)
	if err != nil {
		return Result{}, err
	}
	opts, err := arguments(tail)
	if err != nil {
		return Result{}, err
	}
	if len(opts.operands) == 0 {
		opts.operands = []string{"."}
	}
	domains := map[string]bool{}
	for _, operand := range opts.operands {
		p := at(cwd, operand)
		info, err := s.info(p)
		if err != nil {
			return Result{}, err
		}
		// Explicit ignored files can fail the original --max-warnings run.
		if !info.IsDir() && !slices.Contains(opts.flags, "--no-warn-ignored") {
			return Result{}, fmt.Errorf("explicit file operand without --no-warn-ignored")
		}
		domains[p] = info.IsDir()
	}
	for _, p := range changed {
		if filepath.IsAbs(p) || !within(root, at(root, p)) {
			return Result{}, fmt.Errorf("changed path outside repository: %s", p)
		}
		s.changed[at(root, p)] = true
		// Broad invalidation avoids reconstructing config ownership and
		// also covers additions/deletions of a helper's nearest package.json.
		if infrastructure(filepath.Base(p)) {
			return Result{}, fmt.Errorf("config input changed: %s", p)
		}
	}
	configs, err := s.configs(domains, cwd, opts.config)
	if err != nil {
		return Result{}, err
	}
	if len(configs) == 0 {
		return Result{}, fmt.Errorf("no repository ESLint config")
	}
	for _, p := range configs {
		if err := s.dependency(p); err != nil {
			return Result{}, err
		}
	}
	var targets []string
	for p := range s.changed {
		info, err := s.info(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return Result{}, err
		}
		if info.IsDir() {
			return Result{}, fmt.Errorf("changed path is a directory: %s", p)
		}
		for d, dir := range domains {
			if p == d || dir && within(d, p) {
				rel, _ := filepath.Rel(root, p)
				targets = append(targets, filepath.ToSlash(rel))
				break
			}
		}
	}
	sort.Strings(targets)
	r := Result{Supported: true, Targets: targets}
	if len(targets) == 0 {
		return r, nil
	}
	r.Args = append(append([]string{}, prefix...), opts.flags...)
	if !slices.Contains(opts.flags, "--no-warn-ignored") {
		r.Args = append(r.Args, "--no-warn-ignored")
	}
	r.Args = append(r.Args, "--")
	for _, p := range targets {
		rel, _ := filepath.Rel(cwd, at(root, p))
		r.Args = append(r.Args, filepath.ToSlash(rel))
	}
	size := 0
	for _, arg := range r.Args {
		size += len(arg) + 1
	}
	if size > argvBudget {
		return Result{}, fmt.Errorf("argv too long: %d bytes", size)
	}
	return r, nil
}

// Version evidence follows the executable for direct calls, and the local
// package plus .bin shim for pnpm. Config-path restrictions do not apply to
// package-manager executable links.
func launcher(root, cwd string, e inventory.Entry) ([]string, []string, error) {
	if e.Runner != inventory.CommandRunner || len(e.Run) == 0 {
		return nil, nil, fmt.Errorf("not an ESLint command")
	}
	n := 1
	var bin string
	switch {
	case e.Run[0] == "eslint":
		var err error
		bin, err = exec.LookPath("eslint")
		if err != nil || !filepath.IsAbs(bin) {
			return nil, nil, fmt.Errorf("unresolved ESLint executable")
		}
	case len(e.Run) >= 3 && slices.Equal(e.Run[:3], []string{"pnpm", "exec", "eslint"}):
		n = 3
		for d := cwd; within(root, d); d = filepath.Dir(d) {
			shim := filepath.Join(d, "node_modules", ".bin", "eslint")
			if _, err := os.Stat(shim); err == nil {
				err = eslintVersion(filepath.Join(d, "node_modules", "eslint", "package.json"))
				return e.Run[:n], e.Run[n:], err
			}
			if d == root {
				break
			}
		}
		return nil, nil, fmt.Errorf("unresolved local ESLint package and shim")
	case strings.HasSuffix("/"+filepath.ToSlash(e.Run[0]), "/node_modules/.bin/eslint"):
		bin = at(cwd, e.Run[0])
	default:
		return nil, nil, fmt.Errorf("unrecognized command form: %s", e.Run[0])
	}
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return nil, nil, err
	}
	if e.Run[0] == "eslint" && !within(root, real) {
		return nil, nil, fmt.Errorf("bare ESLint executable outside repository")
	}
	for d, i := filepath.Dir(real), 0; i < 5; d, i = filepath.Dir(d), i+1 {
		p := filepath.Join(d, "package.json")
		if _, err := os.Stat(p); err == nil {
			return e.Run[:n], e.Run[n:], eslintVersion(p)
		}
	}
	return nil, nil, fmt.Errorf("unresolved ESLint executable package")
}

func eslintVersion(p string) error {
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var pkg struct{ Name, Version string }
	if err := json.Unmarshal(data, &pkg); err != nil {
		return err
	}
	if pkg.Name != "eslint" || !strings.HasPrefix(pkg.Version, "10.") {
		return fmt.Errorf("unsupported ESLint package %q version %q", pkg.Name, pkg.Version)
	}
	return nil
}

type options struct {
	flags, operands []string
	config          string
}

func arguments(args []string) (options, error) {
	var o options
	flags := true
	for i := 0; i < len(args); i++ {
		a := args[i]
		if flags && a == "--" {
			flags = false
			continue
		}
		if flags && strings.HasPrefix(a, "-") {
			name, value, equal := strings.Cut(a, "=")
			switch name {
			case "--quiet", "--no-warn-ignored", "--cache", "--no-cache":
				if equal {
					return o, fmt.Errorf("unsupported option %q", a)
				}
				o.flags = append(o.flags, a)
				continue
			case "--max-warnings", "--config", "-c":
				if !equal {
					i++
					if i == len(args) {
						return o, fmt.Errorf("missing option value: %s", name)
					}
					value = args[i]
				}
				if value == "" {
					return o, fmt.Errorf("empty option value: %s", name)
				}
				o.flags = append(o.flags, name, value)
				if name != "--max-warnings" {
					o.config = value
				}
				continue
			default:
				return o, fmt.Errorf("unsupported option %q", a)
			}
		}
		if a == "" || strings.ContainsAny(a, "*?[]{}!\x00") {
			return o, fmt.Errorf("nonliteral operand %q", a)
		}
		o.operands = append(o.operands, a)
	}
	return o, nil
}

type scan struct {
	root          string
	changed, seen map[string]bool
}

// Exact spelling and regular paths let all dependency comparisons stay
// lexical. A case or symlink discrepancy widens instead of being resolved.
func (s *scan) info(p string) (os.FileInfo, error) {
	if !within(s.root, p) {
		return nil, fmt.Errorf("path outside repository: %s", p)
	}
	rel, _ := filepath.Rel(s.root, p)
	d := s.root
	if rel != "." {
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			entries, err := os.ReadDir(d)
			if err != nil {
				return nil, err
			}
			present := slices.ContainsFunc(entries, func(e fs.DirEntry) bool { return e.Name() == part })
			d = filepath.Join(d, part)
			info, err := os.Lstat(d)
			if err != nil {
				return nil, err
			}
			if !present || info.Mode()&fs.ModeSymlink != 0 {
				return nil, fmt.Errorf("case mismatch or symlink: %s", d)
			}
		}
	}
	info, err := os.Lstat(p)
	if err == nil && !info.IsDir() && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", p)
	}
	return info, err
}

func (s *scan) configs(domains map[string]bool, cwd, explicit string) ([]string, error) {
	if explicit != "" {
		return []string{at(cwd, explicit)}, nil
	}
	found := map[string]bool{}
	parents := map[string]bool{}
	for p, dir := range domains {
		start := p
		if !dir {
			start = filepath.Dir(p)
		}
		for d := start; within(s.root, d) && !parents[d]; d = filepath.Dir(d) {
			parents[d] = true
			entries, err := os.ReadDir(d)
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				if strings.HasPrefix(strings.ToLower(e.Name()), "eslint.config.") {
					found[filepath.Join(d, strings.ToLower(e.Name()))] = true
				}
			}
			if d == s.root {
				break
			}
		}
		if !dir {
			continue
		}
		err := filepath.WalkDir(p, func(f string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.Name() == ".git" || e.Name() == "node_modules" {
				if e.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if e.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf("symlink in ESLint domain: %s", f)
			}
			if strings.HasPrefix(strings.ToLower(e.Name()), "eslint.config.") {
				found[filepath.Join(filepath.Dir(f), strings.ToLower(e.Name()))] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	var out []string
	for p := range found {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func (s *scan) dependency(p string) error {
	if s.seen[p] {
		return nil
	}
	s.seen[p] = true
	if len(s.seen) > 200 {
		return fmt.Errorf("too many config dependencies")
	}
	if s.changed[p] {
		return fmt.Errorf("config dependency changed: %s", p)
	}
	info, err := s.info(p)
	if err != nil {
		return err
	}
	if info.IsDir() || !slices.Contains([]string{".js", ".mjs", ".cjs"}, filepath.Ext(p)) {
		return fmt.Errorf("config dependency is not an explicit JavaScript file: %s", p)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	ast, err := js.Parse(parse.NewInputBytes(data), js.Options{})
	if err != nil {
		return fmt.Errorf("config %s: %w", p, err)
	}
	v := imports{}
	js.Walk(&v, ast)
	if v.err != nil {
		return fmt.Errorf("config %s: %w", p, v.err)
	}
	for _, spec := range v.paths {
		if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
			if err := s.dependency(at(filepath.Dir(p), spec)); err != nil {
				return err
			}
		} else if !s.installed(p, spec) {
			return fmt.Errorf("unmodeled config import: %s", spec)
		}
	}
	return nil
}

// Only classify packages here; exports/main and workspace source resolution
// belong to Node. Installed-package changes are covered by broad invalidation.
func (s *scan) installed(importer, spec string) bool {
	if strings.HasPrefix(spec, "node:") || slices.Contains(nodeBuiltins, spec) {
		return true
	}
	parts := strings.Split(spec, "/")
	n := 1
	if strings.HasPrefix(spec, "@") {
		n = 2
	}
	if len(parts) < n || strings.ContainsAny(spec, "\\?#:") ||
		slices.Contains(parts, "") || slices.Contains(parts, ".") || slices.Contains(parts, "..") {
		return false
	}
	name := filepath.Join(parts[:n]...)
	for d := filepath.Dir(importer); within(s.root, d); d = filepath.Dir(d) {
		p := filepath.Join(d, "node_modules", name)
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			if err != nil {
				return false
			}
			real, err := filepath.EvalSymlinks(p)
			// A node_modules ancestor of the repository says nothing about
			// whether a package inside it is installed or workspace source.
			if err == nil && within(s.root, real) {
				real, _ = filepath.Rel(s.root, real)
			}
			return err == nil && strings.Contains("/"+filepath.ToSlash(real), "/node_modules/")
		}
		if d == s.root {
			break
		}
	}
	return false
}

var nodeBuiltins = strings.Fields("assert async_hooks buffer child_process cluster console constants crypto " +
	"dgram diagnostics_channel dns domain events fs http http2 https inspector module net os path " +
	"perf_hooks process punycode querystring readline repl stream string_decoder sys timers tls " +
	"trace_events tty url util v8 vm wasi worker_threads zlib")

type imports struct {
	paths []string
	err   error
}

func (v *imports) add(raw []byte) {
	s := string(raw)
	// Escaped specifiers are valid JS, but outside this deliberately small grammar.
	if len(s) < 2 || s[0] != s[len(s)-1] || s[0] != '\'' && s[0] != '"' ||
		strings.ContainsAny(s[1:len(s)-1], "\\\x00\r\n?#") {
		v.err = fmt.Errorf("unmodeled import specifier %s", s)
		return
	}
	v.paths = append(v.paths, s[1:len(s)-1])
}

func (v *imports) Enter(n js.INode) js.IVisitor {
	if v.err != nil {
		return nil
	}
	switch x := n.(type) {
	case *js.ImportStmt:
		v.add(x.Module)
	case *js.ExportStmt:
		if x.Module != nil {
			v.add(x.Module)
		}
	case *js.CallExpr:
		if name, ok := x.X.(*js.Var); ok && string(name.Data) == "require" {
			if len(x.Args.List) == 1 && !x.Args.List[0].Rest {
				switch lit := x.Args.List[0].Value.(type) {
				case *js.LiteralExpr:
					if lit.TokenType == js.StringToken {
						v.add(lit.Data)
						return nil
					}
				case js.LiteralExpr:
					if lit.TokenType == js.StringToken {
						v.add(lit.Data)
						return nil
					}
				}
			}
			v.err = fmt.Errorf("unmodeled require call")
		}
	case *js.Var:
		if slices.Contains([]string{"require", "URL", "__dirname", "__filename"}, string(x.Data)) {
			v.err = fmt.Errorf("unmodeled config reference: %s", x.Data)
		}
	case *js.ImportMetaExpr, js.ImportMetaExpr:
		v.err = fmt.Errorf("unmodeled import.meta reference")
	case *js.LiteralExpr:
		if x.TokenType == js.ImportToken {
			v.err = fmt.Errorf("dynamic import")
		}
	case js.LiteralExpr:
		if x.TokenType == js.ImportToken {
			v.err = fmt.Errorf("dynamic import")
		}
	}
	return v
}

func (*imports) Exit(js.INode) {}

func infrastructure(name string) bool {
	name = strings.ToLower(name)
	if strings.HasPrefix(name, "eslint.config.") || strings.HasPrefix(name, "tsconfig") && strings.HasSuffix(name, ".json") {
		return true
	}
	return slices.Contains([]string{
		".gitignore", ".eslintignore", "package.json", "package-lock.json", "npm-shrinkwrap.json",
		"pnpm-lock.yaml", "pnpm-workspace.yaml", "yarn.lock", ".yarnrc", ".yarnrc.yml", "bun.lock", "bun.lockb",
		".npmrc", ".nvmrc", ".node-version", ".tool-versions", ".volta.json", "jsconfig.json",
	}, name)
}

func at(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, filepath.FromSlash(p))
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
