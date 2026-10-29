// Gate scope and CI profiles: how much of each verification a gate runs.
package inventory

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/undervoke/ytif/internal/check"
)

// Target names the comparison branch.
type Target struct {
	Remote string `yaml:"remote,omitempty"`
	Branch string `yaml:"branch,omitempty"`
}

// CIProfile is one named CI policy. Scope inherits the common CI scope.
type CIProfile struct {
	Scope  *check.TargetScope `yaml:"scope,omitempty"`
	Target Target             `yaml:"target,omitempty"`
}

// CIScope is the common CI policy plus its named profiles.
type CIScope struct {
	Scope    check.TargetScope    `yaml:"scope"`
	Profiles map[string]CIProfile `yaml:"profiles,omitempty"`
}

// GateScope is the policy for check or commit.
type GateScope struct {
	Scope check.TargetScope `yaml:"scope"`
}

// PushScope is push policy: a local scope or a mapped CI profile.
type PushScope struct {
	Scope     *check.TargetScope `yaml:"scope,omitempty"`
	CiProfile string             `yaml:"ci-profile,omitempty"`
}

// GateConfig is the mandatory root gate. Fragments must not declare it.
type GateConfig struct {
	Check  GateScope `yaml:"check"`
	Commit GateScope `yaml:"commit"`
	Push   PushScope `yaml:"push"`
	CI     CIScope   `yaml:"ci"`
}

// GateContext is one run's gate-level policy. Entry overrides apply via Resolve.
type GateContext struct {
	Profile string
	Scope   check.TargetScope
	Target  Target

	from string // "profile" or "gate"
}

// requireGateScope rejects a missing or mistyped gate scope with the init
// hint shared by the check, commit and ci gates.
func requireGateScope(display, field string, s check.TargetScope) error {
	if s == "" {
		return fmt.Errorf("%s: %s: set scope to file, affected or full; run ytif init to fill missing gate defaults", display, field)
	}
	if !s.Valid() {
		return fmt.Errorf("%s: %s %q: want file, affected or full", display, field, string(s))
	}
	return nil
}

// sortedProfileNames lists CI profile names for unknown-profile diagnostics.
func (g GateConfig) sortedProfileNames() []string {
	names := make([]string, 0, len(g.CI.Profiles))
	for name := range g.CI.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// profileScope resolves a named CI profile onto the common CI scope:
// the profile scope wins when set, otherwise the gate scope applies.
func (g GateConfig) profileScope(name string) (scope check.TargetScope, from string, target Target) {
	p := g.CI.Profiles[name]
	scope, from = g.CI.Scope, "gate"
	if p.Scope != nil {
		scope, from = *p.Scope, "profile"
	}
	return scope, from, p.Target
}

func (g GateConfig) validate(display string) error {
	if err := requireGateScope(display, "gate.check.scope", g.Check.Scope); err != nil {
		return err
	}
	if err := requireGateScope(display, "gate.commit.scope", g.Commit.Scope); err != nil {
		return err
	}
	if err := requireGateScope(display, "gate.ci.scope", g.CI.Scope); err != nil {
		return err
	}
	hasScope := g.Push.Scope != nil
	hasProfile := g.Push.CiProfile != ""
	switch {
	case hasScope && hasProfile:
		return fmt.Errorf("%s: gate.push: set exactly one of scope or ci-profile, have both; a push mapped to a profile inherits that profile, so set scope.ci/<profile> instead of scope.push", display)
	case !hasScope && !hasProfile:
		return fmt.Errorf("%s: gate.push: set exactly one of scope or ci-profile; run ytif init to fill missing gate defaults", display)
	case hasScope && !g.Push.Scope.Valid():
		return fmt.Errorf("%s: gate.push.scope %q: want file, affected or full", display, string(*g.Push.Scope))
	}
	names := g.sortedProfileNames()
	for _, name := range names {
		p := g.CI.Profiles[name]
		if name == "" {
			return fmt.Errorf("%s: gate.ci.profiles: profile name is empty", display)
		}
		if p.Scope != nil && !p.Scope.Valid() {
			return fmt.Errorf("%s: gate.ci.profiles.%s.scope %q: want file, affected or full", display, name, string(*p.Scope))
		}
		if p.Target.Remote != strings.TrimSpace(p.Target.Remote) || p.Target.Branch != strings.TrimSpace(p.Target.Branch) {
			return fmt.Errorf("%s: gate.ci.profiles.%s.target: remove leading or trailing whitespace from remote and branch", display, name)
		}
		if strings.TrimSpace(p.Target.Remote) == "" || strings.TrimSpace(p.Target.Branch) == "" {
			if strings.TrimSpace(p.Target.Remote) != "" || strings.TrimSpace(p.Target.Branch) != "" {
				return fmt.Errorf("%s: gate.ci.profiles.%s.target: set remote and branch", display, name)
			}
		}
	}
	if hasProfile {
		if _, ok := g.CI.Profiles[g.Push.CiProfile]; !ok {
			if len(names) == 0 {
				return fmt.Errorf("%s: gate.push ci-profile %q: unknown profile; define it under gate.ci.profiles", display, g.Push.CiProfile)
			}
			return fmt.Errorf("%s: gate.push ci-profile %q: unknown profile, want one of %s", display, g.Push.CiProfile, strings.Join(names, ", "))
		}
	}
	return nil
}

// validateEntryScopes rejects scope keys and values that would silently miss.
func validateEntryScopes(entries []Entry, gate GateConfig, display string) error {
	profiles := map[string]bool{}
	for name := range gate.CI.Profiles {
		profiles[name] = true
	}
	mapped := gate.Push.CiProfile != ""
	for _, e := range entries {
		loc := e.Origin
		if loc == "" {
			loc = display
		}
		keys := make([]string, 0, len(e.Scope))
		for k := range e.Scope {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := e.Scope[k]
			if !v.Valid() {
				return fmt.Errorf("%s: scope.%s %q: want file, affected or full", loc, k, string(v))
			}
			switch k {
			case check.GateCheck, check.GateCommit, check.GatePush, check.GateCI:
			default:
				name, ok := strings.CutPrefix(k, check.GateCI+"/")
				if !ok || name == "" {
					return fmt.Errorf("%s: scope.%q: unknown scope, want check, commit, push, ci or ci/<profile>", loc, k)
				}
				if !profiles[name] {
					if len(profiles) == 0 {
						return fmt.Errorf("%s: scope.%q: unknown profile; define it under gate.ci.profiles", loc, k)
					}
					return fmt.Errorf("%s: scope.%q: unknown profile, want one of %s", loc, k, strings.Join(gate.sortedProfileNames(), ", "))
				}
			}
		}
		if mapped {
			if _, ok := e.Scope[check.GatePush]; ok {
				return fmt.Errorf("%s: scope.push conflicts with gate.push ci-profile %q; set scope.ci/%s or scope.ci instead", loc, gate.Push.CiProfile, gate.Push.CiProfile)
			}
		}
	}
	return nil
}

// Context resolves the gate-level policy. Only ci takes a profile.
func (g GateConfig) Context(gate, profile string) (GateContext, error) {
	switch gate {
	case check.GateCheck, check.GateCommit:
		if profile != "" {
			return GateContext{}, fmt.Errorf("gate %s does not take a profile; only ci takes --profile", gate)
		}
		scope := g.Check.Scope
		if gate == check.GateCommit {
			scope = g.Commit.Scope
		}
		return GateContext{Scope: scope, from: "gate"}, nil
	case check.GatePush:
		if profile != "" {
			return GateContext{}, fmt.Errorf("gate push does not take a profile; it runs its gate.push ci-profile mapping")
		}
		if g.Push.CiProfile != "" {
			scope, from, target := g.profileScope(g.Push.CiProfile)
			return GateContext{Profile: g.Push.CiProfile, Scope: scope, Target: target, from: from}, nil
		}
		return GateContext{Scope: *g.Push.Scope, from: "gate"}, nil
	case check.GateCI:
		if profile == "" {
			return GateContext{Scope: g.CI.Scope, from: "gate"}, nil
		}
		if _, ok := g.CI.Profiles[profile]; !ok {
			return GateContext{}, fmt.Errorf("ci profile %q: unknown profile", profile)
		}
		scope, from, target := g.profileScope(profile)
		return GateContext{Profile: profile, Scope: scope, Target: target, from: from}, nil
	}
	return GateContext{}, fmt.Errorf("unknown gate %q", gate)
}

// Resolve applies entry overrides: ci/<profile>, then ci, then the context.
func (c GateContext) Resolve(e Entry, gate string) (check.TargetScope, string) {
	if gate == check.GateCI || (gate == check.GatePush && c.Profile != "") {
		if c.Profile != "" {
			if s, ok := e.Scope[check.GateCI+"/"+c.Profile]; ok {
				return s, "entry"
			}
		}
		if s, ok := e.Scope[check.GateCI]; ok {
			return s, "entry"
		}
		return c.Scope, c.from
	}
	if s, ok := e.Scope[gate]; ok {
		return s, "entry"
	}
	return c.Scope, "gate"
}

// ValidateCandidate proves a composed ledger loads before init writes it.
func ValidateCandidate(root string, data []byte) (Inventory, error) {
	var raw Inventory
	if err := decodeDocument(bytes.NewReader(data), InventoryFile, &raw); err != nil {
		return Inventory{}, err
	}
	realRoot, err := filepath.EvalSymlinks(filepath.Join(root, InventoryFile))
	if err != nil && !os.IsNotExist(err) {
		return Inventory{}, fmt.Errorf("%s: %w", InventoryFile, err)
	}
	return composeInventory(root, raw, realRoot)
}
