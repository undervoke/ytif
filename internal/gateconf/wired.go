package gateconf

import (
	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/shellcmd"
)

// Wiring is how the gate configuration starts the gates: the lefthook
// configuration lefthook reads ("" when none), whether its pre-commit and
// pre-push hooks run ytif commit and ytif push, and whether a workflow
// step runs ytif ci.
type Wiring struct {
	Lefthook string
	Commit   bool
	Push     bool
	CI       bool
}

// Wired reads the lefthook configuration and the workflows among
// repo.Tracked as the reconciliation does.
func Wired(repo check.Repo) Wiring {
	w := Wiring{Lefthook: lefthookFile(repo.Root)}
	hooks, _ := lefthookEntries(repo)
	flows, _ := workflowEntries(repo)
	for _, e := range append(hooks, flows...) {
		if e.run == "" {
			continue
		}
		cmds, err := shellcmd.Parse(e.run)
		if err != nil {
			continue
		}
		for _, c := range cmds {
			switch g := RailCommand(c); {
			case g == check.GateCommit && e.hook == "pre-commit":
				w.Commit = true
			case g == check.GatePush && e.hook == "pre-push":
				w.Push = true
			case g == check.GateCI && e.surface == inventory.SurfaceGitHubActions:
				w.CI = true
			}
		}
	}
	return w
}
