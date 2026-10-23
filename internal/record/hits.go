package record

import "sort"

// Hits counts each check's hits per gate: a fail counts as one hit, and the
// fails after it count again only after a pass. Runs are followed per gate and
// key; blocked outcomes leave the fail-run state unchanged. Lines are counted as given, so a caller that
// wants a narrower population filters first.
func Hits(lines []Line) map[string]map[string]int {
	ordered := append([]Line(nil), lines...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Time.Before(ordered[j].Time) })

	type lane struct{ gate, key string }
	failing := map[lane]bool{}
	hits := map[string]map[string]int{}
	for _, l := range ordered {
		if l.Kind != KindResult {
			continue
		}
		id := lane{l.Gate, l.Key}
		switch l.Outcome {
		case "fail":
			if !failing[id] {
				if hits[l.Key] == nil {
					hits[l.Key] = map[string]int{}
				}
				hits[l.Key][l.Gate]++
			}
			failing[id] = true
		case "pass":
			failing[id] = false
		}
	}
	return hits
}
