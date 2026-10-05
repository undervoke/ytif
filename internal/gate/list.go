package gate

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/undervoke/ytif/internal/check"
)

// List prints managed checks and every finding, or only counts when summary
// is set. It fails when there is any finding, as a gate would.
func List(ctx context.Context, root string, sources []check.Source, config ConfigChecker, w, errw io.Writer, summary bool) int {
	s, err := NewSurvey(ctx, root, sources, config, errw)
	if err != nil {
		fmt.Fprintf(errw, "ytif: %v\n", err)
		return ExitUsage
	}
	defer s.Close()
	findings := s.Findings()

	if !summary {
		for _, e := range s.Result.Managed {
			fmt.Fprintf(w, "%-8s %-7s %s\n", "managed", e.Placement, e.Key())
		}
		for _, f := range findings {
			fmt.Fprintln(w, f)
		}
	}

	var runners []string
	for r := range s.Owner {
		runners = append(runners, r)
	}
	sort.Strings(runners)
	var disc []string
	for _, r := range runners {
		state := fmt.Sprint(len(s.Keys[r]))
		if s.Failed[r] != nil {
			state += " (incomplete)"
		}
		disc = append(disc, r+" "+state)
	}
	kinds := map[string]int{}
	for _, f := range findings {
		kinds[string(f.Kind)]++
	}
	var fk []string
	for k, n := range kinds {
		fk = append(fk, fmt.Sprintf("%s %d", k, n))
	}
	sort.Strings(fk)
	fmt.Fprintf(w, "discovered: %s\n", orNone(disc))
	fmt.Fprintf(w, "managed: %d\n", len(s.Result.Managed))
	fmt.Fprintf(w, "findings: %s\n", orNone(fk))
	if len(findings) > 0 {
		return ExitFail
	}
	return ExitPass
}

func orNone(parts []string) string {
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}
