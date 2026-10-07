package gate

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/undervoke/ytif/internal/adapter"
	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/execution"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/reconcile"
)

// runProfile executes one gate through the execution adapter's profile. No
// native discovery runs, so a profile gate needs none of the native
// runners' SDKs: a Node-only hook runs without .NET, Go, or Bun. The
// adapter selects from the gate, scope, files, and inventory; the gate
// validates the selection against the inventory and placement, validates
// outcomes as a full gate does, and reconciles the selected keys and the
// gate configuration only.
func runProfile(ctx context.Context, root string, o Options) int {
	ex, err := execution.Load(root)
	if err != nil {
		fmt.Fprintf(o.Stderr, "ytif: %v\n", err)
		return ExitUsage
	}
	if ex == nil {
		fmt.Fprintf(o.Stderr, "ytif: --profile %q: no %s in %s\n", o.Profile, execution.File, root)
		return ExitUsage
	}
	prof, ok := ex.Profiles[o.Profile]
	if !ok {
		fmt.Fprintf(o.Stderr, "ytif: --profile %q: unknown profile (want %s)\n", o.Profile, strings.Join(sortedProfiles(ex), ", "))
		return ExitUsage
	}

	tracked, err := gitx.Tracked(root)
	if err != nil {
		fmt.Fprintf(o.Stderr, "ytif: %v\n", err)
		return ExitUsage
	}
	inv, err := inventory.LoadInventory(root)
	if err != nil {
		fmt.Fprintf(o.Stderr, "ytif: %v\n", err)
		return ExitUsage
	}
	routing, err := inventory.LoadRouting(root)
	if err != nil {
		fmt.Fprintf(o.Stderr, "ytif: %v\n", err)
		return ExitUsage
	}
	scratch, err := os.MkdirTemp("", "ytif-")
	if err != nil {
		fmt.Fprintf(o.Stderr, "ytif: %v\n", err)
		return ExitUsage
	}
	defer os.RemoveAll(scratch)

	in, err := bindInput(root, o, tracked)
	if err != nil {
		fmt.Fprintf(o.Stderr, "ytif: %v\n", err)
		return ExitUsage
	}

	repo := check.Repo{Root: root, Tracked: tracked, Scratch: scratch, Log: o.Stderr}
	src := adapter.New(ex.Adapter.Command, ex.Adapter.Runners, o.Profile)
	// RunSelected selects, preflights the selection's placement before
	// any side effects, then runs the approved selection.
	selected, rep, runErr := src.RunSelected(ctx, repo, in)

	fmt.Fprintf(o.Stdout, "ytif %s --profile %s (%s) · %d selected · %s %d files\n",
		o.Gate, o.Profile, prof.Description, len(selected), in.Scope, len(in.Files))

	// Validate outcomes as a full gate does, with every runner the
	// profile may report owned by the adapter.
	s := &Survey{
		Repo: repo, Inventory: inv, Routing: routing,
		Sources: []check.Source{src}, Owner: map[string]check.Source{},
	}
	for _, r := range src.Claimed() {
		s.Owner[r] = src
	}
	name := strings.Join(src.Runners(), ", ")
	allowed := allowedKeys(selected, coRuns(s))
	rep, rejected, problems := validate(rep, name, src, s, selected, allowed)
	unresolved := missing(selected, rep, rejected)
	if runErr != nil {
		explained := false
		for i := range unresolved {
			if unresolved[i].detail == noOutcome {
				unresolved[i].detail = runErr.Error()
				explained = true
			}
		}
		if !explained {
			problems = append(problems, fmt.Sprintf("%s: %v", name, runErr))
		}
	}
	run := outcome{reports: []check.Report{rep}, dispatch: unresolved, problems: problems}

	var findings []reconcile.Finding
	if o.Config != nil {
		findings = append(findings, o.Config.Check(ctx, repo, routing)...)
	}
	findings = append(findings, reconcile.SelectedFindings(inv, selected)...)
	reconcile.Sort(findings)
	return finish(ctx, o, root, in.Scope, run, findings)
}

func sortedProfiles(ex *execution.Execution) []string {
	var names []string
	for n := range ex.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return []string{"(no profiles)"}
	}
	return names
}
