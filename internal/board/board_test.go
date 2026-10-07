package board

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/undervoke/ytif/internal/record"
)

const testInventory = `version: 2
checks:
- runner: go-test
  unit: example.com/mod/pkg
  name: TestAlpha
  placement: ci
  tags: [outage, user, mistake, visible, manual]
  accident: accidental test edit
  detection: runs the alpha path and observes its output so the edit fails it
  impact: wrong alpha shipped
  delete_when: TestAlpha is removed from the package
- runner: go-test
  unit: example.com/mod/pkg
  name: TestBeta
  placement: ci
  tags: [outage, user, mistake, visible, manual]
  accident: accidental test edit
  detection: runs the beta path and observes its output so the edit fails it
  impact: wrong beta shipped
  delete_when: TestBeta is removed from the package
- runner: go-test
  unit: example.com/mod/other
  name: TestGamma
  placement: push
  tags: [outage, user, mistake, visible, manual]
  accident: accidental test edit
  detection: runs the gamma path and observes its output so the edit fails it
  impact: wrong gamma shipped
  delete_when: TestGamma is removed from the package
- runner: go-test
  unit: example.com/mod/pkg
  name: TestDelta
  placement: ci
  tags: [outage, user, mistake, visible, manual]
  accident: accidental test edit
  detection: runs the delta path and observes its output so the edit fails it
  impact: wrong delta shipped
  delete_when: TestDelta is removed from the package
`

func renderData(t *testing.T, lines []record.Line) boardData {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ytif-inventory.yaml"), []byte(testInventory), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Render(Assets, root, lines, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	const open = `<script id="data" type="application/json">`
	i := strings.Index(string(out), open)
	if i < 0 {
		t.Fatal("rendered page has no embedded data")
	}
	rest := string(out)[i+len(open):]
	j := strings.Index(rest, `</script>`)
	if j < 0 {
		t.Fatal("rendered page data never closes")
	}
	var d boardData
	if err := json.Unmarshal([]byte(rest[:j]), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func byKey(t *testing.T, d boardData, key string) *checkOut {
	t.Helper()
	for i := range d.Checks {
		if d.Checks[i].Key == key {
			return &d.Checks[i]
		}
	}
	t.Fatalf("check %s not rendered", key)
	return nil
}

func resultLine(key, outcome string, elapsed *int64, at time.Time, commit string) record.Line {
	return record.Line{
		Time: at, Attempt: "a1", Kind: record.KindResult, Gate: "ci", Context: "local",
		Key: key, Runner: "go-test", Unit: "example.com/mod/pkg",
		Outcome: outcome, ElapsedMS: elapsed,
		Commit: commit, Worktree: "wt-0ad7", Profile: "full",
	}
}

func ms(v int64) *int64 { return &v }

// The board shows the latest observed result, time, and provenance per
// inventoried check. Records without provenance read as historical; a check
// with no result lines keeps nil stats and reads as never run.
func TestRenderLatestOutcomeAndProvenance(t *testing.T) {
	base := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	d := renderData(t, []record.Line{
		resultLine("go-test:example.com/mod/pkg:TestAlpha", record.OutcomePass, ms(5), base, "commit-old"),
		resultLine("go-test:example.com/mod/pkg:TestAlpha", record.OutcomeFail, ms(7), base.Add(time.Hour), "commit-new"),
		{
			Time: base, Attempt: "a1", Kind: record.KindResult, Gate: "ci",
			Key: "go-test:example.com/mod/pkg:TestBeta", Runner: "go-test",
			Unit: "example.com/mod/pkg", Outcome: record.OutcomeSkip,
		},
	})

	a := byKey(t, d, "go-test:example.com/mod/pkg:TestAlpha")
	if a.Stats == nil {
		t.Fatal("TestAlpha has no stats")
	}
	if a.Stats.Runs != 2 || a.Stats.Pass != 1 || a.Stats.Fail != 1 || a.Stats.Hits != 1 {
		t.Errorf("TestAlpha stats wrong: %+v", a.Stats)
	}
	if a.Stats.LastOutcome != record.OutcomeFail || a.Stats.LastCommit != "commit-new" ||
		a.Stats.LastWorktree != "wt-0ad7" || a.Stats.LastProfile != "full" || a.Stats.LastGate != "ci" {
		t.Errorf("TestAlpha latest wrong: %+v", a.Stats)
	}
	if a.Stats.LastTime == nil || !a.Stats.LastTime.Equal(base.Add(time.Hour)) {
		t.Errorf("TestAlpha last time wrong: %+v", a.Stats.LastTime)
	}
	if a.Stats.TotalMS != 12 || a.Stats.Timed != 2 {
		t.Errorf("TestAlpha timing wrong: %+v", a.Stats)
	}

	b := byKey(t, d, "go-test:example.com/mod/pkg:TestBeta")
	if b.Stats == nil {
		t.Fatal("a skipped check must still have stats")
	}
	if b.Stats.Runs != 1 || b.Stats.Skip != 1 || b.Stats.Hits != 0 {
		t.Errorf("TestBeta stats wrong: %+v", b.Stats)
	}
	if b.Stats.LastOutcome != record.OutcomeSkip {
		t.Errorf("TestBeta latest wrong: %+v", b.Stats)
	}
	if b.Stats.LastCommit != "" || b.Stats.LastWorktree != "" || b.Stats.LastProfile != "" {
		t.Errorf("historical record must keep empty provenance: %+v", b.Stats)
	}

	g := byKey(t, d, "go-test:example.com/mod/other:TestGamma")
	if g.Stats != nil {
		t.Errorf("a check with no result lines must read as never run, got %+v", g.Stats)
	}
}

// A cached pass ends a fail run like a pass but carries no fresh timing, and
// the unit build time stays in the invocation table, not the check.
func TestRenderCachedHasNoFreshTiming(t *testing.T) {
	base := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	d := renderData(t, []record.Line{
		resultLine("go-test:example.com/mod/pkg:TestDelta", record.OutcomeFail, ms(9), base, "c1"),
		resultLine("go-test:example.com/mod/pkg:TestDelta", record.OutcomeCached, nil, base.Add(time.Hour), "c2"),
		{Time: base.Add(time.Hour), Attempt: "a1", Kind: record.KindInvocation, Gate: "ci",
			Runner: "go-test", Unit: "example.com/mod/pkg", What: "run", ElapsedMS: ms(100)},
	})
	s := byKey(t, d, "go-test:example.com/mod/pkg:TestDelta").Stats
	if s == nil {
		t.Fatal("TestDelta has no stats")
	}
	if s.Runs != 2 || s.Cached != 1 || s.Fail != 1 || s.Hits != 1 {
		t.Errorf("TestDelta stats wrong: %+v", s)
	}
	if s.TotalMS != 9 || s.Timed != 1 {
		t.Errorf("cached outcome must not add fresh timing: %+v", s)
	}
	if s.LastOutcome != record.OutcomeCached {
		t.Errorf("TestDelta latest wrong: %+v", s)
	}
	found := false
	for _, v := range d.Invocations {
		if v.Unit == "example.com/mod/pkg" && v.Runs == 1 && v.TotalMS == 100 {
			found = true
		}
	}
	if !found {
		t.Errorf("build/process time must stay in the invocation table: %+v", d.Invocations)
	}
}

// An invocation's success never invents a pass: only KindResult lines feed
// per-check stats.
func TestRenderNeverInfersPassFromInvocation(t *testing.T) {
	base := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	d := renderData(t, []record.Line{
		{Time: base, Attempt: "a1", Kind: record.KindInvocation, Gate: "ci",
			Runner: "go-test", Unit: "example.com/mod/pkg", What: "run", ElapsedMS: ms(50)},
	})
	g := byKey(t, d, "go-test:example.com/mod/pkg:TestAlpha")
	if g.Stats != nil {
		t.Errorf("invocation without results must not invent stats: %+v", g.Stats)
	}
}

// A renamed check's history stays under its old key as an orphan: what it
// caught before it left. Stats aggregate on the full runner:unit:name key,
// so checks sharing a unit never merge.
func TestRenderRenameKeepsOrphanHistory(t *testing.T) {
	base := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	const oldKey = "go-test:example.com/mod/pkg:TestRenamed"
	d := renderData(t, []record.Line{
		resultLine(oldKey, record.OutcomeFail, ms(4), base, "c1"),
		resultLine(oldKey, record.OutcomePass, ms(3), base.Add(time.Hour), "c2"),
		resultLine("go-test:example.com/mod/pkg:TestAlpha", record.OutcomePass, ms(6), base, "c1"),
		resultLine("go-test:example.com/mod/pkg:TestBeta", record.OutcomeBlocked, nil, base, "c1"),
	})
	var orphan *orphanOut
	for i := range d.Orphans {
		if d.Orphans[i].Key == oldKey {
			orphan = &d.Orphans[i]
		}
	}
	if orphan == nil {
		t.Fatalf("renamed check missing from orphans: %+v", d.Orphans)
	}
	if orphan.Runs != 2 || orphan.Hits != 1 || orphan.LastOutcome != record.OutcomePass {
		t.Errorf("orphan history wrong: %+v", orphan)
	}
	if orphan.LastCommit != "c2" {
		t.Errorf("orphan provenance wrong: %+v", orphan)
	}
	a := byKey(t, d, "go-test:example.com/mod/pkg:TestAlpha").Stats
	b := byKey(t, d, "go-test:example.com/mod/pkg:TestBeta").Stats
	if a == nil || b == nil || a.Runs != 1 || a.Pass != 1 || b.Runs != 1 || b.Blocked != 1 {
		t.Errorf("same-unit checks must not merge: alpha=%+v beta=%+v", a, b)
	}
	if b.Hits != 0 {
		t.Errorf("blocked must not add hits: %+v", b)
	}
}

// A keyed dispatch failure means execution was blocked, not absent history.
func TestRenderDispatchDiagnostic(t *testing.T) {
	d := renderData(t, []record.Line{{
		Time: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC),
		Kind: record.KindDispatch, Attempt: "a", Gate: "ci",
		Key:    "go-test:example.com/mod/pkg:TestAlpha",
		Detail: "runner executable unavailable", Commit: "c1", Profile: "full",
	}})
	s := byKey(t, d, "go-test:example.com/mod/pkg:TestAlpha").Stats
	if s == nil || s.LastOutcome != record.OutcomeBlocked || s.Blocked != 1 ||
		s.LastDetail != "runner executable unavailable" || s.LastCommit != "c1" {
		t.Fatalf("dispatch diagnostic lost: %+v", s)
	}
}
