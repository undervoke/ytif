package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gate"
	"github.com/undervoke/ytif/internal/gateconf"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/record"
	"github.com/undervoke/ytif/internal/shellcmd"
)

// serialRunners run their work one at a time inside the source: gate workers
// do not apply within them. Their known work sums first, then counts as one
// gate-level component.
var serialRunners = map[string]bool{
	"dotnet-test": true,
	"node-verify": true,
	"go-verify":   true,
	"nx-target":   true,
}

// keyLevelRunners report one outcome per verification key, so a key's own
// result carries its elapsed time. The rest report one process invocation.
var keyLevelRunners = map[string]bool{
	"node-verify": true,
	"go-verify":   true,
	"command":     true,
	"nx-target":   true,
}

// noGateEvidence lists process runners whose records key test files while a
// gate runs them by config, so no gate component can be built from them. The
// guard still prices a direct run of either by its config.
var noGateEvidence = map[string]bool{
	"vitest-test":     true,
	"playwright-test": true,
}

type sample struct {
	ms      int64
	attempt string
	at      time.Time
}

// estimator holds one repository's record evidence, indexed so a ledger with
// thousands of entries costs one map lookup each rather than a rescan.
type estimator struct {
	now time.Time
	// proc maps "runner\x00unit" to gate to samples of that unit's run phase;
	// keys maps a verification key to gate to samples of that key.
	proc map[string]map[string][]sample
	keys map[string]map[string][]sample
	nx   map[string]*nxInspector
}

// newEstimator indexes lines. A process sample is one attempt, runner and
// unit group whose run invocations each carry elapsed time and no error
// detail: their elapsed times summed into one sample. A group gives no
// sample when any of its run invocations lacks elapsed time or carries a
// detail, when its gate is empty, or when that runner recorded a failure in
// the same attempt. dotnet additionally needs exactly one run invocation in
// the group, because a partial selection splits into several filtered runs.
func newEstimator(lines []record.Line, now time.Time) *estimator {
	e := &estimator{now: now, proc: map[string]map[string][]sample{}, keys: map[string]map[string][]sample{}, nx: map[string]*nxInspector{}}
	failed := map[string]map[string]bool{}
	for _, l := range lines {
		if l.Kind == record.KindResult && l.Outcome == string(check.Fail) && l.Attempt != "" && l.Runner != "" {
			if failed[l.Attempt] == nil {
				failed[l.Attempt] = map[string]bool{}
			}
			failed[l.Attempt][l.Runner] = true
		}
	}
	type group struct {
		runner, unit, gate string
		attempt            string
		count              int
		bad                bool
		ms                 int64
		at                 time.Time
	}
	groups := map[string]*group{}
	for _, l := range lines {
		if l.Kind != record.KindInvocation || l.What != "run" || l.Attempt == "" || l.Runner == "" || l.Unit == "" {
			continue
		}
		id := l.Attempt + "\x00" + l.Runner + "\x00" + l.Unit
		gr := groups[id]
		if gr == nil {
			gr = &group{runner: l.Runner, unit: l.Unit, gate: l.Gate, attempt: l.Attempt}
			groups[id] = gr
		}
		gr.count++
		if l.ElapsedMS == nil || l.Detail != "" {
			gr.bad = true
			continue
		}
		gr.ms += *l.ElapsedMS
		if l.Time.After(gr.at) {
			gr.at = l.Time
		}
	}
	for _, gr := range groups {
		if gr.bad || gr.count == 0 || gr.gate == "" {
			continue
		}
		if gr.runner == "dotnet-test" && gr.count != 1 {
			continue
		}
		if failed[gr.attempt][gr.runner] {
			continue
		}
		uk := gr.runner + "\x00" + gr.unit
		if e.proc[uk] == nil {
			e.proc[uk] = map[string][]sample{}
		}
		e.proc[uk][gr.gate] = append(e.proc[uk][gr.gate], sample{ms: gr.ms, attempt: gr.attempt, at: gr.at})
	}
	for _, l := range lines {
		if l.Kind != record.KindResult || l.ElapsedMS == nil || l.Key == "" || l.Attempt == "" || l.Gate == "" {
			continue
		}
		if l.Outcome != string(check.Pass) {
			continue
		}
		runner := l.Key
		if i := strings.Index(runner, ":"); i >= 0 {
			runner = runner[:i]
		}
		// A failed process says its whole source's attempt is untrustworthy;
		// a failed direct command says only that command, which the outcome
		// already excludes.
		if runner != inventory.CommandRunner && failed[l.Attempt][runner] {
			continue
		}
		if e.keys[l.Key] == nil {
			e.keys[l.Key] = map[string][]sample{}
		}
		e.keys[l.Key][l.Gate] = append(e.keys[l.Key][l.Gate], sample{ms: *l.ElapsedMS, attempt: l.Attempt, at: l.Time})
	}
	return e
}

// processUnit returns one unit's run-phase measure: the largest gate mean of
// that unit's last 10 samples within 30 days.
func (e *estimator) processUnit(runner, unit string) (ms int64, n int, latest time.Time, ok bool) {
	return representative(e.proc[runner+"\x00"+unit], e.now)
}

// keyResult returns one verification key's measure, the same shape as
// processUnit's for a whole process.
func (e *estimator) keyResult(key string) (ms int64, n int, latest time.Time, ok bool) {
	return representative(e.keys[key], e.now)
}

func (e *estimator) inspector(root string) *nxInspector {
	if insp, ok := e.nx[root]; ok {
		return insp
	}
	insp := newNxInspector(root)
	e.nx[root] = insp
	return insp
}

// representative returns the largest gate mean of last-10-in-30d samples.
// Recency is the winning sample group's latest.
func representative(byGate map[string][]sample, now time.Time) (ms int64, n int, latest time.Time, ok bool) {
	cutoff := now.AddDate(0, 0, -sampleDays)
	gates := make([]string, 0, len(byGate))
	for g := range byGate {
		gates = append(gates, g)
	}
	sort.Strings(gates)
	var best int64
	var bestN int
	var bestLatest time.Time
	for _, g := range gates {
		samples := byGate[g]
		kept := make([]sample, 0, len(samples))
		for _, s := range samples {
			if !s.at.Before(cutoff) {
				kept = append(kept, s)
			}
		}
		sort.Slice(kept, func(i, j int) bool { return kept[i].at.Before(kept[j].at) })
		if len(kept) > sampleLast {
			kept = kept[len(kept)-sampleLast:]
		}
		if len(kept) == 0 {
			continue
		}
		var sum int64
		for _, s := range kept {
			sum += s.ms
		}
		mean := (sum + int64(len(kept))/2) / int64(len(kept))
		if bestN == 0 || mean > best {
			best, bestN, bestLatest = mean, len(kept), kept[len(kept)-1].at
		}
	}
	return best, bestN, bestLatest, bestN > 0
}

func basisText(what string, ms int64, n int, latest, now time.Time) string {
	return what + " ~" + formatDur(time.Duration(ms)*time.Millisecond) + " (n=" + strconv.Itoa(n) + ", latest " + ageText(now.Sub(latest)) + ")"
}

func ageText(d time.Duration) string {
	return d.Round(time.Hour).String() + " ago"
}

// combine applies max(longest, sum/workers) to component durations.
func combine(durations []time.Duration, workers int) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	var longest, sum time.Duration
	for _, d := range durations {
		if d > longest {
			longest = d
		}
		sum += d
	}
	if workers <= 1 {
		return sum
	}
	q := sum / time.Duration(workers)
	if sum%time.Duration(workers) != 0 {
		q += time.Millisecond
	}
	if q < longest {
		return longest
	}
	return q
}

// component is one piece of a gate's or a command's workload.
type component struct {
	name   string
	dur    time.Duration
	basis  string
	latest time.Time
}

// fold sums each serial source into one component, keeps the rest as
// concurrent components, and orders them for a stable record.
func fold(serial map[string]time.Duration, serialBases map[string][]string, serialLatest map[string]time.Time, conc []component) []component {
	runners := make([]string, 0, len(serial))
	for r := range serial {
		runners = append(runners, r)
	}
	sort.Strings(runners)
	var out []component
	for _, r := range runners {
		out = append(out, component{name: r, dur: serial[r], basis: r + " serial " + formatDur(serial[r]) + " (" + strings.Join(serialBases[r], ", ") + ")", latest: serialLatest[r]})
	}
	sort.Slice(conc, func(i, j int) bool { return conc[i].name < conc[j].name })
	return append(out, conc...)
}

// gateEstimateText writes the arithmetic the refusal record keeps: the
// estimate with the sum, longest component, workers, component count and
// recency it came from, then each component's own evidence.
func gateEstimateText(gateName string, est, sum, longest time.Duration, workers int, known bool, comps []component, latest time.Time, now time.Time) string {
	workersText := "workers unknown"
	if known {
		workersText = "workers " + strconv.Itoa(workers)
	}
	text := fmt.Sprintf("gate %s estimate %s (sum %s, longest %s, %s, components %d, latest %s)",
		gateName, formatDur(est), formatDur(sum), formatDur(longest), workersText, len(comps), ageText(now.Sub(latest)))
	return text + ": " + strings.Join(basisOf(comps), "; ")
}

func basisOf(comps []component) []string {
	out := make([]string, len(comps))
	for i, c := range comps {
		out[i] = c.basis
	}
	return out
}

// priceGate estimates a direct ytif commit, push, or ci command. Membership
// is the resolved Member bit; a process unit counts only when every key
// registered under it runs at the gate with full scope, and a key counts
// from its own result. An empty gate has no price, so historical evidence
// from other gates never prices an empty push. Serial sources sum into one
// component each, direct commands stay independent, and a runner whose
// records cannot be matched to a gate invocation is left out.
func priceGate(c shellcmd.Command, gateName string, rd *repoData) (durationDecision, bool) {
	profile := literalFlag(c, "--profile")
	if hasNonLiteralFlag(c, "--profile") {
		return durationDecision{}, false
	}
	hasMember := false
	for _, e := range rd.inv.Verifications {
		if e.Member(gateName) {
			hasMember = true
			break
		}
	}
	if !hasMember {
		return durationDecision{}, false
	}
	workers, knownWorkers := gateWorkers(c, rd.root)
	policy, err := rd.inv.Gate.Context(gateName, profile)
	if err != nil {
		return durationDecision{}, false
	}
	est := rd.evidence()
	serial := map[string]time.Duration{}
	serialBases := map[string][]string{}
	serialLatest := map[string]time.Time{}
	var conc []component
	var units []string
	unitKeys := map[string][]inventory.Entry{}
	for _, e := range rd.inv.Verifications {
		k := e.Key()
		if keyLevelRunners[k.Runner] || noGateEvidence[k.Runner] {
			continue
		}
		uk := k.Runner + "\x00" + k.Unit
		if _, fresh := unitKeys[uk]; !fresh {
			units = append(units, uk)
		}
		unitKeys[uk] = append(unitKeys[uk], e)
	}
	for _, e := range rd.inv.Verifications {
		k := e.Key()
		if !keyLevelRunners[k.Runner] || !e.Member(gateName) {
			continue
		}
		if scope, _ := policy.Resolve(e, gateName); scope != check.ScopeFull {
			continue
		}
		ms, n, latest, ok := est.keyResult(k.String())
		if !ok {
			continue
		}
		addComponent(serial, serialBases, serialLatest, &conc, k.Runner, k.String(), time.Duration(ms)*time.Millisecond, basisText(k.String(), ms, n, latest, est.now), latest)
	}
	sort.Strings(units)
	for _, uk := range units {
		if !unitRunsWhole(unitKeys[uk], gateName, policy) {
			continue
		}
		parts := strings.SplitN(uk, "\x00", 2)
		ms, n, latest, ok := est.processUnit(parts[0], parts[1])
		if !ok {
			continue
		}
		name := parts[0] + ":" + parts[1]
		addComponent(serial, serialBases, serialLatest, &conc, parts[0], name, time.Duration(ms)*time.Millisecond, basisText(name, ms, n, latest, est.now), latest)
	}
	comps := fold(serial, serialBases, serialLatest, conc)
	if len(comps) == 0 {
		return durationDecision{}, false
	}
	priced, longest, sum, newest := estimateComponents(comps, workers, knownWorkers)
	detail := gateEstimateText(gateName, priced, sum, longest, workers, knownWorkers, comps, newest, rd.now)
	if len(detail) > 900 {
		detail = detail[:900] + "…"
	}
	return durationDecision{runner: "ytif " + gateName, estimate: priced, limit: rd.limit, basis: detail, known: true}, true
}

// unitRunsWhole reports whether every key registered under one process unit
// runs at the gate with full scope, which is what makes the unit's recorded
// process measure the whole unit.
func unitRunsWhole(entries []inventory.Entry, gateName string, policy inventory.GateContext) bool {
	for _, e := range entries {
		if !e.Member(gateName) {
			return false
		}
		if scope, _ := policy.Resolve(e, gateName); scope != check.ScopeFull {
			return false
		}
	}
	return len(entries) > 0
}
func addComponent(serial map[string]time.Duration, serialBases map[string][]string, serialLatest map[string]time.Time, conc *[]component, runner, name string, d time.Duration, basis string, latest time.Time) {
	if serialRunners[runner] {
		serial[runner] += d
		serialBases[runner] = append(serialBases[runner], basis)
		if latest.After(serialLatest[runner]) {
			serialLatest[runner] = latest
		}
		return
	}
	*conc = append(*conc, component{name: name, dur: d, basis: basis, latest: latest})
}

// estimateComponents applies the aggregation rule: with known workers,
// max(longest, sum/workers); with unknown workers, the longest component
// alone, since dividing a workload of unknown size would invent a smaller
// one. latest is the newest sample any component came from.
func estimateComponents(comps []component, workers int, known bool) (est, longest, sum time.Duration, latest time.Time) {
	for _, c := range comps {
		if c.dur > longest {
			longest = c.dur
		}
		sum += c.dur
		if c.latest.After(latest) {
			latest = c.latest
		}
	}
	if !known {
		return longest, longest, sum, latest
	}
	return combine(comps2durs(comps), workers), longest, sum, latest
}

func comps2durs(comps []component) []time.Duration {
	out := make([]time.Duration, len(comps))
	for i, c := range comps {
		out[i] = c.dur
	}
	return out
}

// priceCommand resolves one simple command to an estimate. Exact ledger
// argv+cwd matches price first, before category classification, so a
// registered pytest or cargo command works. Wrappers price what they run
// instead of themselves. Depth bounds nested resolution.
func (g *guard) priceCommand(c shellcmd.Command, rd *repoData, dir string, depth int) (durationDecision, bool) {
	if isHelp(c) {
		return durationDecision{}, false
	}
	switch rail := gateconf.RailCommand(c); rail {
	case "allow":
		return durationDecision{}, false
	case check.GateCommit, check.GatePush, check.GateCI:
		return priceGate(c, rail, rd)
	}
	if c.Chdir || depth > 8 {
		return durationDecision{}, false
	}
	if est, ok := priceExactCommand(c, dir, rd); ok {
		return est, true
	}
	if body, ok := shBody(c); ok {
		return g.priceNested(body, dir, rd, depth)
	}
	if run, ok := gateconf.PackageScript(c); ok && run.Name != "" {
		return g.priceScript(c, run, dir, rd, depth)
	}
	if wrapped, _ := gateconf.Wrapped(c); len(wrapped) > 0 {
		return g.priceMany(wrapped, rd, dir, depth+1)
	}
	switch gateconf.TestRunner(c) {
	case "go test":
		return priceGoTest(c, dir, rd)
	case "dotnet test":
		return priceDotnet(c, dir, rd)
	case "vitest":
		return priceVitest(c, "vitest-test", vitestConfigNames, dir, rd)
	case "playwright test":
		return priceVitest(c, "playwright-test", playwrightConfigNames, dir, rd)
	case "node --test":
		return priceNodeTest(c, dir, rd)
	}
	if c.NameBase() == "nx" {
		return g.priceNx(c, dir, rd, depth)
	}
	return durationDecision{}, false
}

// priceNested prices the top-level steps of a literal `sh -c` script. The
// nested shell starts where its parent stands and its own cd moves only it,
// so the walk stays inside the repository the guard loaded.
func (g *guard) priceNested(body, dir string, rd *repoData, depth int) (durationDecision, bool) {
	sub, _, err := shellcmd.Steps(body)
	if err != nil {
		return durationDecision{}, false
	}
	return g.priceSteps(sub, dir, rd, depth)
}

func (g *guard) priceSteps(sub []shellcmd.Step, dir string, rd *repoData, depth int) (durationDecision, bool) {
	var decisions []durationDecision
	for _, pu := range walkSteps(sub, dir, rd.root, true, func(string) *repoData { return rd }) {
		if est, ok := g.priceUnit(pu.u, pu.dir, rd, depth+1); ok {
			decisions = append(decisions, est)
		}
	}
	return sumDecisions(decisions)
}

// priceMany sums the estimates of commands one runs together, such as the
// set a wrapper starts.
func (g *guard) priceMany(cmds []shellcmd.Command, rd *repoData, dir string, depth int) (durationDecision, bool) {
	var decisions []durationDecision
	for _, c := range cmds {
		if est, ok := g.priceCommand(c, rd, dir, depth); ok {
			decisions = append(decisions, est)
		}
	}
	return sumDecisions(decisions)
}

// followDirIn resolves a literal cd inside one repository; a target outside
// the root makes the following directory unknown, because the guard prices
// only what it can locate in the ledger it loaded.
func followDirIn(cur, target, root string) (string, bool) {
	nd, ok := followDir(cur, target)
	if !ok {
		return "", false
	}
	rel, err := filepath.Rel(root, nd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		if filepath.Clean(nd) == filepath.Clean(root) {
			return root, true
		}
		return "", false
	}
	return nd, true
}

// priceExactCommand prices a ledger command entry by its key result: the
// command the agent typed is the command the gate recorded, argv and working
// directory alike.
func priceExactCommand(c shellcmd.Command, dir string, rd *repoData) (durationDecision, bool) {
	if dir == "" {
		return durationDecision{}, false
	}
	rel := relDir(dir, rd.root)
	for _, e := range rd.inv.Verifications {
		if e.Runner != inventory.CommandRunner || !argvEqual(e.Run, c.Args) || !cwdEqual(e.Cwd, rel) {
			continue
		}
		ms, n, latest, ok := rd.evidence().keyResult(e.Key().String())
		if !ok {
			// Registered but without a usable record: unknown, never a price
			// of nothing. A sibling entry for the same argv may have one.
			continue
		}
		return durationDecision{runner: "command " + e.Name, estimate: time.Duration(ms) * time.Millisecond, limit: rd.limit,
			basis: basisText(e.Key().String(), ms, n, latest, rd.now), known: true}, true
	}
	return durationDecision{}, false
}

func argvEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cwdEqual(entryCwd, rel string) bool {
	want := entryCwd
	if want == "" {
		want = "."
	}
	return filepath.Clean(want) == filepath.Clean(rel)
}

func relDir(dir, root string) string {
	if dir == "" {
		return "."
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return "."
	}
	return filepath.ToSlash(rel)
}

// priceGoTest prices `go test` by the packages it names. A package list of
// ./... is every package the repository tracks; a relative pattern the guard
// cannot map, a name filter, or an unresolvable flag leaves it unknown.
func priceGoTest(c shellcmd.Command, dir string, rd *repoData) (durationDecision, bool) {
	if hasNameFilter(c, []string{"-run", "-skip"}) {
		return durationDecision{}, false
	}
	units, ok := goUnits(c, dir, rd.root)
	if !ok {
		return durationDecision{}, false
	}
	comps, ok := componentsForUnits(rd, "go-test", units)
	if !ok {
		return durationDecision{}, false
	}
	workers, known := goWorkers(c)
	return commandDecision("go test", comps, workers, known, rd)
}

// priceDotnet prices `dotnet test` for exactly one project. A partial
// selection splits into several filtered runs, which the records cannot
// attribute, so a solution, several projects, or a name filter is unknown.
func priceDotnet(c shellcmd.Command, dir string, rd *repoData) (durationDecision, bool) {
	if hasNameFilter(c, []string{"--filter"}) {
		return durationDecision{}, false
	}
	units, ok := dotnetUnits(c, dir, rd.root)
	if !ok || len(units) != 1 {
		return durationDecision{}, false
	}
	comps, ok := componentsForUnits(rd, "dotnet-test", units)
	if !ok {
		return durationDecision{}, false
	}
	// dotnet runs its projects one at a time inside the source.
	return commandDecision("dotnet test", comps, 1, true, rd)
}

// componentsForUnits prices one record unit each, skipping a unit with no
// usable sample rather than inventing a duration for it.
func componentsForUnits(rd *repoData, runner string, units []string) ([]component, bool) {
	est := rd.evidence()
	var comps []component
	for _, u := range units {
		ms, n, latest, ok := est.processUnit(runner, u)
		if !ok {
			continue
		}
		comps = append(comps, component{name: runner + ":" + u, dur: time.Duration(ms) * time.Millisecond, basis: basisText(runner+":"+u, ms, n, latest, est.now), latest: latest})
	}
	if len(comps) == 0 {
		return nil, false
	}
	return comps, true
}

// commandDecision folds a command's components into one estimate, reporting
// the arithmetic in the record basis.
func commandDecision(runner string, comps []component, workers int, known bool, rd *repoData) (durationDecision, bool) {
	priced, longest, sum, _ := estimateComponents(comps, workers, known)
	workersText := "workers unknown"
	if known {
		workersText = "workers " + strconv.Itoa(workers)
	}
	detail := fmt.Sprintf("%s estimate %s (sum %s, longest %s, %s, components %d): %s",
		runner, formatDur(priced), formatDur(sum), formatDur(longest), workersText, len(comps), strings.Join(basisOf(comps), "; "))
	if len(detail) > 900 {
		detail = detail[:900] + "…"
	}
	return durationDecision{runner: runner, estimate: priced, limit: rd.limit, basis: detail, known: true}, true
}

var (
	vitestConfigNames     = regexp.MustCompile(`^vitest\.config\.(ts|mts|cts|js|mjs|cjs)$`)
	viteConfigNames       = regexp.MustCompile(`^vite\.config\.(ts|mts|cts|js|mjs|cjs)$`)
	playwrightConfigNames = regexp.MustCompile(`^playwright\.config\.(ts|mts|cts|js|mjs|cjs)$`)
)

// priceVitest prices a vitest or playwright run by the config that owns it.
// The config is the one the command names with --config, or the one beside
// the directory it runs in. Only a run of the whole config prices: test
// files and patterns narrow within it, and the records carry no measure of a
// subset. A word the guard does not recognise is read as narrowing, so an
// option it has not met makes the command unknown rather than cheaper.
func priceVitest(c shellcmd.Command, runner string, own *regexp.Regexp, dir string, rd *repoData) (durationDecision, bool) {
	if dir == "" {
		return durationDecision{}, false
	}
	tables, ok := configRunnerTables(runner)
	if !ok || hasNameFilter(c, nameFilterFlags(runner)) || !wholeConfigRun(c, tables.names, tables.valueFlags, tables.boolFlags) {
		return durationDecision{}, false
	}
	unit, ok := configUnit(c, own, dir, rd.root)
	if !ok {
		return durationDecision{}, false
	}
	comps, ok := componentsForUnits(rd, runner, []string{unit})
	if !ok {
		return durationDecision{}, false
	}
	workers, known := processWorkers(c, runner)
	return commandDecision(runner, comps, workers, known, rd)
}

// runnerTables is what one config-running test tool accepts. Only the words
// that carry a separate value, the switches that carry none, and the tool's
// own words are listed: an option the guard has not met makes the run
// unknown, which is the safe direction, so the table needs only the entries
// that widen coverage.
type runnerTables struct {
	names      map[string]bool
	valueFlags map[string]bool
	boolFlags  map[string]bool
}

func configRunnerTables(runner string) (runnerTables, bool) {
	switch runner {
	case "vitest-test":
		return runnerTables{
			names: map[string]bool{"vitest": true, "run": true, "start": true},
			valueFlags: map[string]bool{
				"-c": true, "--config": true, "--root": true, "-r": true,
				"--reporter": true, "-t": true, "--testNamePattern": true, "--test-name-pattern": true,
				"--suite": true,
				"-m":      true, "--mode": true, "-e": true, "--environment": true,
				"--pool": true, "--maxWorkers": true, "--max-workers": true, "--minWorkers": true,
				"--min-workers": true, "--bail": true, "--timeout": true, "--retry": true,
				"--browser": true, "--workspaces": true, "--changed": true,
			},
			boolFlags: map[string]bool{
				"-u": true, "--update": true, "--update-snapshots": true,
			},
		}, true
	case "playwright-test":
		return runnerTables{
			names: map[string]bool{"playwright": true, "test": true},
			valueFlags: map[string]bool{
				"--config": true, "-g": true, "--grep": true, "--grep-invert": true, "--grep-title": true,
				"-j": true, "--workers": true, "--reporter": true, "-o": true,
				"--output": true, "--timeout": true, "--max-failures": true, "--retries": true,
				"--browser": true, "--channel": true, "--host": true, "--port": true, "--device": true,
				"--env": true, "--tracing": true, "--attached-report": true,
			},
			boolFlags: map[string]bool{
				"--update-snapshots": true,
			},
		}, true
	}
	return runnerTables{}, false
}

// wholeConfigRun reports a command whose every word the guard can place: the
// tool's own words, an option it knows carries a value, a switch it knows
// carries none, or a known option written as --flag=value. Anything else — a
// positional test file, a value after an option it has not met — is a
// narrowing it cannot price, so the run is unknown rather than estimated
// from the whole config.
func wholeConfigRun(c shellcmd.Command, names, valueFlags, boolFlags map[string]bool) bool {
	for i := 1; i < len(c.Args); i++ {
		if !c.Literal[i] {
			return false
		}
		a := c.Args[i]
		if a == "--" {
			return false // the words after it go to the tool itself
		}
		if strings.HasPrefix(a, "-") {
			flag, _, hasVal := splitFlag(a)
			if hasVal {
				if valueFlags[flag] || boolFlags[flag] {
					continue // --flag=value carries no separate word
				}
				return false
			}
			if valueFlags[flag] {
				i++ // its value is a setting, not a test file
				continue
			}
			if boolFlags[flag] {
				continue // a switch with no value, so the next word stays a word
			}
			return false
		}
		if names[a] {
			continue // the tool's own word, such as vitest's run
		}
		return false // a test file or pattern the records cannot attribute
	}
	return true
}

// configUnit returns the repository-relative config path a vitest or
// playwright run uses: the one --config names, or the single config beside
// dir. Vitest prefers its own config and falls back to a vite.config that
// mentions vitest, which is the order the runner discovers them in.
func configUnit(c shellcmd.Command, own *regexp.Regexp, dir, root string) (string, bool) {
	if hasNonLiteralFlag(c, "--config") || hasNonLiteralFlag(c, "-c") || hasNonLiteralFlag(c, "--root") || hasNonLiteralFlag(c, "-r") {
		return "", false
	}
	if own == vitestConfigNames {
		named := literalFlag(c, "--root")
		if named == "" {
			named = literalFlag(c, "-r")
		}
		if named != "" {
			rel, ok := relWithin(dir, named, root)
			if !ok {
				return "", false
			}
			dir = filepath.Join(root, filepath.FromSlash(rel))
		}
	}
	named := literalFlag(c, "--config")
	if named == "" {
		named = literalFlag(c, "-c")
	}
	if named != "" {
		return relWithin(dir, named, root)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() || !own.MatchString(e.Name()) {
			continue
		}
		found = append(found, e.Name())
	}
	if len(found) == 1 {
		return path.Join(relDir(dir, root), filepath.ToSlash(found[0])), true
	}
	if len(found) == 0 && own == vitestConfigNames {
		// A vite.config that mentions vitest stands in for a missing one.
		for _, e := range entries {
			if e.IsDir() || !viteConfigNames.MatchString(e.Name()) {
				continue
			}
			src, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil || !strings.Contains(string(src), "vitest") {
				continue
			}
			found = append(found, e.Name())
		}
		if len(found) == 1 {
			return path.Join(relDir(dir, root), filepath.ToSlash(found[0])), true
		}
	}
	return "", false
}

// relWithin maps a config path a command names, relative to the directory it
// runs in, onto the repository-relative unit the records carry.
func relWithin(dir, named, root string) (string, bool) {
	var abs string
	if filepath.IsAbs(named) {
		abs = filepath.Clean(named)
	} else if dir == "" {
		return "", false
	} else {
		abs = filepath.Join(dir, filepath.FromSlash(named))
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// priceNodeTest prices `node --test` by the test files the command names.
// Without operands node discovers the whole repository, which the guard
// cannot bound to the units the records carry, so a bare run stays unknown.
func priceNodeTest(c shellcmd.Command, dir string, rd *repoData) (durationDecision, bool) {
	if hasNameFilter(c, nameFilterFlags("node-test")) {
		return durationDecision{}, false
	}
	files, ok := nodeTestFiles(c, dir, rd.root)
	if !ok || len(files) == 0 {
		return durationDecision{}, false
	}
	comps, ok := componentsForUnits(rd, "node-test", files)
	if !ok {
		return durationDecision{}, false
	}
	workers, known := processWorkers(c, "node-test")
	return commandDecision("node --test", comps, workers, known, rd)
}

// nodeTestFiles returns the repository-relative test files a `node --test`
// run names, false when a word is not literal or lands outside the
// repository. Words after `--` go to the tests, not to node.
func nodeTestFiles(c shellcmd.Command, dir, root string) ([]string, bool) {
	var out []string
	for i := 1; i < len(c.Args); i++ {
		a := c.Args[i]
		if !c.Literal[i] {
			return nil, false
		}
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") {
			if strings.Contains(a, "=") || nodeTestBoolFlags[a] {
				continue
			}
			if nodeTestValueFlags[a] {
				i++
				continue
			}
			return nil, false
		}
		rel, ok := relWithin(dir, a, root)
		if !ok {
			return nil, false
		}
		out = append(out, rel)
	}
	return out, true
}

var (
	nodeTestValueFlags = map[string]bool{
		"-t": true, "--test-name-pattern": true, "--test-skip-pattern": true,
		"--test-concurrency": true, "--test-reporter": true, "--test-reporter-destination": true,
		"--test-timeout": true, "--experimental-test-isolation": true, "--test-observer-output": true,
		"-C": true, "--test-shard": true,
	}
	nodeTestBoolFlags = map[string]bool{
		"--test": true, "--experimental-test-coverage": true, "--test-only": true,
		"--experimental-test-snapshots": true, "--test-update-snapshots": true,
		"--experimental-test-coverage-include-uncovered-files": true,
	}
)

// priceNx prices Nx commands through the static own-body resolver: each
// selected project:target prices from its own nx-target record first, and
// only an uncached target with no record falls back to its single literal
// body. dependsOn is never priced; affected and unresolvable selections stay
// unknown.
func (g *guard) priceNx(c shellcmd.Command, dir string, rd *repoData, depth int) (durationDecision, bool) {
	est := rd.evidence()
	sels, ok := est.inspector(rd.root).Select(c, dir)
	if !ok || len(sels) == 0 {
		return durationDecision{}, false
	}
	var comps []component
	for _, s := range sels {
		if ms, n, latest, ok := est.keyResult(s.Key()); ok {
			comps = append(comps, component{name: s.Label(), dur: time.Duration(ms) * time.Millisecond, basis: basisText(s.Key(), ms, n, latest, est.now), latest: latest})
			continue
		}
		if s.Cacheable || s.Body == "" {
			// A cached target runs nothing to time, and a target whose body
			// the guard cannot read has no price the guard may invent.
			continue
		}
		sub, _, err := shellcmd.Steps(s.Body)
		if err != nil || len(sub) != 1 || sub[0].Alt || len(sub[0].Units) != 1 {
			continue
		}
		u := sub[0].Units[0]
		if u.Piped || len(u.Commands) != 1 {
			continue
		}
		inner, ok := g.priceCommand(u.Commands[0], rd, s.BodyDir, depth+1)
		if !ok || !inner.known {
			continue
		}
		comps = append(comps, component{name: s.Label(), dur: inner.estimate, basis: inner.basis})
	}
	if len(comps) == 0 {
		return durationDecision{}, false
	}
	workers, known := nxWorkers(c, rd.root)
	labels := make([]string, len(comps))
	for i, cc := range comps {
		labels[i] = cc.name
	}
	sort.Strings(labels)
	return commandDecision("nx "+strings.Join(labels, ","), comps, workers, known, rd)
}

// priceScript prices a package script by resolving each verification its body
// runs. Bodies are parsed as top-level steps with their own directory
// tracking; nested scripts recurse with a depth bound. A pnpm recursive run
// sums each package's own scripts and combines the packages with workspace
// concurrency.
func (g *guard) priceScript(c shellcmd.Command, run gateconf.ScriptRun, dir string, rd *repoData, depth int) (durationDecision, bool) {
	if dir == "" || depth > 8 {
		return durationDecision{}, false
	}
	// Words forwarded to the script change the workload in ways the body
	// does not show. Stay unknown instead of pricing the body as written.
	if len(run.Args) > 0 {
		return durationDecision{}, false
	}
	s := newScripts()
	bodies := s.bodies(run, dir)
	if len(bodies) == 0 {
		return durationDecision{}, false
	}
	perPackage := map[string]time.Duration{}
	bases := map[string][]string{}
	var order []string
	for _, b := range bodies {
		est, ok := g.priceNested(b.text, b.dir, rd, depth)
		if !ok || !est.known {
			continue
		}
		if _, fresh := perPackage[b.dir]; !fresh {
			order = append(order, b.dir)
		}
		perPackage[b.dir] += est.estimate
		bases[b.dir] = append(bases[b.dir], est.basis)
	}
	if len(order) == 0 {
		return durationDecision{}, false
	}
	sort.Strings(order)
	var comps []component
	for _, d := range order {
		name := relDir(d, rd.root)
		if name == "" || name == "." {
			name = "the package"
		}
		comps = append(comps, component{name: name, dur: perPackage[d], basis: name + " " + formatDur(perPackage[d]) + " (" + strings.Join(bases[d], " + ") + ")"})
	}
	if run.Manager == "pnpm" && (run.Recursive || len(run.Filters) > 0) {
		workers, known := pnpmWorkers(c)
		return commandDecision("pnpm -r", comps, workers, known, rd)
	}
	// One package, or a manager whose selection the guard read as a set of
	// packages it runs in turn: the work adds up.
	return commandDecision(scriptRunnerName(c), comps, 1, true, rd)
}

func scriptRunnerName(c shellcmd.Command) string {
	if n := c.NameBase(); n != "" {
		return n
	}
	return "package script"
}

// Workers.

func literalFlag(c shellcmd.Command, flag string) string {
	for i := 1; i < len(c.Args); i++ {
		if !c.Literal[i] {
			continue
		}
		a := c.Args[i]
		if a == flag && i+1 < len(c.Args) && c.Literal[i+1] {
			return c.Args[i+1]
		}
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"=")
		}
	}
	return ""
}

func hasNonLiteralFlag(c shellcmd.Command, flag string) bool {
	for i := 1; i < len(c.Args); i++ {
		a := c.Args[i]
		base, _, _ := strings.Cut(a, "=")
		if base != flag {
			continue
		}
		if !c.Literal[i] {
			return true
		}
		if a == flag && (i+1 >= len(c.Args) || !c.Literal[i+1]) {
			return true
		}
	}
	return false
}

// gateWorkers reads a gate's --jobs: a literal positive count, else the
// count the gate resolves for root itself. A non-literal count divides
// nothing the guard can name.
func gateWorkers(c shellcmd.Command, root string) (int, bool) {
	for i := 1; i < len(c.Args); i++ {
		a := c.Args[i]
		base, _, _ := strings.Cut(a, "=")
		if base != "--jobs" && base != "-j" {
			continue
		}
		if !c.Literal[i] {
			return 0, false
		}
		v := strings.TrimPrefix(a, base+"=")
		if v == "" || v == base {
			if i+1 >= len(c.Args) || !c.Literal[i+1] {
				return 0, false
			}
			v = c.Args[i+1]
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, false
		}
		return n, true
	}
	n, err := gate.ConfiguredJobs(root)
	return n, err == nil
}

// goWorkers reads `go test -p`, else the process parallelism Go would use.
// Only the exact -p option counts: -parallel and other -p* options set a
// different parallelism the gate workers do not divide by.
func goWorkers(c shellcmd.Command) (int, bool) {
	for i := 1; i < len(c.Args); i++ {
		a := c.Args[i]
		var v string
		var has bool
		switch {
		case a == "-p":
			if !c.Literal[i] {
				return 0, false
			}
			if i+1 >= len(c.Args) || !c.Literal[i+1] {
				return 0, false
			}
			v, has = c.Args[i+1], true
		case strings.HasPrefix(a, "-p="):
			if !c.Literal[i] {
				return 0, false
			}
			v, has = strings.TrimPrefix(a, "-p="), true
		case strings.HasPrefix(a, "-p") && !strings.Contains(a, "="):
			rest := strings.TrimPrefix(a, "-p")
			if rest == "" || !isDigits(rest) {
				continue // -parallel and other -p* options do not set workers
			}
			if !c.Literal[i] {
				return 0, false
			}
			v, has = rest, true
		default:
			continue
		}
		if !has {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, false
		}
		return n, true
	}
	if v := os.Getenv("GOMAXPROCS"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 1 {
			return n, true
		}
	}
	n := runtime.GOMAXPROCS(0)
	if n < 1 {
		n = runtime.NumCPU()
	}
	return n, true
}

// nxWorkers resolves Nx task parallelism: explicit literal flags, then known
// NX_PARALLEL, then nx.json, else the Nx default 3. Any unresolved override
// (non-literal flag value) yields unknown, never a silent 3.
func nxWorkers(c shellcmd.Command, root string) (int, bool) {
	literal, hasLiteral, unresolved := nxParallelFlags(c)
	if unresolved {
		return 0, false
	}
	if hasLiteral {
		return literal, true
	}
	if v := strings.TrimSpace(os.Getenv("NX_PARALLEL")); v != "" {
		if v == "false" || v == "0" {
			return 1, true
		}
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n, true
		}
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			return int(n), true
		}
		return 0, false
	}
	if n, ok := nxJSONParallel(root); ok {
		return n, true
	}
	return 3, true
}

func nxParallelFlags(c shellcmd.Command) (n int, has bool, unresolved bool) {
	for i := 1; i < len(c.Args); i++ {
		a := c.Args[i]
		lit := c.Literal[i]
		flag, val, hasVal := splitFlag(a)
		switch flag {
		case "--parallel", "--max-parallel", "--maxParallel", "--maxParallelWorkers":
			if !lit {
				return 0, false, true
			}
			if !hasVal {
				if flag == "--parallel" {
					// --parallel takes an optional value: a bare flag
					// uses the configured default and must not swallow
					// the next word, which names a target or project.
					if i+1 >= len(c.Args) {
						continue
					}
					if !c.Literal[i+1] {
						return 0, false, true
					}
					peek := c.Args[i+1]
					if peek != "true" && peek != "false" && !strings.HasSuffix(peek, "%") && !isDigits(peek) {
						continue
					}
					val, hasVal = peek, true
				} else if i+1 < len(c.Args) {
					if !c.Literal[i+1] {
						return 0, false, true
					}
					val, hasVal = c.Args[i+1], true
				}
			}
			if !hasVal || val == "" || val == "true" {
				continue
			}
			if val == "false" {
				return 1, true, false
			}
			if strings.HasSuffix(val, "%") {
				return 0, false, true
			}
			if parsed, err := strconv.Atoi(val); err == nil && parsed >= 1 {
				return parsed, true, false
			}
			return 0, false, true
		}
	}
	return 0, false, false
}

func splitFlag(a string) (flag, val string, hasVal bool) {
	if i := strings.Index(a, "="); i >= 0 {
		return a[:i], a[i+1:], true
	}
	return a, "", false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// intFlag reads a literal integer option value. present reports whether the
// flag appears; ok is false when a value is non-literal or invalid.
func intFlag(c shellcmd.Command, flags ...string) (n int, present bool, ok bool) {
	set := map[string]bool{}
	for _, f := range flags {
		set[f] = true
	}
	for i := 1; i < len(c.Args); i++ {
		a := c.Args[i]
		flag, val, hasVal := splitFlag(a)
		if !set[flag] {
			continue
		}
		if !c.Literal[i] {
			return 0, true, false
		}
		if !hasVal {
			if i+1 >= len(c.Args) || !c.Literal[i+1] {
				return 0, true, false
			}
			val = c.Args[i+1]
		}
		if val == "" || val == "true" {
			continue
		}
		parsed, err := strconv.Atoi(val)
		if err != nil || parsed < 1 {
			return 0, true, false
		}
		return parsed, true, true
	}
	return 0, false, true
}

// processWorkers resolves a runner's own concurrency from its configured
// flags, else the machine default the runner uses without one.
func processWorkers(c shellcmd.Command, runner string) (int, bool) {
	var flags []string
	switch runner {
	case "vitest-test":
		flags = []string{"--maxWorkers", "--max-workers"}
	case "playwright-test":
		flags = []string{"--workers"}
	case "node-test":
		flags = []string{"--test-concurrency"}
	default:
		return 0, false
	}
	n, present, ok := intFlag(c, flags...)
	if !ok {
		return 0, false
	}
	if !present {
		return defaultProcessWorkers(), true
	}
	return n, true
}

func defaultProcessWorkers() int {
	n := runtime.NumCPU()
	if n < 1 {
		n = 1
	}
	return n
}

// pnpmWorkers resolves pnpm workspace concurrency. --parallel is unbounded,
// --sequential forces 1, otherwise a literal --workspace-concurrency is used
// as the value states it. Without a literal setting the configured count is
// unknown, so the caller uses the longest component.
func pnpmWorkers(c shellcmd.Command) (int, bool) {
	for i := 1; i < len(c.Args); i++ {
		a := c.Args[i]
		flag, _, _ := splitFlag(a)
		if flag == "--parallel" || flag == "--workspace-concurrency" {
			if !c.Literal[i] {
				return 0, false
			}
		}
	}
	for i := 1; i < len(c.Args); i++ {
		if !c.Literal[i] {
			continue
		}
		switch a := c.Args[i]; a {
		case "--parallel":
			return 0, false // unbounded: no count the guard may divide by
		case "--sequential":
			return 1, true
		}
	}
	for i := 1; i < len(c.Args); i++ {
		if !c.Literal[i] {
			continue
		}
		a := c.Args[i]
		var v string
		switch {
		case strings.HasPrefix(a, "--workspace-concurrency="):
			v = strings.TrimPrefix(a, "--workspace-concurrency=")
		case a == "--workspace-concurrency":
			if i+1 < len(c.Args) && c.Literal[i+1] {
				v = c.Args[i+1]
			}
		default:
			continue
		}
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, false
		}
		if n > 0 {
			return n, true
		}
		// pnpm reads a negative value as a count of cores to leave idle.
		avail := runtime.NumCPU()
		if avail < 1 {
			avail = 1
		}
		if got := avail + n; got > 1 {
			return got, true
		}
		return 1, true
	}
	return 0, false
}

// nxJSONParallel reads the top-level parallel field of nx.json: a count, or
// false for one at a time. Nested parallel fields elsewhere in the document
// configure something else and are not read.
func nxJSONParallel(root string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(root, "nx.json"))
	if err != nil {
		return 0, false
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) != nil {
		return 0, false
	}
	raw, ok := doc["parallel"]
	if !ok {
		return 0, false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		if !b {
			return 1, true
		}
		return 3, true
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		if n >= 1 && n == float64(int(n)) {
			return int(n), true
		}
		return 0, false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		s = strings.TrimSpace(s)
		if s == "false" || s == "0" {
			return 1, true
		}
		if parsed, err := strconv.Atoi(s); err == nil && parsed >= 1 {
			return parsed, true
		}
	}
	return 0, false
}

// Command resolution helpers.

func hasNameFilter(c shellcmd.Command, flags []string) bool {
	set := map[string]bool{}
	for _, f := range flags {
		set[f] = true
	}
	for i := 1; i < len(c.Args); i++ {
		if !c.Literal[i] {
			continue
		}
		a := c.Args[i]
		flag, _, _ := splitFlag(a)
		if set[flag] || set[a] {
			return true
		}
	}
	return false
}

func nameFilterFlags(runner string) []string {
	switch runner {
	case "go test":
		return []string{"-run", "-skip"}
	case "dotnet test":
		return []string{"--filter"}
	case "vitest-test":
		return []string{"-t", "--testNamePattern", "--test-name-pattern", "--suite", "--changed", "--project", "--shard"}
	case "playwright-test":
		return []string{"-g", "--grep", "--grep-invert", "--grep-title", "--last-failed", "--only-changed", "--project", "--shard"}
	case "node-test":
		return []string{"--test-name-pattern", "--test-skip-pattern", "--test-only"}
	}
	return nil
}

// goUnits returns the package directories `go test` names, as the records
// carry them: repository-relative, slash-separated. A pattern the guard
// cannot map to directories, including a bare or absolute import path, is
// unknown rather than guessed.
func goUnits(c shellcmd.Command, dir, root string) ([]string, bool) {
	var pkgs []string
	inTest := false
	for i := 1; i < len(c.Args); i++ {
		if !c.Literal[i] {
			return nil, false
		}
		a := c.Args[i]
		if !inTest {
			if a == "test" {
				inTest = true
			}
			continue
		}
		if a == "--" {
			break // words after -- go to the test binary, not to go
		}
		if strings.HasPrefix(a, "-") {
			if strings.Contains(a, "=") || goBoolFlags[a] {
				continue
			}
			if goValueFlags[a] {
				i++
				continue
			}
			return nil, false
		}
		pkgs = append(pkgs, a)
	}
	if !inTest {
		return nil, false
	}
	if len(pkgs) == 0 {
		if dir == "" {
			return nil, false
		}
		return []string{relDir(dir, root)}, true
	}
	var units []string
	for _, p := range pkgs {
		if p == "." || p == "./" {
			if dir == "" {
				return nil, false
			}
			units = append(units, relDir(dir, root))
			continue
		}
		if p == "./..." || p == "..." {
			all, ok := goUnitsUnder(root, dir)
			if !ok {
				return nil, false
			}
			units = append(units, all...)
			continue
		}
		if rest, ok := strings.CutSuffix(p, "/..."); ok && (rest == "." || strings.HasPrefix(rest, "./") || strings.HasPrefix(rest, "../")) {
			all, ok := goWildcardUnits(rest, dir, root)
			if !ok {
				return nil, false
			}
			units = append(units, all...)
			continue
		}
		u, ok := goPackageDir(p, dir, root)
		if !ok {
			return nil, false
		}
		units = append(units, u)
	}
	if len(units) == 0 {
		return nil, false
	}
	return units, true
}

// goValueFlags are the `go test` options that take a separate word;
// goBoolFlags take none. An option on neither list makes the command
// unknown, because its operand would be read as a package.
var (
	goValueFlags = map[string]bool{
		"-p": true, "-parallel": true, "-run": true, "-skip": true, "-count": true,
		"-timeout": true, "-cpu": true, "-tags": true, "-bench": true, "-benchtime": true,
		"-coverprofile": true, "-outputdir": true, "-exec": true, "-coverpkg": true,
		"-args": true, "-fuzz": true, "-fuzztime": true, "-overlay": true, "-modfile": true,
		"-vet": true, "-asmflags": true, "-gcflags": true, "-ldflags": true,
		"-memprofile": true, "-memprofilerate": true, "-blockprofile": true,
		"-blockprofilerate": true, "-cpuprofile": true, "-mutexprofile": true,
		"-mutexprofilefraction": true, "-trace": true,
	}
	goBoolFlags = map[string]bool{
		"-v": true, "-race": true, "-cover": true, "-short": true, "-failfast": true,
		"-json": true, "-benchmem": true, "-x": true, "-work": true, "-a": true,
		"-n": true, "-msan": true, "-trimpath": true,
	}
)

// allGoUnits returns every directory the repository tracks a Go test in.
func allGoUnits(root string) ([]string, bool) {
	tracked, err := gitx.Tracked(root)
	if err != nil || len(tracked) == 0 {
		return nil, false
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range tracked {
		if !strings.HasSuffix(f, "_test.go") {
			continue
		}
		d := dirOf(f)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	sort.Strings(out)
	return out, true
}

// goUnitsUnder returns the test directories under dir: what ./... names
// from there. A bare ... names the same subtree in module mode.
func goUnitsUnder(root, dir string) ([]string, bool) {
	if dir == "" {
		return nil, false
	}
	all, ok := allGoUnits(root)
	if !ok {
		return nil, false
	}
	prefix := relDir(dir, root)
	if prefix == "." {
		return all, true
	}
	var out []string
	for _, u := range all {
		if u == prefix || strings.HasPrefix(u, prefix+"/") {
			out = append(out, u)
		}
	}
	return out, true
}

// goWildcardUnits returns the test directories under a ./x/... pattern the
// command names from dir. A bare import-path wildcard the guard cannot map
// stays unknown.
func goWildcardUnits(pattern, dir, root string) ([]string, bool) {
	var abs string
	switch {
	case pattern == ".":
		if dir == "" {
			return nil, false
		}
		abs = dir
	case strings.HasPrefix(pattern, "./"), strings.HasPrefix(pattern, "../"):
		if dir == "" {
			return nil, false
		}
		abs = filepath.Join(dir, filepath.FromSlash(pattern))
	default:
		return nil, false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, false
	}
	prefix := filepath.ToSlash(rel)
	all, ok := allGoUnits(root)
	if !ok {
		return nil, false
	}
	var out []string
	for _, u := range all {
		if u == prefix || strings.HasPrefix(u, prefix+"/") {
			out = append(out, u)
		}
	}
	return out, true
}

func goPackageDir(pkg, dir, root string) (string, bool) {
	clean := filepath.Clean(filepath.FromSlash(pkg))
	var abs string
	switch {
	case filepath.IsAbs(clean):
		abs = clean
	case pkg == ".":
		if dir == "" {
			return "", false
		}
		abs = dir
	case strings.HasPrefix(pkg, "./"), strings.HasPrefix(pkg, "../"):
		if dir == "" {
			return "", false
		}
		abs = filepath.Join(dir, clean)
	default:
		return "", false // a bare or absolute import path the guard cannot map
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// dotnetUnits returns the one project, or the several projects, a
// `dotnet test` names. A solution, an unrecognised option, or a word that is
// not a project file makes the command unknown: the records hold no
// measurement of a solution, and an option the guard misreads would be
// priced as a project.
func dotnetUnits(c shellcmd.Command, dir, root string) ([]string, bool) {
	var units []string
	inTest := false
	for i := 1; i < len(c.Args); i++ {
		if !c.Literal[i] {
			return nil, false
		}
		a := c.Args[i]
		if !inTest {
			if a == "test" {
				inTest = true
			}
			continue
		}
		if a == "--" {
			break // words after -- go to vstest, not to dotnet
		}
		if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "/") {
			if strings.ContainsAny(a, "=:") || dotnetBoolFlags[a] {
				continue
			}
			if dotnetValueFlags[a] {
				i++
				continue
			}
			return nil, false
		}
		u, ok := dotnetUnit(a, dir, root)
		if !ok {
			return nil, false
		}
		units = append(units, u)
	}
	if !inTest || len(units) == 0 {
		return nil, false
	}
	return units, true
}

// dotnetValueFlags take a separate word; dotnetBoolFlags take none.
var (
	dotnetValueFlags = map[string]bool{
		"-f": true, "--framework": true, "-v": true, "--verbosity": true,
		"-l": true, "--logger": true, "-c": true, "--configuration": true,
		"-o": true, "--output": true, "-r": true, "--runtime": true,
		"--results-directory": true, "--filter": true, "--collect": true,
		"-s": true, "--settings": true, "--diag": true, "--test-adapter-path": true,
		"-a": true, "--arch": true, "-e": true, "--environment": true, "--os": true,
		"--blame-crash-collect-always": true,
	}
	dotnetBoolFlags = map[string]bool{
		"--no-build": true, "--no-restore": true, "--nologo": true, "--list": true,
		"--blame": true, "--blame-hang": true, "--blame-crash": true, "--sp": true,
		"--treat-no-arguments": true, "--guessbuildonly": true, "--nodeReuse": true,
		"-h": true, "--help": true, "-?": true,
	}
)

func dotnetUnit(arg, dir, root string) (string, bool) {
	if !strings.HasSuffix(strings.ToLower(arg), ".csproj") {
		return "", false // a solution, a directory, or a dll the records do not carry
	}
	clean := filepath.Clean(filepath.FromSlash(arg))
	var abs string
	if filepath.IsAbs(clean) {
		abs = clean
	} else {
		if dir == "" {
			return "", false
		}
		abs = filepath.Join(dir, clean)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func dirOf(f string) string {
	if i := strings.LastIndex(f, "/"); i >= 0 {
		if i == 0 {
			return "."
		}
		return f[:i]
	}
	return "."
}
