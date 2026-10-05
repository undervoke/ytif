package gohost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gomod"
)

// pkg is one importable package that defines checks.
type pkg struct {
	Module     string // repo-relative module root
	ModulePath string
	Dir        string // repo-relative package directory
	ImportPath string
	Domain     string   // repo-relative root of the tree allowed to import Dir
	BuildTags  []string // the go command's own build tags in the module
	Funcs      []*fn    // valid checks, providers before their dependents
}

// fn is one valid check.
type fn struct {
	Pkg      *pkg
	Name     string
	Key      check.Key
	Args     []arg    // parameters between ctx and w, in order
	Provides string   // canonical provided type; "" for a plain check
	Deps     []*fn    // providers this check consumes
	pending  []string // canonical provided types, resolved into Deps
}

// arg is a middle parameter: the scope's files, or a provider's value.
type arg struct {
	Files bool
	Dep   *fn
}

// discover finds every check in the repository's tracked Go sources. It
// returns the valid packages even when some definitions are invalid; the
// error lists every rejected definition.
func discover(ctx context.Context, repo check.Repo) ([]*pkg, error) {
	modules := gomod.Modules(repo.Tracked)
	byDir := map[string][]string{}
	for _, f := range repo.Tracked {
		if !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "_test.go") || gomod.Excluded(f) {
			continue
		}
		dir := path.Dir(f)
		byDir[dir] = append(byDir[dir], path.Base(f))
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var errs []error
	var pkgs []*pkg
	modulePaths := map[string]string{}
	contexts := map[string]*build.Context{} // by module; nil when go env failed
	for _, dir := range dirs {
		names := byDir[dir]
		mod, inModule := gomod.Owner(modules, dir)
		envDir := mod
		if !inModule {
			envDir = "."
		}
		ordinary, seen := contexts[envDir]
		if !seen {
			c, err := gomod.BuildContext(ctx, filepath.Join(repo.Root, filepath.FromSlash(envDir)))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", envDir, err))
			} else {
				ordinary = &c
			}
			contexts[envDir] = ordinary
		}
		if ordinary == nil {
			continue
		}
		vfiles, err := verificationFiles(*ordinary, repo.Root, dir, names)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(vfiles) == 0 {
			continue
		}
		if !inModule {
			errs = append(errs, fmt.Errorf("%s: verification files outside any Go module", dir))
			continue
		}
		modPath, ok := modulePaths[mod]
		if !ok {
			modPath, err = modulePath(ctx, filepath.Join(repo.Root, filepath.FromSlash(mod)))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", mod, err))
				continue
			}
			modulePaths[mod] = modPath
		}
		p, perrs := parsePackage(*ordinary, repo.Root, mod, modPath, dir, names, vfiles)
		errs = append(errs, perrs...)
		if p != nil && len(p.Funcs) > 0 {
			pkgs = append(pkgs, p)
		}
	}
	return pkgs, errors.Join(errs...)
}

func modulePath(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "mod", "edit", "-json")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go mod edit -json: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var mod struct{ Module struct{ Path string } }
	if err := json.Unmarshal(out, &mod); err != nil || mod.Module.Path == "" {
		return "", fmt.Errorf("go mod edit -json: no module path")
	}
	return mod.Module.Path, nil
}

// verificationFiles returns the files in dir that build with the
// verification tag and not without it, so checks never reach an ordinary
// build.
func verificationFiles(ordinary build.Context, root, dir string, names []string) ([]string, error) {
	with, without := withTag(ordinary), ordinary
	abs := filepath.Join(root, filepath.FromSlash(dir))
	var out []string
	for _, name := range names {
		in, err := with.MatchFile(abs, name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path.Join(dir, name), err)
		}
		if !in {
			continue
		}
		if ordinary, err := without.MatchFile(abs, name); err != nil || ordinary {
			continue
		}
		if !with.CgoEnabled && importsC(filepath.Join(abs, name)) {
			continue // the go command drops cgo files when cgo is disabled
		}
		out = append(out, name)
	}
	return out, nil
}

// importsC reports whether a file uses cgo.
func importsC(file string) bool {
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
	if err != nil {
		return false
	}
	for _, spec := range f.Imports {
		if spec.Path.Value == `"C"` {
			return true
		}
	}
	return false
}

// withTag is the dispatcher build: the ordinary build plus the verification
// tag.
func withTag(ordinary build.Context) build.Context {
	ordinary.BuildTags = append(append([]string(nil), ordinary.BuildTags...), Tag)
	return ordinary
}

// parsePackage validates one directory's checks and orders them so every
// provider precedes its dependents.
func parsePackage(ordinary build.Context, root, mod, modPath, dir string, names, vfiles []string) (*pkg, []error) {
	abs := filepath.Join(root, filepath.FromSlash(dir))
	ctx := withTag(ordinary)
	fset := token.NewFileSet()

	// Parse every file the verification build compiles, for the package
	// name and the type declarations that provided types may refer to.
	pkgName := ""
	index := newTypeIndex()
	parsed := map[string]*ast.File{}
	for _, name := range names {
		if ok, _ := ctx.MatchFile(abs, name); !ok {
			continue
		}
		file := filepath.Join(abs, name)
		if !ctx.CgoEnabled && importsC(file) {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, []error{err}
		}
		if f.Name.Name == "documentation" {
			continue
		}
		switch {
		case pkgName == "":
			pkgName = f.Name.Name
		case pkgName != f.Name.Name:
			return nil, []error{fmt.Errorf("%s: packages %s and %s in one directory", dir, pkgName, f.Name.Name)}
		}
		index.declare(f)
		parsed[name] = f
	}
	if pkgName == "main" {
		return nil, []error{fmt.Errorf("%s: checks must live in an importable package, not main", dir)}
	}

	rel := gomod.Rel(mod, dir)
	importPath := modPath
	if rel != "" {
		importPath = modPath + "/" + rel
	}
	p := &pkg{Module: mod, ModulePath: modPath, Dir: dir, ImportPath: importPath, Domain: domainOf(mod, rel), BuildTags: ordinary.BuildTags}

	var errs []error
	var funcs []*fn
	for _, name := range vfiles {
		f := parsed[name]
		if f == nil {
			continue
		}
		scope := newFileScope(f)
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !isCheckName(fd.Name.Name) {
				continue
			}
			c, err := signature(fd, scope, index)
			where := fset.Position(fd.Pos())
			if err != nil {
				errs = append(errs, fmt.Errorf("%s:%d: %s: %v", path.Join(dir, name), where.Line, fd.Name.Name, err))
				continue
			}
			c.Pkg = p
			c.Name = fd.Name.Name
			c.Key = check.Key{Runner: Runner, Unit: dir, Name: c.Name}
			funcs = append(funcs, c)
		}
	}
	p.Funcs, errs = link(dir, funcs, errs)
	return p, errs
}

// domainOf returns the repo-relative root of the tree allowed to import a
// package: the parent of its last internal element, else its module root.
func domainOf(mod, rel string) string {
	els := strings.Split(rel, "/")
	cut := -1
	for i, el := range els {
		if el == "internal" {
			cut = i
		}
	}
	if cut <= 0 {
		return mod
	}
	return path.Join(mod, strings.Join(els[:cut], "/"))
}

// isCheckName mirrors go test's rule for TestXxx: Verify, optionally followed
// by a name that does not start with a lowercase letter.
func isCheckName(name string) bool {
	rest, ok := strings.CutPrefix(name, "Verify")
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return !unicode.IsLower(r)
}

const wantSignature = "want func(ctx context.Context, [files []string], [provided values...], w io.Writer) error or (T, error)"

// signature validates a check's shape and records its middle parameters.
func signature(fd *ast.FuncDecl, scope fileScope, index *typeIndex) (*fn, error) {
	if fd.Type.TypeParams != nil && len(fd.Type.TypeParams.List) > 0 {
		return nil, errors.New("checks cannot be generic")
	}
	canon := func(e ast.Expr) string { return index.canon(e, scope) }
	params := flatten(fd.Type.Params)
	n := len(params)
	if n < 2 || canon(params[0]) != "context.Context" || canon(params[n-1]) != "io.Writer" {
		return nil, errors.New(wantSignature)
	}
	c := &fn{}
	files := 0
	for _, p := range params[1 : n-1] {
		if _, ok := p.(*ast.Ellipsis); ok {
			return nil, errors.New("checks cannot be variadic")
		}
		t := canon(p)
		if t == "[]string" {
			files++
			c.Args = append(c.Args, arg{Files: true})
			continue
		}
		c.Args = append(c.Args, arg{})
		c.pending = append(c.pending, t)
	}
	if files > 1 {
		return nil, errors.New("at most one files []string parameter")
	}
	results := flatten(fd.Type.Results)
	switch {
	case len(results) == 1 && canon(results[0]) == "error":
	case len(results) == 2 && canon(results[1]) == "error":
		c.Provides = canon(results[0])
		if c.Provides == "[]string" || c.Provides == "error" {
			return nil, fmt.Errorf("cannot provide %s", c.Provides)
		}
	default:
		return nil, errors.New(wantSignature)
	}
	return c, nil
}

// link resolves provided values to providers in the same package, rejects
// duplicates, missing providers, and cycles, and orders providers first.
func link(dir string, funcs []*fn, errs []error) ([]*fn, []error) {
	providers := map[string]*fn{}
	dupes := map[string]bool{}
	for _, f := range funcs {
		if f.Provides == "" {
			continue
		}
		if other := providers[f.Provides]; other != nil {
			errs = append(errs, fmt.Errorf("%s: %s and %s both provide %s", dir, other.Name, f.Name, f.Provides))
			dupes[f.Provides] = true
		}
		providers[f.Provides] = f
	}
	invalid := map[*fn]bool{}
	for _, f := range funcs {
		if f.Provides != "" && dupes[f.Provides] {
			invalid[f] = true
			continue
		}
		next := 0
		for i := range f.Args {
			if f.Args[i].Files {
				continue
			}
			t := f.pending[next]
			next++
			p := providers[t]
			if p == nil || dupes[t] {
				errs = append(errs, fmt.Errorf("%s: %s needs %s, which no single check in the package provides", dir, f.Name, t))
				invalid[f] = true
				break
			}
			f.Args[i].Dep = p
			f.Deps = append(f.Deps, p)
		}
	}

	// Order providers first; a cycle or an invalid provider invalidates its
	// dependents.
	state := map[*fn]int{} // 0 new, 1 visiting, 2 done
	var ordered []*fn
	var visit func(f *fn) bool
	visit = func(f *fn) bool {
		switch state[f] {
		case 1:
			errs = append(errs, fmt.Errorf("%s: provider cycle through %s", dir, f.Name))
			invalid[f] = true
			return false
		case 2:
			return !invalid[f]
		}
		state[f] = 1
		ok := !invalid[f]
		for _, d := range f.Deps {
			if !visit(d) {
				if ok {
					errs = append(errs, fmt.Errorf("%s: %s depends on rejected %s", dir, f.Name, d.Name))
				}
				ok = false
			}
		}
		state[f] = 2
		if !ok {
			invalid[f] = true
			return false
		}
		ordered = append(ordered, f)
		return true
	}
	for _, f := range funcs {
		visit(f)
	}
	return ordered, errs
}

func flatten(fl *ast.FieldList) []ast.Expr {
	if fl == nil {
		return nil
	}
	var out []ast.Expr
	for _, f := range fl.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for range n {
			out = append(out, f.Type)
		}
	}
	return out
}
