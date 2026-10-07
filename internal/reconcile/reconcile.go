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
	UnknownTag       Kind = "unknown-tag"       // a tag no vocabulary declares
	TagCount         Kind = "tag-count"         // an exactly-one group tagged zero or several times
	DanglingRelation Kind = "dangling-relation" // requires or ensures names a check the inventory lacks
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

	inventoried := map[check.Key]bool{}
	for _, e := range inv.Checks {
		inventoried[e.Key()] = true
	}
	groups := inv.TagGroups()
	for _, e := range inv.Checks {
		res.Findings = append(res.Findings, entryFindings(e, groups, inventoried)...)
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

// SelectedFindings reconciles only selected keys against the inventory:
// a selected check the inventory lacks, an invalid placement, a missing
// contract, and tag or relation defects of the selected entries. Profile
// gates use it instead of a full reconciliation, which would drag in
// runners the profile never discovers.
func SelectedFindings(inv inventory.Inventory, selected []check.Key) []Finding {
	inventoried := map[check.Key]bool{}
	byKey := map[check.Key]inventory.Entry{}
	for _, e := range inv.Checks {
		inventoried[e.Key()] = true
		byKey[e.Key()] = e
	}
	groups := inv.TagGroups()
	var fs []Finding
	for _, k := range selected {
		e, ok := byKey[k]
		if !ok {
			fs = append(fs, Finding{Kind: Unregistered, Key: k})
			continue
		}
		if check.Rank(e.Placement) == 0 {
			fs = append(fs, Finding{Kind: InvalidPlacement, Key: k, Detail: fmt.Sprintf("%q is not commit, push, or ci", e.Placement)})
		}
		if missing := e.MissingContract(); len(missing) > 0 {
			fs = append(fs, Finding{Kind: MissingContract, Key: k, Detail: "empty " + strings.Join(missing, ", ")})
		}
		fs = append(fs, entryFindings(e, groups, inventoried)...)
	}
	Sort(fs)
	return fs
}

// entryFindings reports an entry's tag and relation defects.
func entryFindings(e inventory.Entry, groups map[string]inventory.Group, inventoried map[check.Key]bool) []Finding {
	var fs []Finding
	k := e.Key()
	unknown, counts := e.TagProblems(groups)
	if len(unknown) > 0 {
		fs = append(fs, Finding{Kind: UnknownTag, Key: k, Detail: strings.Join(unknown, ", ")})
	}
	for _, c := range counts {
		fs = append(fs, Finding{Kind: TagCount, Key: k, Detail: c})
	}
	for _, rel := range []struct {
		name string
		refs []inventory.Ref
	}{{"requires", e.Requires}, {"ensures", e.Ensures}} {
		for _, r := range rel.refs {
			if !inventoried[r.Key()] {
				fs = append(fs, Finding{Kind: DanglingRelation, Key: k, Detail: rel.name + " " + r.Key().String()})
			}
		}
	}
	return fs
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
