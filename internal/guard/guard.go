// Package guard refuses agent shell commands that run checks — a test
// runner or a ytif gate — so checks run only when git hooks and CI start the
// gates, which own their scope and cost. While the user allows it (ytif
// allow), they pass; an agent's own ytif allow is always refused, so the
// permission stays the user's.
//
// It reads a PreToolUse hook payload ({"tool_input":{"command":...}}) and
// answers with the Claude Code and Codex deny decision. Anything it cannot
// read or parse is allowed: the gates, not the guard, are the authority. A
// refusal it cannot record is still refused, and the failure goes to stderr.
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
	runner, reason := refused(command)
	if runner == "" {
		return
	}
	common, err := commonDir(payload.Cwd)
	if reason == refusal && err == nil {
		if _, ok := allow.Until(common, time.Now()); ok {
			return
		}
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "deny",
		"permissionDecisionReason": reason,
	}})
	if err == nil {
		err = recordRefusal(common, runner, command)
	}
	if err != nil {
		fmt.Fprintf(stderr, "(YTiF) refusal not recorded: %v\n", err)
	}
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

// refused returns what command runs that the guard refuses, ytif allow, a
// ytif gate, or a test runner, with the reason, or "".
func refused(command string) (runner, reason string) {
	cmds, err := shellcmd.Parse(command)
	if err != nil && len(cmds) == 0 {
		return "", ""
	}
	for _, c := range cmds {
		if gateconf.RailCommand(c) == "allow" {
			return "ytif allow", allowRefusal
		}
	}
	for _, c := range cmds {
		switch g := gateconf.RailCommand(c); g {
		case check.GateCommit, check.GatePush, check.GateCI:
			return "ytif " + g, refusal
		}
		if r := gateconf.TestRunner(c); r != "" {
			return r, refusal
		}
	}
	return "", ""
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
