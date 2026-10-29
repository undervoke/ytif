package gate

import (
	"context"
	"fmt"
	"slices"

	"github.com/undervoke/ytif/internal/affected"
	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/targeting"
)

func requestedPlans(s *Survey, gate string, policy inventory.GateContext) map[check.Key]*check.Plan {
	plans := map[check.Key]*check.Plan{}
	for _, e := range s.Inventory.Verifications {
		if !e.Member(gate) {
			continue
		}
		requested, origin := policy.Resolve(e, gate)
		p := &check.Plan{Key: e.Key().String(), Requested: requested, Resolved: requested, Origin: origin, Run: true, Offered: "full-domain"}
		plans[e.Key()] = p
		if len(s.Admission) > 0 {
			p.Run, p.Resolved, p.Reason = false, "", "admission-rejected"
			continue
		}
		if requested == check.ScopeFull {
			continue
		}
		if e.Runner == inventory.CommandRunner {
			if requested != check.ScopeFile {
				fullPlan(p, "command-affected-domain-unmapped")
			} else if r := targeting.ESLint(s.Repo.Root, e, nil); !r.Supported {
				fullPlan(p, r.Reason)
			}
			continue
		}
		if !slices.Contains(s.Repo.Tracked, "nx.json") {
			fullPlan(p, "affected-unavailable: no nx.json")
		} else if requested == check.ScopeFile {
			p.Resolved, p.Reason = check.ScopeAffected, "file-targeting-unavailable: using Nx unit selection"
		}
	}
	return plans
}

func needsComparison(plans map[check.Key]*check.Plan) bool {
	for _, p := range plans {
		if p.Run && p.Resolved != check.ScopeFull {
			return true
		}
	}
	return false
}

func fullPlan(p *check.Plan, reason string) {
	p.Resolved, p.Run, p.Reason = check.ScopeFull, true, reason
	p.Offered, p.Targets, p.Changed, p.Argv = "full-domain", 0, nil, nil
}

func resolvePlans(ctx context.Context, s *Survey, in *check.Input, snap gitx.Snapshot) {
	var graph *affected.Graph
	graphReason, graphRead := "", false
	for _, e := range s.Inventory.Verifications {
		p := in.Plans[e.Key()]
		if p == nil || !p.Run {
			continue
		}
		if p.Resolved != check.ScopeFull {
			switch {
			case snap.RevisionMismatch:
				fullPlan(p, "revision-mismatch")
			case snap.Fallback != "":
				fullPlan(p, snap.Fallback)
			case snap.Comparison == nil && !(in.Gate == check.GatePush && len(snap.Tips) == 0):
				fullPlan(p, "comparison-unavailable")
			case e.Runner == inventory.CommandRunner:
				r := targeting.ESLint(s.Repo.Root, e, snap.Changed)
				if !r.Supported {
					fullPlan(p, r.Reason)
				} else {
					p.Argv, p.Changed, p.Targets, p.Offered = r.Args, append([]string{}, snap.Changed...), len(r.Targets), "native-targets"
					if len(r.Targets) == 0 {
						p.Run, p.Reason = false, "empty-targets"
					}
				}
			default:
				if !graphRead {
					var invs []check.Invocation
					var err error
					graph, graphReason, invs, err = affected.NxFiles(ctx, s.Repo, snap.Changed)
					s.Listing = append(s.Listing, invs...)
					if err != nil {
						graphReason = err.Error()
					}
					graphRead = true
				}
				if graph == nil {
					fullPlan(p, "affected-unavailable: "+graphReason)
				} else if _, found := graph.Project(e.Key().Unit); !found {
					fullPlan(p, "affected-domain-unmapped")
				} else {
					p.Offered, p.Targets = "nx-units", 1
					p.Changed = append([]string{}, snap.Changed...)
					if graph.Unaffected(e.Key().Unit) {
						p.Run, p.Reason, p.Targets = false, "unaffected", 0
					}
				}
			}
		}
		// Offered names the actual inputs a legacy runner receives: the gate
		// file list for full runs. Narrowed runs keep the targeting the
		// resolver set above (native-targets or nx-units); Changed stays the
		// comparison context, not a proof of file targeting.
		if p.Run && (e.Runner == "node-verify" || e.Runner == "go-verify") && p.Resolved == check.ScopeFull {
			p.Offered = "legacy-files:" + in.Scope
			p.Targets = len(in.Files)
		}
	}
}

func selectedPlans(s *Survey, plans map[check.Key]*check.Plan) []check.Key {
	var selected []check.Key
	for _, e := range s.Inventory.Verifications {
		if p := plans[e.Key()]; p != nil && p.Run {
			selected = append(selected, e.Key())
		}
	}
	return selected
}

func fullCensus(plans map[check.Key]*check.Plan, defaultScope check.TargetScope) bool {
	if len(plans) == 0 {
		return defaultScope == check.ScopeFull
	}
	for _, p := range plans {
		if !p.Run || p.Resolved != check.ScopeFull {
			return false
		}
	}
	return true
}

func restorePlans(selected []check.Key, plans map[check.Key]*check.Plan, co map[check.Key][]check.Key) {
	for _, k := range selected {
		for _, partner := range co[k] {
			if p := plans[partner]; p != nil && !p.Run {
				fullPlan(p, "co-run:"+k.String())
				p.Expansion = "co-run-full-domain"
			}
		}
	}
}

func printPlans(o Options, s *Survey, in check.Input) {
	for _, e := range s.Inventory.Verifications {
		p := in.Plans[e.Key()]
		if p == nil {
			continue
		}
		decision := "run"
		if !p.Run {
			decision = "skip"
		}
		fmt.Fprintf(o.Stdout, "  %s · %s → %s · %s", p.Key, p.Requested, p.Resolved, decision)
		if p.Run && p.Offered != "full-domain" {
			fmt.Fprintf(o.Stdout, " · %s (%d offered)", p.Offered, p.Targets)
		}
		if p.Reason != "" {
			fmt.Fprintf(o.Stdout, " · %s", p.Reason)
		}
		fmt.Fprintln(o.Stdout)
	}
}
