// Package reconcile compares discovered checks with the inventory.
package reconcile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/inventory"
)

// Kind names one reason a gate cannot accept the repository's checks or
// gate configuration.
type Kind string

const (
	Unregistered     Kind = "unregistered"      // discovered, absent from the inventory
	Stale            Kind = "stale"             // in the inventory, absent from a complete discovery
	MissingContract  Kind = "missing-contract"  // inventoried without required assessments
	Duplicate        Kind = "duplicate"         // one key listed or discovered twice
	InvalidPlacement Kind = "invalid-placement" // placement is not commit, push, or ci
	PlacementOrder   Kind = "placement-order"   // a check runs another that is placed later
	UnknownRunner    Kind = "unknown-runner"    // inventory names a runner no source owns
	DiscoveryFailed  Kind = "discovery-failed"  // a runner's discovery is incomplete
	GateConfig       Kind = "gate-config"       // a gate entry bypasses the rail or is unclassified
)

// Finding is one reconciliation failure.
type Finding struct {
	Kind     Kind
	Key      check.Key // zero for discovery and gate-config findings
	Runner   string    // discovery findings
	Location string    // gate-config findings: file and entry
	Detail   string
}

func (f Finding) String() string {
	var b strings.Builder
	b.WriteString(string(f.Kind))
	switch {
	case f.Key != (check.Key{}):
		b.WriteString(" " + f.Key.String())
	case f.Runner != "":
		b.WriteString(" " + f.Runner)
	case f.Location != "":
		b.WriteString(" " + f.Location)
	}
	if f.Detail != "" {
		b.WriteString(": " + f.Detail)
	}
	return b.String()
}

// Result splits the inventory into managed entries and findings.
type Result struct {
	// Managed entries are both discovered and inventoried with a valid
	// placement; gates select from these.
	Managed  []inventory.Entry
	Findings []Finding
}

// Reconcile compares discovered keys with inventory entries. known lists the
// runners some source owns; failed holds runners whose discovery is
// incomplete, whose entries are never reported stale.
func Reconcile(inv inventory.Inventory, known map[string]bool, discovered map[string][]check.Key, failed map[string]error) Result {
	var res Result

	for runner, err := range failed {
		for _, e := range split(err) {
			res.Findings = append(res.Findings, Finding{Kind: DiscoveryFailed, Runner: runner, Detail: e.Error()})
		}
	}

	found := map[check.Key]bool{}
	for _, keys := range discovered {
		for _, k := range keys {
			if found[k] {
				res.Findings = append(res.Findings, Finding{Kind: Duplicate, Key: k, Detail: "discovered twice"})
			}
			found[k] = true
		}
	}

	listed := map[check.Key]bool{}
	for _, e := range inv.Checks {
		k := e.Key()
		if listed[k] {
			res.Findings = append(res.Findings, Finding{Kind: Duplicate, Key: k, Detail: "listed twice in the inventory"})
			continue
		}
		listed[k] = true
		if !known[e.Runner] {
			res.Findings = append(res.Findings, Finding{Kind: UnknownRunner, Key: k})
			continue
		}
		if missing := e.MissingContract(); len(missing) > 0 {
			res.Findings = append(res.Findings, Finding{Kind: MissingContract, Key: k, Detail: "empty " + strings.Join(missing, ", ")})
		}
		valid := check.Rank(e.Placement) > 0
		if !valid {
			res.Findings = append(res.Findings, Finding{Kind: InvalidPlacement, Key: k, Detail: fmt.Sprintf("%q is not commit, push, or ci", e.Placement)})
		}
		switch {
		case found[k]:
			if valid {
				res.Managed = append(res.Managed, e)
			}
		case failed[e.Runner] == nil:
			res.Findings = append(res.Findings, Finding{Kind: Stale, Key: k})
		}
	}

	for k := range found {
		if !listed[k] {
			res.Findings = append(res.Findings, Finding{Kind: Unregistered, Key: k})
		}
	}

	Sort(res.Findings)
	sort.Slice(res.Managed, func(i, j int) bool { return res.Managed[i].Key().Less(res.Managed[j].Key()) })
	return res
}

// split reports each cause of a joined error as its own finding.
func split(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range j.Unwrap() {
			out = append(out, split(e)...)
		}
		return out
	}
	return []error{err}
}

// Sort orders findings by kind, then subject.
func Sort(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Key != b.Key {
			return a.Key.Less(b.Key)
		}
		if a.Runner != b.Runner {
			return a.Runner < b.Runner
		}
		if a.Location != b.Location {
			return a.Location < b.Location
		}
		return a.Detail < b.Detail
	})
}
