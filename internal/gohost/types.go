package gohost

import (
	"go/ast"
	"go/types"
	"path"
	"strconv"
	"strings"
	"unicode"
)

// fileScope is how one file names imported packages.
type fileScope struct {
	names map[string]string // local package name → import path
	dots  []string          // dot-imported paths
}

func newFileScope(f *ast.File) fileScope {
	s := fileScope{names: map[string]string{}}
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		switch {
		case spec.Name == nil:
			s.names[assumedName(p)] = p
		case spec.Name.Name == ".":
			s.dots = append(s.dots, p)
		case spec.Name.Name != "_":
			s.names[spec.Name.Name] = p
		}
	}
	return s
}

// assumedName is the package name an unnamed import is assumed to declare,
// as goimports assumes it: the last path element without a major-version
// element, a "go-" prefix, or a non-identifier suffix.
func assumedName(importPath string) string {
	base := path.Base(importPath)
	if strings.HasPrefix(base, "v") {
		if _, err := strconv.Atoi(base[1:]); err == nil {
			if dir := path.Dir(importPath); dir != "." {
				base = path.Base(dir)
			}
		}
	}
	base = strings.TrimPrefix(base, "go-")
	if i := strings.IndexFunc(base, func(r rune) bool { return r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) }); i >= 0 {
		base = base[:i]
	}
	return base
}

// typeIndex renders the type expressions of one package so that identical
// types compare equal: import qualifiers become import paths, and
// predeclared and package-level aliases resolve to their targets. Aliases
// declared in other packages are not followed.
type typeIndex struct {
	aliases map[string]alias // package-level alias → its target
	locals  map[string]bool  // package-level defined type names
}

type alias struct {
	target ast.Expr
	scope  fileScope
}

var predeclaredAliases = map[string]string{"byte": "uint8", "rune": "int32", "any": "interface{}"}

func newTypeIndex() *typeIndex {
	return &typeIndex{aliases: map[string]alias{}, locals: map[string]bool{}}
}

// declare records the package-level type declarations of one file.
func (x *typeIndex) declare(f *ast.File) {
	scope := newFileScope(f)
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if ts.Assign.IsValid() && ts.TypeParams.NumFields() == 0 {
				x.aliases[ts.Name.Name] = alias{ts.Type, scope}
			} else {
				x.locals[ts.Name.Name] = true
			}
		}
	}
}

func (x *typeIndex) canon(e ast.Expr, s fileScope) string {
	return x.render(e, s, 0)
}

const maxAliasDepth = 32

func (x *typeIndex) render(e ast.Expr, s fileScope, depth int) string {
	r := func(e ast.Expr) string { return x.render(e, s, depth) }
	switch t := e.(type) {
	case *ast.Ident:
		name := t.Name
		switch {
		case x.locals[name]:
			return name
		case x.aliases[name].target != nil && depth < maxAliasDepth:
			a := x.aliases[name]
			return x.render(a.target, a.scope, depth+1)
		case predeclaredAliases[name] != "":
			return predeclaredAliases[name]
		case types.Universe.Lookup(name) == nil && len(s.dots) == 1:
			return s.dots[0] + "." + name
		}
		return name
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			if p, ok := s.names[id.Name]; ok {
				return p + "." + t.Sel.Name
			}
		}
	case *ast.StarExpr:
		return "*" + r(t.X)
	case *ast.ParenExpr:
		return r(t.X)
	case *ast.Ellipsis:
		return "..." + r(t.Elt)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + r(t.Elt)
		}
		return "[" + types.ExprString(t.Len) + "]" + r(t.Elt)
	case *ast.MapType:
		return "map[" + r(t.Key) + "]" + r(t.Value)
	case *ast.ChanType:
		switch t.Dir {
		case ast.SEND:
			return "chan<- " + r(t.Value)
		case ast.RECV:
			return "<-chan " + r(t.Value)
		}
		return "chan " + r(t.Value)
	case *ast.FuncType:
		return "func(" + x.fields(t.Params, s, depth, false) + ")" + results(x.fields(t.Results, s, depth, false))
	case *ast.StructType:
		return "struct{" + x.fields(t.Fields, s, depth, true) + "}"
	case *ast.InterfaceType:
		return "interface{" + x.fields(t.Methods, s, depth, true) + "}"
	case *ast.IndexExpr:
		return r(t.X) + "[" + r(t.Index) + "]"
	case *ast.IndexListExpr:
		args := make([]string, len(t.Indices))
		for i, a := range t.Indices {
			args[i] = r(a)
		}
		return r(t.X) + "[" + strings.Join(args, ",") + "]"
	}
	return types.ExprString(e)
}

// fields renders a field list; named lists (struct fields, methods) keep
// names and tags, parameter lists keep only types.
func (x *typeIndex) fields(fl *ast.FieldList, s fileScope, depth int, named bool) string {
	if fl == nil {
		return ""
	}
	var parts []string
	for _, f := range fl.List {
		t := x.render(f.Type, s, depth)
		if !named || len(f.Names) == 0 {
			for range max(1, len(f.Names)) {
				parts = append(parts, t)
			}
			continue
		}
		for _, n := range f.Names {
			p := n.Name + " " + t
			if f.Tag != nil {
				p += " " + f.Tag.Value
			}
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "; ")
}

func results(s string) string {
	switch {
	case s == "":
		return ""
	case strings.Contains(s, "; "):
		return " (" + s + ")"
	}
	return " " + s
}
