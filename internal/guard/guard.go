// Package guard refuses agent shell commands that run checks, so checks
// run only when git hooks and CI start the gates, which own their scope and
// cost. It refuses a ytif gate, a test runner, a *.verify.mjs module, a
// package script, Nx target, or Make goal named as a check, a formatter,
// linter, or type checker in its checking mode, and a package script whose
// commands do any of these. While the user allows it (ytif allow), they
// pass; an agent's own ytif allow is always refused, so the permission
// stays the user's.
//
// It reads a PreToolUse hook payload ({"cwd":...,"tool_input":{"command":...}})
// and answers with the Claude Code and Codex deny decision. Anything it
// cannot read or parse is allowed: the gates, not the guard, are the
// authority. A refusal it cannot record is still refused, and the failure
// goes to stderr.
package guard

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"

	"github.com/undervoke/ytif/internal/allow"
	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gateconf"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/record"
	"github.com/undervoke/ytif/internal/shellcmd"
)

const (
	refusal      = "(YTiF) Unauthorized verification executions are prohibited."
	allowRefusal = "(YTiF) Only the user can allow verification executions."
)

// Run handles one hook invocation. It always succeeds; a refusal is the
// decision it prints.
func Run(stdin io.Reader, stdout, stderr io.Writer) {
	var payload struct {
		Cwd       string `json:"cwd"`
		ToolInput struct {
			Command json.RawMessage `json:"command"`
		} `json:"tool_input"`
	}
	if json.NewDecoder(stdin).Decode(&payload) != nil {
		return
	}
	command, ok := commandText(payload.ToolInput.Command)
	if !ok {
		return
	}
	g := &guard{cwd: payload.Cwd}
	runner, reason := g.refused(command)
	if runner == "" {
		return
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "deny",
		"permissionDecisionReason": reason,
	}})
	common, err := g.commonDir()
	if err == nil {
		err = recordRefusal(common, runner, command)
	}
	if err != nil {
		fmt.Fprintf(stderr, "(YTiF) refusal not recorded: %v\n", err)
	}
}

// guard judges one command from the hook's working directory.
type guard struct {
	cwd       string
	common    string
	commonErr error
	located   bool
	allowed   *bool
}

func (g *guard) commonDir() (string, error) {
	if !g.located {
		g.common, g.commonErr = commonDir(g.cwd)
		g.located = true
	}
	return g.common, g.commonErr
}

// allowance reports whether the user currently allows checks.
func (g *guard) allowance() bool {
	if g.allowed == nil {
		ok := false
		if common, err := g.commonDir(); err == nil {
			_, ok = allow.Until(common, time.Now())
		}
		g.allowed = &ok
	}
	return *g.allowed
}

// commandText accepts a command string or an argv array.
func commandText(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, s != ""
	}
	var argv []string
	if json.Unmarshal(raw, &argv) != nil || len(argv) == 0 {
		return "", false
	}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		q, err := syntax.Quote(a, syntax.LangBash)
		if err != nil {
			return "", false
		}
		quoted[i] = q
	}
	return strings.Join(quoted, " "), true
}

// refused returns what command runs that the guard refuses, with the
// reason, or "". An agent's ytif allow is refused wherever the command or
// a package script it runs reaches it, even while the user allows checks.
// A check the command names is refused without reading the scripts it
// runs; otherwise they are read for checks.
func (g *guard) refused(command string) (runner, reason string) {
	cmds, err := shellcmd.Parse(command)
	if err != nil && len(cmds) == 0 {
		return "", ""
	}
	s := newScripts(g.allowance)
	cmds = s.expand(cmds, g.dir())
	for _, c := range cmds {
		if gateconf.RailCommand(c) == "allow" {
			return "ytif allow", allowRefusal
		}
	}
	named := ""
	for _, c := range cmds {
		if named = checkRun(c); named != "" {
			break
		}
	}
	if named != "" && !g.allowance() {
		return named, refusal
	}
	s.walk(cmds, g.dir(), 0)
	switch {
	case s.allow:
		return "ytif allow", allowRefusal
	case named == "" && s.hit != "" && !g.allowance():
		return s.hit, refusal
	}
	return "", ""
}

// dir returns the directory the command starts in.
func (g *guard) dir() string {
	if g.cwd != "" {
		return g.cwd
	}
	wd, _ := os.Getwd()
	return wd
}

// expand adds to cmds the commands each runs on its behalf, through npx,
// a package manager's exec, or a package binary shorthand, recursively. A
// command a wrapper runs in another directory is marked so. A shorthand
// whose package, found from dir ("" when unknown), has a script of that
// name runs the script, which walk reads, rather than the binary.
func (s *scripts) expand(cmds []shellcmd.Command, dir string) []shellcmd.Command {
	if changesDir(cmds) {
		dir = ""
	}
	var out []shellcmd.Command
	var add func(c shellcmd.Command, depth int)
	add = func(c shellcmd.Command, depth int) {
		out = append(out, c)
		if depth >= maxWrapDepth {
			return
		}
		wrapped, shorthand := gateconf.Wrapped(c)
		if shorthand && s.hasScript(c, dir) {
			return
		}
		for _, w := range wrapped {
			w.Chdir = w.Chdir || c.Chdir || setsDir(c)
			add(w, depth+1)
		}
	}
	for _, c := range cmds {
		add(c, 0)
	}
	return out
}

const maxWrapDepth = 8

// setsDir reports whether a package manager command names a directory to
// run in.
func setsDir(c shellcmd.Command) bool {
	for _, a := range c.Args[1:] {
		flag, _, _ := strings.Cut(a, "=")
		switch flag {
		case "-C", "--dir", "--prefix", "--cwd":
			return true
		}
	}
	return false
}

// checkRun returns the check c runs, or "": a ytif gate, a test runner, a
// verification module, a task named as a check, or a checking mode.
func checkRun(c shellcmd.Command) string {
	switch g := gateconf.RailCommand(c); g {
	case check.GateCommit, check.GatePush, check.GateCI:
		return "ytif " + g
	}
	if r := gateconf.TestRunner(c); r != "" {
		return r
	}
	if m := gateconf.VerifyModule(c); m != "" {
		return m
	}
	for _, t := range gateconf.TaskNames(c) {
		if checkName.MatchString(t) {
			return c.NameBase() + " " + t
		}
	}
	return gateconf.CheckMode(c)
}

// checkName matches a task name with test, check, or verify as a word of
// its own, set off by the ends of the name or by characters other than
// letters and digits: test:fast, maps:check, verify-runtime-config, but not
// typecheck or latest.
var checkName = regexp.MustCompile(`(?i)(^|[^a-z0-9])(test|check|verify)([^a-z0-9]|$)`)

func commonDir(cwd string) (string, error) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	root, err := gitx.Root(cwd)
	if err != nil {
		return "", err
	}
	return gitx.CommonDir(root)
}

// recordRefusal appends a guard line to the repository's local records.
func recordRefusal(commonDir, runner, command string) error {
	if len(command) > 500 {
		command = command[:500] + "…"
	}
	return record.Append(record.DefaultPath(commonDir), []record.Line{{
		Time: time.Now().UTC(), Attempt: record.NewAttempt(), Kind: record.KindGuard, Context: "agent",
		Runner: path.Base(strings.Fields(runner)[0]), Detail: fmt.Sprintf("%s: %s", runner, command),
	}})
}
