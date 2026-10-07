package record

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func line(key, outcome string, elapsed *int64, at time.Time) Line {
	return Line{
		Time: at, Attempt: "a", Kind: KindResult, Gate: "ci", Context: "local",
		Key: key, Runner: "go-test", Unit: "example.com/mod/pkg", Outcome: outcome,
		ElapsedMS: elapsed,
	}
}

func ms(v int64) *int64 { return &v }

// Provenance fields use snake_case JSON and stay backward compatible: old
// records without them decode with empty provenance, which the board shows
// as unknown/historical.
func TestLineProvenanceRoundTrip(t *testing.T) {
	l := Line{
		Time: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), Attempt: "a",
		Kind: KindResult, Gate: "ci", Key: "go-test:u:TestX",
		Outcome: OutcomePass, Commit: "abc123", Worktree: "wide-projects", Profile: "full",
	}
	raw, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"commit":"abc123"`, `"worktree":"wide-projects"`, `"profile":"full"`} {
		if !strings.Contains(string(raw), k) {
			t.Errorf("marshaled line lacks %s: %s", k, raw)
		}
	}
	var back Line
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Commit != "abc123" || back.Worktree != "wide-projects" || back.Profile != "full" {
		t.Errorf("provenance did not round-trip: %+v", back)
	}

	var old Line
	if err := json.Unmarshal([]byte(`{"time":"2026-10-07T12:00:00Z","kind":"result","key":"go-test:u:TestX","outcome":"pass"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Commit != "" || old.Worktree != "" || old.Profile != "" {
		t.Errorf("old record should decode with empty provenance: %+v", old)
	}
}

// Hits count runs of consecutive fails that a pass or a cached pass ends.
// Blocked and skip neither start nor end one; a cached pass ends one like a
// pass but carries no fresh timing.
func TestStatsOutcomeSemantics(t *testing.T) {
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	at := func(i int) time.Time { return base.Add(time.Duration(i) * time.Minute) }
	lines := []Line{
		// A: fail, fail, pass → one hit.
		line("k:a", OutcomeFail, ms(10), at(0)),
		line("k:a", OutcomeFail, ms(10), at(1)),
		line("k:a", OutcomePass, ms(10), at(2)),
		// B: fail, blocked, fail → blocked changes nothing: one hit.
		line("k:b", OutcomeFail, ms(10), at(0)),
		line("k:b", OutcomeBlocked, nil, at(1)),
		line("k:b", OutcomeFail, ms(10), at(2)),
		// C: fail, skip, fail → skip changes nothing: one hit.
		line("k:c", OutcomeFail, ms(10), at(0)),
		line("k:c", OutcomeSkip, nil, at(1)),
		line("k:c", OutcomeFail, ms(10), at(2)),
		// D: fail, cached, fail → cached ends the run like a pass: two hits.
		line("k:d", OutcomeFail, ms(10), at(0)),
		line("k:d", OutcomeCached, nil, at(1)),
		line("k:d", OutcomeFail, ms(10), at(2)),
		// E: skip only → a run with no verdict and no hit.
		line("k:e", OutcomeSkip, nil, at(0)),
		// F: cached only → a run with no fresh timing.
		line("k:f", OutcomeCached, nil, at(0)),
	}
	checks, _, _ := Stats(lines, Filter{}, 0)
	got := map[string]CheckStat{}
	for _, c := range checks {
		got[c.Key] = c
	}
	wantHits := map[string]int{"k:a": 1, "k:b": 1, "k:c": 1, "k:d": 2, "k:e": 0, "k:f": 0}
	for k, h := range wantHits {
		if got[k].Hits != h {
			t.Errorf("%s: hits = %d, want %d", k, got[k].Hits, h)
		}
	}
	if got["k:e"].Runs != 1 || got["k:f"].Runs != 1 {
		t.Errorf("skip/cached lines must count as runs: %+v %+v", got["k:e"], got["k:f"])
	}
	if got["k:f"].Timed != 0 || got["k:f"].AvgMS != nil {
		t.Errorf("cached outcome without elapsed_ms must not time the average: %+v", got["k:f"])
	}
	if got["k:a"].Timed != 3 {
		t.Errorf("k:a timed = %d, want 3", got["k:a"].Timed)
	}
}
