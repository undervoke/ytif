package checkrun

import (
	"regexp"
	"strings"

	"github.com/undervoke/ytif/internal/gateconf"
	"github.com/undervoke/ytif/internal/guard"
	"github.com/undervoke/ytif/internal/shellcmd"
	"mvdan.cc/sh/v3/syntax"
)

var taskWork = regexp.MustCompile(`(?i)(^|[^a-z0-9])(test|build)([^a-z0-9]|$)`)

// inspector reads declared shell/package/Nx commands, never executing their
// resolvers. The shared budget also bounds package-to-Nx-to-package cycles.
type inspector struct {
	nx     *nxInspector
	visits int
}

func newInspector(root string) *inspector {
	i := &inspector{}
	i.nx = newNxInspector(root, i.script)
	return i
}

func (i *inspector) script(text, dir string) string {
	if i.visits >= 256 {
		return ""
	}
	i.visits++
	return guard.Inspect(text, dir, func(c shellcmd.Command, dir string) string {
		// The check lane must not grant the user-only allowance, enter a
		// gate through a side door, or recursively execute its own list.
		switch r := gateconf.RailCommand(c); r {
		case "allow", "commit", "push", "ci", "check":
			return "ytif " + r
		}
		if r := gateconf.TestRunner(c); r != "" {
			return r
		}
		if r := gateconf.BuildCommand(c); r != "" {
			return r
		}
		if r := typescriptBuild(c, dir); r != "" {
			return r
		}
		for _, t := range gateconf.TaskNames(c) {
			if taskWork.MatchString(t) {
				return c.NameBase() + " " + t
			}
		}
		if c.NameBase() == "nx" && dir != "" && !c.Chdir {
			return i.nx.inspect(c, dir)
		}
		return ""
	})
}

func commandText(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i], _ = syntax.Quote(a, syntax.LangBash)
	}
	return strings.Join(quoted, " ")
}
