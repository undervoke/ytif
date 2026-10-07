package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/reconcile"
)

// ListOptions configures List.
type ListOptions struct {
	Summary bool
	// JSON prints the managed checks, discovery counts, and findings as
	// one JSON document instead of the human-readable list.
	JSON bool
	// Runners limits discovery and reconciliation to these runners; nil
	// means all runners.
	Runners []string
}

// List prints managed checks and every finding, only counts when summary
// is set, or one JSON document when JSON is set. It fails when there is
// any finding, as a gate would.
func List(ctx context.Context, root string, sources []check.Source, config ConfigChecker, w, errw io.Writer, opts ListOptions) int {
	var only map[string]bool
	if opts.Runners != nil {
		only = map[string]bool{}
		for _, r := range opts.Runners {
			only[r] = true
		}
	}
	s, err := NewSurvey(ctx, root, sources, config, errw, only)
	if err != nil {
		fmt.Fprintf(errw, "ytif: %v\n", err)
		return ExitUsage
	}
	defer s.Close()
	findings := s.Findings()

	display := displayRunners(s, opts.Runners)
	if opts.JSON {
		return listJSON(w, s, findings, display)
	}

	if !opts.Summary {
		for _, e := range s.Result.Managed {
			fmt.Fprintf(w, "%-8s %-7s %s\n", "managed", e.Placement, e.Key())
		}
		for _, f := range findings {
			fmt.Fprintln(w, f)
		}
	}

	fmt.Fprintln(w, discoveryLine(s, display))
	fmt.Fprintf(w, "managed: %d\n", len(s.Result.Managed))
	fmt.Fprintf(w, "findings: %s\n", findingsLine(findings))
	if len(findings) > 0 {
		return ExitFail
	}
	return ExitPass
}

// displayRunners lists the runners the list reports: the scope when one
// is set, else every owned runner. A scoped source kept for one runner
// still owns the rest, which the scope hides.
func displayRunners(s *Survey, scope []string) []string {
	if scope != nil {
		display := append([]string(nil), scope...)
		sort.Strings(display)
		return display
	}
	var runners []string
	for r := range s.Owner {
		runners = append(runners, r)
	}
	sort.Strings(runners)
	return runners
}

// discoveryLine summarizes per-runner discovery counts.
func discoveryLine(s *Survey, runners []string) string {
	var disc []string
	for _, r := range runners {
		state := fmt.Sprint(len(s.Keys[r]))
		if s.Failed[r] != nil {
			state += " (incomplete)"
		}
		disc = append(disc, r+" "+state)
	}
	return "discovered: " + orNone(disc)
}

// findingsLine summarizes finding counts by kind.
func findingsLine(findings []reconcile.Finding) string {
	kinds := map[string]int{}
	for _, f := range findings {
		kinds[string(f.Kind)]++
	}
	var fk []string
	for k, n := range kinds {
		fk = append(fk, fmt.Sprintf("%s %d", k, n))
	}
	sort.Strings(fk)
	return orNone(fk)
}

// listJSON writes the machine-readable list: the managed checks with their
// contracts, the per-runner discovery counts, and the structured findings.
func listJSON(w io.Writer, s *Survey, findings []reconcile.Finding, runners []string) int {
	discovered := map[string]discoveryJSON{}
	for _, r := range runners {
		discovered[r] = discoveryJSON{Checks: len(s.Keys[r]), Incomplete: s.Failed[r] != nil}
	}
	managed := s.Result.Managed
	if managed == nil {
		managed = []inventory.Entry{}
	}
	structured := make([]findingJSON, 0, len(findings))
	for _, f := range findings {
		structured = append(structured, findingJSON{
			Kind:     string(f.Kind),
			Key:      keyString(f.Key),
			Runner:   f.Runner,
			Location: f.Location,
			Detail:   f.Detail,
		})
	}
	doc := listDocument{Managed: managed, Discovered: discovered, Findings: structured}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fmt.Fprintf(w, "ytif: %v\n", err)
		return ExitUsage
	}
	fmt.Fprintln(w, string(data))
	if len(findings) > 0 {
		return ExitFail
	}
	return ExitPass
}

// listDocument is the --json document.
type listDocument struct {
	Managed    []inventory.Entry        `json:"managed"`
	Discovered map[string]discoveryJSON `json:"discovered"`
	Findings   []findingJSON            `json:"findings"`
}

// discoveryJSON is one runner's discovery count.
type discoveryJSON struct {
	Checks     int  `json:"checks"`
	Incomplete bool `json:"incomplete"`
}

// findingJSON is one structured finding.
type findingJSON struct {
	Kind     string `json:"kind"`
	Key      string `json:"key,omitempty"`
	Runner   string `json:"runner,omitempty"`
	Location string `json:"location,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// keyString renders k, or "" for the zero key findings leave unset.
func keyString(k check.Key) string {
	if k == (check.Key{}) {
		return ""
	}
	return k.String()
}

func orNone(parts []string) string {
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}
