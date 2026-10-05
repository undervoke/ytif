// Package shellcmd lists the simple commands a shell script runs, seeing
// through wrappers and the scripts other commands run — sh -c, a shell's
// here-document, trap, and eval — so callers can classify what a gate entry
// or an agent command executes.
package shellcmd

import (
	"bytes"
	"fmt"
	"path"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Command is one simple command after wrappers are removed.
type Command struct {
	Args    []string // literal value of each word; "" where a word is not literal
	Literal []bool   // whether each word is fully literal
	// Bases holds, for a non-literal word such as $(go env GOPATH)/bin/ytif,
	// its literal last path element; "" when there is none.
	Bases []string
	Text  string // the command as written
	Stdin Stdin
	// Unresolved explains a script the command runs that cannot be read,
	// such as a non-literal sh -c script or a shell reading a pipe.
	Unresolved string
}

// Stdin is where a command reads standard input from, as far as the script
// says.
type Stdin struct {
	File   string // a literal file redirected with <
	Doc    string // the literal text of a here-document or here-string
	HasDoc bool
	Opaque bool // a pipe, or a redirection that is not literal
}

// NameBase returns the literal last path element of the command name.
func (c Command) NameBase() string {
	if len(c.Args) == 0 {
		return ""
	}
	if c.Literal[0] {
		return path.Base(c.Args[0])
	}
	return c.Bases[0]
}

// Name returns the command name, or "" when it is not literal.
func (c Command) Name() string {
	if len(c.Args) == 0 || !c.Literal[0] {
		return ""
	}
	return c.Args[0]
}

// Rest returns the command formed by the words from i on, such as the
// command a wrapper runs.
func (c Command) Rest(i int) Command {
	return Command{Args: c.Args[i:], Literal: c.Literal[i:], Bases: c.Bases[i:], Text: c.Text, Stdin: c.Stdin}
}

// Parse returns every simple command in script, including those in
// substitutions and functions, and those of the scripts sh -c, a shell's
// here-document, trap, and eval run. A parse error means the script cannot
// be classified.
func Parse(script string) ([]Command, error) {
	return parse(script, 0)
}

const maxDepth = 8

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true}

func parse(script string, depth int) ([]Command, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("nested shell scripts deeper than %d", maxDepth)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil {
		return nil, err
	}
	piped := map[*syntax.Stmt]bool{}
	var cmds []Command
	var nestedErr error
	syntax.Walk(f, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.BinaryCmd:
			if x.Op == syntax.Pipe || x.Op == syntax.PipeAll {
				markPiped(x.Y, piped)
			}
		case *syntax.Stmt:
			call, ok := x.Cmd.(*syntax.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			c := Unwrap(command(call))
			c.Stdin = stdin(x, piped[x])
			expanded, err := expand(c, depth)
			if err != nil && nestedErr == nil {
				nestedErr = err
			}
			cmds = append(cmds, expanded...)
		}
		return true
	})
	return cmds, nestedErr
}

// Expand returns c followed by the commands it runs itself: those of the
// script a shell, eval, or trap runs, and those find runs, recursively. A
// command whose script cannot be read carries the reason in Unresolved.
func Expand(c Command) ([]Command, error) {
	return expand(c, 0)
}

func expand(c Command, depth int) ([]Command, error) {
	inner, ok, why := nestedScript(c)
	if why != "" {
		c.Unresolved = why
	}
	cmds := []Command{c}
	var firstErr error
	for _, e := range embedded(c) {
		more, err := expand(e, depth)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		cmds = append(cmds, more...)
	}
	if ok {
		nested, err := parse(inner, depth+1)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", c.Text, err)
		}
		cmds = append(cmds, nested...)
	}
	return cmds, firstErr
}

// markPiped marks the statement that reads a pipe: the right side, or the
// first statement of a pipeline on the right side.
func markPiped(st *syntax.Stmt, piped map[*syntax.Stmt]bool) {
	piped[st] = true
	if b, ok := st.Cmd.(*syntax.BinaryCmd); ok && (b.Op == syntax.Pipe || b.Op == syntax.PipeAll) {
		markPiped(b.X, piped)
	}
}

func stdin(st *syntax.Stmt, piped bool) Stdin {
	in := Stdin{Opaque: piped}
	for _, r := range st.Redirs {
		if r.N != nil && r.N.Value != "0" {
			continue
		}
		var w *syntax.Word
		switch r.Op {
		case syntax.RdrIn:
			if v, ok := literal(r.Word); ok {
				in = Stdin{File: v}
			} else {
				in = Stdin{Opaque: true}
			}
			continue
		case syntax.Hdoc, syntax.DashHdoc:
			w = r.Hdoc
		case syntax.WordHdoc:
			w = r.Word
		default:
			continue
		}
		if v, ok := literal(w); ok {
			in = Stdin{Doc: v, HasDoc: true}
		} else {
			in = Stdin{Opaque: true}
		}
	}
	return in
}

func command(call *syntax.CallExpr) Command {
	var c Command
	for _, w := range call.Args {
		v, ok := literal(w)
		base := ""
		if !ok {
			if tail := literalTail(w.Parts[len(w.Parts)-1]); strings.Contains(tail, "/") {
				base = path.Base(tail)
			}
		}
		c.Args = append(c.Args, v)
		c.Literal = append(c.Literal, ok)
		c.Bases = append(c.Bases, base)
	}
	var buf bytes.Buffer
	if err := syntax.NewPrinter().Print(&buf, call); err == nil {
		c.Text = strings.TrimSpace(buf.String())
	}
	return c
}

// literalTail returns the literal text a word part ends with, looking inside
// double quotes: "$GOROOT/bin/go" ends with /bin/go.
func literalTail(part syntax.WordPart) string {
	switch p := part.(type) {
	case *syntax.Lit:
		return p.Value
	case *syntax.DblQuoted:
		if len(p.Parts) > 0 {
			return literalTail(p.Parts[len(p.Parts)-1])
		}
	}
	return ""
}

// literal resolves a word made only of literal and quoted literal parts.
func literal(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", true
	}
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", false
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// nestedScript returns the script a command runs as shell code: the -c
// script or here-document of a shell, a trap action, or eval's words. It
// returns why when such a script exists but cannot be read.
func nestedScript(c Command) (script string, ok bool, why string) {
	switch path.Base(c.Name()) {
	case "sh", "bash", "zsh", "dash", "ksh":
		return shellInput(c)
	case "eval":
		var words []string
		for i := 1; i < len(c.Args); i++ {
			if !c.Literal[i] {
				return "", false, "the eval script is not literal"
			}
			words = append(words, c.Args[i])
		}
		return strings.Join(words, " "), true, ""
	case "trap":
		return trapAction(c)
	}
	return "", false, ""
}

// shellInput returns the script of sh -c SCRIPT, or the standard input of a
// shell given no script file. A script file is not returned; callers
// classify it as a file.
func shellInput(c Command) (string, bool, string) {
	for i := 1; i < len(c.Args); i++ {
		if !c.Literal[i] {
			return "", false, "the shell argument is not literal"
		}
		a := c.Args[i]
		switch {
		case a == "-o" || a == "+o":
			i++ // shell option name
		case a == "--version" || a == "--help":
			return "", false, ""
		case a == "--" || (!strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+")):
			return "", false, "" // a script file
		case !strings.HasPrefix(a, "--") && strings.ContainsRune(a[1:], 'c'):
			if i+1 < len(c.Args) && c.Literal[i+1] {
				return c.Args[i+1], true, ""
			}
			return "", false, "the sh -c script is not literal"
		}
	}
	switch {
	case c.Stdin.HasDoc:
		return c.Stdin.Doc, true, ""
	case c.Stdin.File != "":
		return "", false, ""
	case c.Stdin.Opaque:
		return "", false, "the shell reads its script from a pipe or an unresolved redirection"
	}
	return "", false, "the shell reads its script from standard input"
}

// trapAction returns the action of trap ACTION SIGNAL..., which the shell
// runs later. Listing, resetting with -, and an empty action run nothing.
func trapAction(c Command) (string, bool, string) {
	args, lits := c.Args[1:], c.Literal[1:]
	if len(args) > 0 && lits[0] && args[0] == "--" {
		args, lits = args[1:], lits[1:]
	}
	if len(args) < 2 {
		return "", false, ""
	}
	if !lits[0] {
		return "", false, "the trap action is not literal"
	}
	if args[0] == "" || strings.HasPrefix(args[0], "-") {
		return "", false, ""
	}
	return args[0], true, ""
}

// embedded returns the commands find runs for each match with -exec,
// -execdir, -ok, and -okdir.
func embedded(c Command) []Command {
	if c.NameBase() != "find" {
		return nil
	}
	var out []Command
	for i := 1; i < len(c.Args); i++ {
		switch c.Args[i] {
		case "-exec", "-execdir", "-ok", "-okdir":
		default:
			continue
		}
		j := i + 1
		for j < len(c.Args) && !(c.Literal[j] && (c.Args[j] == ";" || c.Args[j] == "+")) {
			j++
		}
		if j > i+1 {
			inner := c.Rest(i + 1)
			n := j - i - 1
			inner.Args, inner.Literal, inner.Bases = inner.Args[:n], inner.Literal[:n], inner.Bases[:n]
			inner.Stdin = Stdin{}
			out = append(out, Unwrap(inner))
		}
		i = j
	}
	return out
}

// valueFlags lists wrapper options that consume the next word.
var valueFlags = map[string]map[string]bool{
	"env":     {"-u": true, "--unset": true, "-C": true, "--chdir": true},
	"nice":    {"-n": true, "--adjustment": true},
	"timeout": {"-s": true, "--signal": true, "-k": true, "--kill-after": true},
	"xargs": {"-n": true, "-I": true, "-J": true, "-L": true, "-P": true, "-R": true, "-S": true, "-d": true,
		"-s": true, "-E": true, "-a": true},
	"sudo": {"-u": true, "-g": true, "-C": true, "-D": true},
	"exec": {"-a": true},
}

// splitString reads the operand of env -S as the words of one command.
func splitString(s string) (Command, bool) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(s), "")
	if err != nil || len(f.Stmts) != 1 {
		return Command{}, false
	}
	call, ok := f.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return Command{}, false
	}
	return command(call), true
}

// Unwrap drops wrapper commands that run their arguments as a command:
// env (with assignments and -S), time, command, builtin, exec, nice, nohup,
// timeout, xargs, and sudo, however their path is spelled.
func Unwrap(c Command) Command {
next:
	for {
		name := ""
		if len(c.Args) > 0 && c.Literal[0] {
			name = path.Base(c.Args[0])
		}
		switch name {
		case "env", "time", "command", "builtin", "exec", "nice", "nohup", "timeout", "xargs", "sudo":
		default:
			return c
		}
		i := 1
		for i < len(c.Args) {
			a := c.Args[i]
			if !c.Literal[i] {
				break
			}
			if a == "--" {
				i++
				break
			}
			if strings.HasPrefix(a, "-") && len(a) > 1 {
				if name == "command" && (a == "-v" || a == "-V") {
					return Command{Text: c.Text} // a lookup, not an execution
				}
				if name == "env" && (strings.HasPrefix(a, "-S") || a == "--split-string" || strings.HasPrefix(a, "--split-string=")) {
					split, ok := envSplit(c, i)
					if !ok {
						return Command{Text: c.Text, Unresolved: "the env -S command cannot be read"}
					}
					c = split
					continue next
				}
				i++
				if valueFlags[name][a] {
					i++
				}
				continue
			}
			if name == "env" && strings.Contains(a, "=") {
				i++
				continue
			}
			if name == "timeout" {
				i++ // the duration
				name = ""
				continue
			}
			break
		}
		if i >= len(c.Args) {
			return Command{Text: c.Text}
		}
		c = c.Rest(i)
	}
}

// envSplit returns the command env -S runs, given the index of -S: its
// operand split into words, followed by the remaining arguments.
func envSplit(c Command, i int) (Command, bool) {
	a, rest := c.Args[i], i+1
	var v string
	switch {
	case strings.HasPrefix(a, "--split-string="):
		v = strings.TrimPrefix(a, "--split-string=")
	case a == "-S" || a == "--split-string":
		if rest >= len(c.Args) || !c.Literal[rest] {
			return Command{}, false
		}
		v, rest = c.Args[rest], rest+1
	default:
		v = a[len("-S"):] // -Sstring
	}
	split, ok := splitString(v)
	if !ok {
		return Command{}, false
	}
	tail := c.Rest(rest)
	split.Args = append(split.Args, tail.Args...)
	split.Literal = append(split.Literal, tail.Literal...)
	split.Bases = append(split.Bases, tail.Bases...)
	split.Text, split.Stdin = c.Text, c.Stdin
	return split, true
}
