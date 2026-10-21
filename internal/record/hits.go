package record

import "sort"

// Hits counts each check's hits: runs of consecutive fail outcomes that a
// pass ends. Runs are followed per gate and key; blocked outcomes leave
// the fail-run state unchanged.
func Hits(lines []Line) map[string]int {
	ordered := append([]Line(nil), lines...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Time.Before(ordered[j].Time) })

	type lane struct{ gate, key string }
	failing := map[lane]bool{}
	hits := map[string]int{}
	for _, l := range ordered {
		if l.Kind != KindResult {
			continue
		}
		id := lane{l.Gate, l.Key}
		switch l.Outcome {
		case "fail":
			if !failing[id] {
				hits[l.Key]++
			}
			failing[id] = true
		case "pass":
			failing[id] = false
		}
	}
	return hits
}
