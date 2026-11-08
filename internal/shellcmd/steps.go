// Steps preserves execution structure for the duration guard. It models
// && success paths and the left side of ||, rather than interpreting shell
// conditions. Self-grant inspection uses Parse independently.
package shellcmd

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Unit is a simple command, pipeline, or nested statement list.
// Isolated keeps a subshell's directory changes out of its parent.
// UnknownDir marks an unmodelled directory effect between priced units.
type Unit struct {
	Commands   []Command
	Stages     []Unit
	Nested     []Step
	Piped      bool
	Isolated   bool
	UnknownDir bool
}

// Step is one top-level sequence. Alt means its ending directory is unknown.
type Step struct {
	Units []Unit
	Alt   bool
}

type stepParser struct {
	functions map[string]bool
	unknown   bool
}

// Steps returns supported work without expanding wrappers. Background jobs
// and control-flow bodies stay unknown; blocks and subshells retain scope.
func Steps(script string) ([]Step, bool, error) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil {
		return nil, false, err
	}
	p := stepParser{functions: map[string]bool{}}
	syntax.Walk(f, func(n syntax.Node) bool {
		if fn, ok := n.(*syntax.FuncDecl); ok {
			p.functions[fn.Name.Value] = true
		}
		return true
	})
	steps := p.list(f.Stmts)
	return steps, p.unknown, nil
}

func (p *stepParser) list(stmts []*syntax.Stmt) []Step {
	var steps []Step
	for _, s := range stmts {
		units, alt := p.convert(s)
		if len(units) > 0 || alt {
			steps = append(steps, Step{Units: units, Alt: alt})
		}
	}
	return steps
}

func (p *stepParser) convert(s *syntax.Stmt) (units []Unit, alt bool) {
	if s == nil || s.Cmd == nil {
		return nil, false
	}
	if s.Background || s.Coprocess {
		p.unknown = true
		return nil, false // a child cannot move its parent
	}
	switch x := s.Cmd.(type) {
	case *syntax.CallExpr:
		if len(x.Args) == 0 {
			return nil, false
		}
		c := Unwrap(command(x))
		c.Stdin = stdin(s, true)
		if p.functions[c.Name()] {
			p.unknown = true
			return nil, true
		}
		return []Unit{{Commands: []Command{c}}}, false
	case *syntax.DeclClause:
		return nil, false
	case *syntax.TimeClause:
		return p.convert(x.Stmt)
	case *syntax.Subshell:
		return []Unit{{Nested: p.list(x.Stmts), Isolated: true}}, false
	case *syntax.Block:
		return []Unit{{Nested: p.list(x.Stmts)}}, false
	case *syntax.BinaryCmd:
		switch x.Op {
		case syntax.AndStmt:
			left, leftAlt := p.convert(x.X)
			right, rightAlt := p.convert(x.Y)
			if leftAlt {
				left = append(left, Unit{UnknownDir: true})
			}
			return append(left, right...), leftAlt || rightAlt
		case syntax.OrStmt:
			p.unknown = true
			left, leftAlt := p.convert(x.X)
			return left, leftAlt || p.mayChangeDir(x.Y)
		case syntax.Pipe, syntax.PipeAll:
			var stages []Unit
			flattenPipe(x, func(stage *syntax.Stmt) {
				u, alt := p.convert(stage)
				stages = append(stages, Unit{Nested: []Step{{Units: u, Alt: alt}}, Isolated: true})
			})
			units = []Unit{{Stages: stages, Piped: true}}
			if p.mayChangeDir(s) {
				units = append(units, Unit{UnknownDir: true})
				return units, true
			}
			return units, false
		}
	}
	p.unknown = true
	return nil, p.mayChangeDir(s)
}

// mayChangeDir ignores child shells and function definitions, but calls to
// locally defined functions have unknown effects. Some shells run the last
// pipeline stage in the parent, so a stage's cd remains relevant.
func (p *stepParser) mayChangeDir(n syntax.Node) bool {
	changed := false
	syntax.Walk(n, func(node syntax.Node) bool {
		if changed {
			return false
		}
		switch x := node.(type) {
		case *syntax.Stmt:
			return !x.Background && !x.Coprocess
		case *syntax.Subshell, *syntax.FuncDecl, *syntax.CoprocClause, *syntax.CmdSubst:
			return false
		case *syntax.CallExpr:
			if len(x.Args) == 0 {
				return true
			}
			c := Unwrap(command(x))
			if !c.Chdir {
				switch c.Name() {
				case "cd", "pushd", "popd":
					changed = true
				default:
					changed = p.functions[c.Name()]
				}
			}
		}
		return true
	})
	return changed
}

func flattenPipe(b *syntax.BinaryCmd, collect func(*syntax.Stmt)) {
	if left, ok := b.X.Cmd.(*syntax.BinaryCmd); ok && (left.Op == syntax.Pipe || left.Op == syntax.PipeAll) {
		flattenPipe(left, collect)
	} else {
		collect(b.X)
	}
	collect(b.Y)
}
