// Package guard refuses agent shell commands whose recorded duration predicts
// they exceed the ledger's guard limit, so heavy verifications run only when
// git hooks and CI start the gates, which own their scope and cost. It prices
// ytif gates, registered commands, test runners, package scripts, and
// Nx targets by local record elapsed time; unknown stays allowed. While the
// user allows it (ytif allow), duration refusals lift; an agent's own ytif
// allow is always refused, so the permission stays the user's.
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
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"

	"github.com/undervoke/ytif/internal/gateconf"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/record"
	"github.com/undervoke/ytif/internal/shellcmd"
)

const (
	durationRefusal = "(YTiF) This takes too long to run directly. Run a narrower verification for your changes. The gates cover the rest on commit, push, and CI, so do not rerun this in another form. Report it as left to the gates."
	allowRefusal    = "(YTiF) Only the user can lift this limit. Run a narrower verification for your changes."
)

// Run handles one hook invocation. It always succeeds; a refusal is the
// decision it prints.
func Run(stdin io.Reader, stdout, stderr io.Writer) {
	runHook(stdin, stdout, stderr, false)
}

// RunCursor handles Cursor's beforeShellExecution payload and response.
func RunCursor(stdin io.Reader, stdout, stderr io.Writer) {
	runHook(stdin, stdout, stderr, true)
}

func runHook(stdin io.Reader, stdout, stderr io.Writer, cursor bool) {
	var payload struct {
		Cwd       string          `json:"cwd"`
		Command   json.RawMessage `json:"command"`
		ToolInput struct {
			Command json.RawMessage `json:"command"`
		} `json:"tool_input"`
	}
	decoded := json.NewDecoder(stdin).Decode(&payload) == nil
	raw := payload.ToolInput.Command
	if cursor {
		raw = payload.Command
	}
	command, ok := commandText(raw)
	if !decoded || !ok {
		if cursor {
			_ = json.NewEncoder(stdout).Encode(map[string]string{"permission": "allow"})
		}
		return
	}
	g := &guard{cwd: payload.Cwd}
	runner, reason, basis, recCommon := g.refused(command)
	if runner == "" {
		if cursor {
			_ = json.NewEncoder(stdout).Encode(map[string]string{"permission": "allow"})
		}
		return
	}
	if cursor {
		_ = json.NewEncoder(stdout).Encode(map[string]string{"permission": "deny", "user_message": reason, "agent_message": reason})
	} else {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		}})
	}
	common := recCommon
	if common == "" {
		var err error
		common, err = g.commonDir()
		if err != nil {
			common = ""
		}
	}
	if common != "" {
		if err := recordRefusal(common, runner, command, basis, stderr); err != nil {
			fmt.Fprintf(stderr, "(YTiF) refusal not recorded: %v\n", err)
		}
	} else {
		fmt.Fprintf(stderr, "(YTiF) refusal not recorded: no repository\n")
	}
}

// guard judges one command from the hook's working directory.
type guard struct {
	cwd       string
	common    string
	commonErr error
	located   bool
}

func (g *guard) commonDir() (string, error) {
	if !g.located {
		g.common, g.commonErr = commonDir(g.cwd)
		g.located = true
	}
	return g.common, g.commonErr
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

// refused returns what command runs that the guard refuses, with the fixed
// model reason, the numeric record basis, and the common directory to record
// in, or "". An agent's ytif allow is refused wherever a traversed script
// reaches it, even while the user has lifted the limit. The grant is read
// only after an over-limit estimate, from the repository that exceeded it.
func (g *guard) refused(command string) (runner, reason, basis, recCommon string) {
	if runner := findAllow(command, g.dir()); runner != "" {
		return runner, allowRefusal, "", ""
	}
	steps, _, err := shellcmd.Steps(command)
	if err != nil && len(steps) == 0 {
		return "", "", "", ""
	}
	return g.durationRefused(steps)
}

// findAllow inspects all readable calls and package scripts, independently
// of which paths have enough evidence to price.
func findAllow(command, dir string) string {
	s := newScripts()
	s.inspect(command, dir, 0)
	if s.allow {
		return "ytif allow"
	}
	return ""
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

// recordRefusal appends a guard line to the repository's managed records,
// keeping the numeric estimate basis in the detail. The fixed model text
// stays out of the record.
func recordRefusal(commonDir, runner, command, basis string, warn io.Writer) error {
	if len(command) > 300 {
		command = command[:300] + "…"
	}
	detail := fmt.Sprintf("%s: %s", runner, command)
	if basis != "" {
		if len(basis) > 500 {
			basis = basis[:500] + "…"
		}
		detail += " | " + basis
	}
	return record.AppendManaged(commonDir, []record.Line{{
		Time: time.Now().UTC(), Attempt: record.NewAttempt(), Kind: record.KindGuard,
		Runner: path.Base(strings.Fields(runner)[0]), Detail: detail,
	}}, warn)
}
