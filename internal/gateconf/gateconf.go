// Package gateconf reconciles gate configuration — lefthook hooks and GitHub
// Actions workflows — with the rail: repository code runs only through
// ytif, and every entry that runs only external code is classified in the
// routing list. Entries that cannot be read or classified fail, routed or
// not: routing classifies external code and never vouches for what cannot
// be read.
package gateconf

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/reconcile"
	"github.com/undervoke/ytif/internal/shellcmd"
)

// Checker implements gate.ConfigChecker.
type Checker struct{}

// entry is one gate step: a shell script, an action reference, or a
// construct that runs repository code by definition.
type entry struct {
	surface  string
	location string
	run      string // shell script, as the shell receives it
	route    string // the text routing entries match; run when empty
	shell    string // workflow shell; "" means the default
	scope    scope  // where the script runs
	uses     string // GitHub Actions reference
	job      bool   // uses names a reusable workflow, not a step action
	repoCode string // set for constructs such as lefthook scripts
}

func (Checker) Check(ctx context.Context, repo check.Repo, routing inventory.Routing) []reconcile.Finding {
	var entries []entry
	var findings []reconcile.Finding
	for _, src := range []func(check.Repo) ([]entry, []reconcile.Finding){lefthookEntries, workflowEntries} {
		e, f := src(repo)
		entries = append(entries, e...)
		findings = append(findings, f...)
	}
	c := newClassifier(repo.Root, repo.Tracked)
	for _, e := range entries {
		if detail := c.check(e, routing); detail != "" {
			findings = append(findings, reconcile.Finding{Kind: reconcile.GateConfig, Location: e.location, Detail: detail})
		}
	}
	return findings
}

// check returns why an entry is not acceptable, or "".
func (c *classifier) check(e entry, routing inventory.Routing) string {
	if e.repoCode != "" {
		return e.repoCode + "; call ytif instead"
	}
	if e.uses != "" {
		return c.checkUses(e, routing)
	}
	if !shellSupported(e.shell) {
		return fmt.Sprintf("shell %q cannot be read; use bash", e.shell)
	}
	if strings.Contains(e.run, "${{") {
		return "a ${{ }} expression is substituted into the script text before the shell reads it, so the script cannot be classified; pass the value through env and use the variable"
	}
	cmds, err := shellcmd.Parse(e.run)
	if err != nil {
		return "cannot parse the script: " + err.Error()
	}
	sc := within(cmds, e.scope)
	var unknowns []string
	sawExternal := false
	for _, cmd := range cmds {
		cls, why := c.classify(cmd, sc)
		switch cls {
		case repoCode:
			return fmt.Sprintf("%s (%s); call ytif instead", why, cmd.Text)
		case unknown:
			unknowns = append(unknowns, fmt.Sprintf("%s (%s)", why, cmd.Text))
		case external:
			sawExternal = true
		}
	}
	switch {
	case len(unknowns) > 0:
		return "cannot classify " + strings.Join(unknowns, "; ")
	case !sawExternal || routed(routing, e):
		return ""
	}
	return "runs external commands not listed in " + inventory.RoutingFile + ": " + oneLine(e.routeText())
}

func (e entry) routeText() string {
	if e.route != "" {
		return e.route
	}
	return e.run
}

func (c *classifier) checkUses(e entry, routing inventory.Routing) string {
	ref := e.uses
	if strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../") {
		if e.job {
			return "" // a local reusable workflow is reconciled as its own file
		}
		return fmt.Sprintf("runs the local action %s; call ytif from a run step instead", ref)
	}
	if routed(routing, e) {
		return ""
	}
	return fmt.Sprintf("uses %s, which is not listed in %s", usesKey(ref), inventory.RoutingFile)
}

// routed reports whether the routing list classifies the entry: a run entry
// by its exact text, a uses entry by owner/repo without the ref.
func routed(routing inventory.Routing, e entry) bool {
	for _, r := range routing.Entries {
		if r.Surface != e.surface {
			continue
		}
		if e.uses != "" && r.Uses != "" && r.Uses == usesKey(e.uses) {
			return true
		}
		if e.uses == "" && r.Run != "" && strings.TrimSpace(r.Run) == strings.TrimSpace(e.routeText()) {
			return true
		}
	}
	return false
}

// usesKey reduces an action reference to owner/repo, or a docker reference
// to its image without tag or digest.
func usesKey(ref string) string {
	if img, ok := strings.CutPrefix(ref, "docker://"); ok {
		img, _, _ = strings.Cut(img, "@")
		if i := strings.LastIndex(img, ":"); i > strings.LastIndex(img, "/") {
			img = img[:i]
		}
		return "docker://" + img
	}
	ref, _, _ = strings.Cut(ref, "@")
	parts := strings.SplitN(ref, "/", 3)
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return ref
}

// shellSupported reports whether a workflow shell is one shellcmd reads; the
// runner's default shell is bash.
func shellSupported(shell string) bool {
	fields := strings.Fields(shell)
	return len(fields) == 0 || fields[0] == "bash" || fields[0] == "sh"
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func isYAML(p string) bool {
	ext := path.Ext(p)
	return ext == ".yml" || ext == ".yaml"
}
