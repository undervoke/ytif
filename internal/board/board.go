// Package board renders the verification ledger and its records as one
// static HTML page: the verification list with the ledger's file tree, the
// relation diagram, the vocabulary, and the time and hit statistics the
// records support. The page makes no network requests; CI records reach it
// from the local cache.
package board

import (
	"embed"
	"encoding/json"
	"io/fs"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/cirecords"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/record"
	"github.com/undervoke/ytif/internal/storage"
)

//go:embed assets
var embedded embed.FS

// Assets holds the page source the binary ships: board.html, board.css,
// board.js, and icons.svg.
var Assets, _ = fs.Sub(embedded, "assets")

// DefaultPath is the board file inside the git common directory.
func DefaultPath(commonDir string) string {
	return filepath.Join(commonDir, "ytif", "board.html")
}

// GateCheck aliases the agent-invocable gate name for board display order.
const GateCheck = check.GateCheck

// gateOrder is the display order for gates.
var gateOrder = []string{check.GateCheck, check.GateCommit, check.GatePush, check.GateCI}

// Sources are the records and the branch the page shows: the local record
// lines, the CI lines read from the artifact cache, the checked-out branch,
// each CI attempt's branch, and the CI sync status. A nil Status leaves CI out
// of the page, as on a repository whose origin is not GitHub.
type Sources struct {
	Local        []record.Line
	Remote       []record.Line
	Branch       string
	RemoteBranch map[string]string
	Status       *cirecords.Status
	LocalStorage *storage.Pool
}

// Render builds the page from the assets, the verification ledger under root,
// and the records.
func Render(assets fs.FS, root string, src Sources, now time.Time) ([]byte, error) {
	names := []string{"board.html", "board.css", "board.js", "icons.svg"}
	files := map[string]string{}
	for _, name := range names {
		b, err := fs.ReadFile(assets, name)
		if err != nil {
			return nil, err
		}
		files[name] = string(b)
	}
	inv, err := inventory.LoadInventory(root)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(build(filepath.Base(root), inv, src, now))
	if err != nil {
		return nil, err
	}
	// json.Marshal escapes <, >, and &, so the data cannot close its script.
	out := strings.NewReplacer("/*STYLE*/", files["board.css"], "/*SCRIPT*/", files["board.js"], "/*ICONS*/", files["icons.svg"], "/*DATA*/", string(data)).Replace(files["board.html"])
	return []byte(out), nil
}

type label struct {
	Text string `json:"text,omitempty"`
	En   string `json:"en,omitempty"`
	Ko   string `json:"ko,omitempty"`
}

func toLabel(l inventory.Label) label {
	if l.ByLang == nil {
		return label{Text: l.Text}
	}
	return label{En: l.ByLang["en"], Ko: l.ByLang["ko"]}
}

type tagOut struct {
	Name  string `json:"name"`
	Label label  `json:"label"`
}

type groupOut struct {
	Name       string   `json:"name"`
	Heading    string   `json:"heading,omitempty"`
	Label      *label   `json:"label,omitempty"`
	Owner      string   `json:"owner"`
	ExactlyOne bool     `json:"exactly_one"`
	Tags       []tagOut `json:"tags"`
}

// meanOut is one gate's average and the samples it used, at most the gate's
// window.
type meanOut struct {
	MS int64 `json:"ms"`
	K  int   `json:"k"`
}

type verificationOut struct {
	Key        string             `json:"key"`
	Runner     string             `json:"runner"`
	Unit       string             `json:"unit"`
	Name       string             `json:"name"`
	Check      bool               `json:"check"`
	Commit     bool               `json:"commit"`
	SkipPush   bool               `json:"skip_push"`
	Gates      []string           `json:"gates"` // member gates in gateOrder
	IsCommand  bool               `json:"is_command"`
	Run        []string           `json:"run,omitempty"`
	Cwd        string             `json:"cwd,omitempty"`
	Exclusive  []string           `json:"exclusive,omitempty"`
	Origin     string             `json:"origin,omitempty"`
	File       string             `json:"file,omitempty"`
	Tags       []string           `json:"tags"`
	Requires   []string           `json:"requires"`
	Ensures    []string           `json:"ensures"`
	Accident   string             `json:"accident,omitempty"`
	Detection  string             `json:"detection,omitempty"`
	Impact     string             `json:"impact,omitempty"`
	DeleteWhen string             `json:"delete_when,omitempty"`
	Means      map[string]meanOut `json:"means"`
	Hits       map[string]int     `json:"hits"`
	OnBranch   bool               `json:"on_branch"`
	RecentFail bool               `json:"recent_fail"`
}

type processOut struct {
	Runner string             `json:"runner"`
	What   string             `json:"what"`
	Unit   string             `json:"unit"`
	Means  map[string]meanOut `json:"means"`
}

type fileOut struct {
	Path   string `json:"path"`
	Parent string `json:"parent,omitempty"`
}

type omissionOut struct {
	Key    string    `json:"key"`
	Reason string    `json:"reason"`
	Gate   string    `json:"gate"`
	Time   time.Time `json:"time"`
}

// gateAverageOut is one gate's row in the average total: the sum of its
// members' means, null when no member has a sample, and its hit total.
type gateAverageOut struct {
	Gate string `json:"gate"`
	MS   *int64 `json:"ms"`
	Hits int    `json:"hits"`
}

// gateBranchOut is one gate's row on the current branch: the timed results of
// its latest attempt there, null when none of them is timed.
type gateBranchOut struct {
	Gate string    `json:"gate"`
	MS   *int64    `json:"ms"`
	Time time.Time `json:"time"`
}

type summaryOut struct {
	Average []gateAverageOut `json:"average"`
	Branch  []gateBranchOut  `json:"branch"`
}

// spanOut is a record population's time range and its attempts per gate.
type spanOut struct {
	From     *time.Time     `json:"from,omitempty"`
	To       *time.Time     `json:"to,omitempty"`
	Attempts map[string]int `json:"attempts"`
}

// criteriaOut states the windows the page's figures use: the timed samples a
// mean keeps per gate and how fast their weight halves, the population a hit
// is counted in, and how far back a fail marks a verification.
type criteriaOut struct {
	Average        map[string]int `json:"average"`
	HalfLife       int            `json:"half_life_percent"`
	Hits           hitWindowOut   `json:"hits"`
	RecentFailDays int            `json:"recent_fail_days"`
	Scales         scalesOut      `json:"scales"`
}

type scalesOut struct {
	Unit         []int64 `json:"unit"`
	Verification []int64 `json:"verification"`
}

type hitWindowOut struct {
	Days int            `json:"days"`
	Last map[string]int `json:"last,omitempty"`
}

type ciOut struct {
	Repo     string        `json:"repo"`
	From     *time.Time    `json:"from,omitempty"`
	To       *time.Time    `json:"to,omitempty"`
	Attempts int           `json:"attempts"`
	Synced   *time.Time    `json:"synced,omitempty"`
	Pending  int           `json:"pending"`
	Status   string        `json:"status,omitempty"`
	Storage  *storage.Pool `json:"storage,omitempty"`
}

type metaOut struct {
	Repo         string        `json:"repo"`
	Version      int           `json:"version"`
	Generated    time.Time     `json:"generated"`
	Branch       string        `json:"branch"`
	Criteria     criteriaOut   `json:"criteria"`
	Local        spanOut       `json:"local"`
	CI           *ciOut        `json:"ci,omitempty"`
	LocalStorage *storage.Pool `json:"local_storage,omitempty"`
}

type boardData struct {
	Meta          metaOut           `json:"meta"`
	Groups        []groupOut        `json:"groups"`
	Files         []fileOut         `json:"files"`
	Verifications []verificationOut `json:"verifications"`
	Processes     []processOut      `json:"processes"`
	Summary       summaryOut        `json:"summary"`
	Omissions     []omissionOut     `json:"omissions"`
}

func build(repo string, inv inventory.Inventory, src Sources, now time.Time) boardData {
	settings := inv.Board.Settings()
	local := withoutGate(src.Local, check.GateCI)
	remote := onlyGate(src.Remote, check.GateCI)
	lines := append(append([]record.Line(nil), local...), remote...)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time.Before(lines[j].Time) })
	ix := newIndex(lines, src)

	d := boardData{
		Meta: metaOut{
			Repo: repo, Version: inv.Version, Generated: now.UTC(), Branch: src.Branch,
			Criteria:     criteriaOut{Average: settings.Average, HalfLife: settings.HalfLife, Hits: hitWindowOut{Days: settings.HitDays, Last: settings.HitLast}, RecentFailDays: settings.RecentFailDays, Scales: scalesOut{Unit: settings.Scales.Unit, Verification: settings.Scales.Verification}},
			Local:        spanOf(local),
			LocalStorage: src.LocalStorage,
		},
		Groups:        []groupOut{},
		Files:         files(inv),
		Verifications: []verificationOut{},
		Omissions:     omissions(lines),
	}
	if src.Status != nil {
		sp := spanOf(remote)
		d.Meta.CI = &ciOut{
			Repo: src.Status.Repo, From: sp.From, To: sp.To, Attempts: sp.Attempts[check.GateCI],
			Pending: src.Status.Pending, Status: src.Status.Kind,
			Storage: &src.Status.Storage,
		}
		if !src.Status.Synced.IsZero() {
			synced := src.Status.Synced.UTC()
			d.Meta.CI.Synced = &synced
		}
	}
	for _, g := range inventory.Builtin {
		d.Groups = append(d.Groups, group(g, "ytif"))
	}
	for _, g := range inv.Vocabulary {
		d.Groups = append(d.Groups, group(g, "project"))
	}

	means, procs := aggregate(lines, settings.Average, settings.HalfLife)
	d.Processes = procs
	hits := record.Hits(window(lines, now, settings))
	onBranch, recentFail := flags(lines, ix, src.Branch, now.AddDate(0, 0, -settings.RecentFailDays))

	for _, e := range inv.Verifications {
		k := e.Key().String()
		var gates []string
		for _, g := range gateOrder {
			if e.Member(g) {
				gates = append(gates, g)
			}
		}
		h := hits[k]
		if h == nil {
			h = map[string]int{}
		}
		d.Verifications = append(d.Verifications, verificationOut{
			Key: k, Runner: e.Runner, Unit: e.Key().Unit, Name: e.Name,
			Check: e.Check, Commit: e.Commit, SkipPush: e.SkipPush, Gates: gates,
			IsCommand: e.Runner == inventory.CommandRunner,
			Run:       append([]string(nil), e.Run...),
			Cwd:       e.Cwd, Exclusive: append([]string(nil), e.Exclusive...),
			Origin: e.Origin, File: e.File,
			Tags: nonNil(e.Tags), Requires: refs(e.Requires), Ensures: refs(e.Ensures),
			Accident: e.Accident, Detection: e.Detection, Impact: e.Impact, DeleteWhen: e.DeleteWhen,
			Means:      ofGates(means[k], gates),
			Hits:       h,
			OnBranch:   onBranch[k],
			RecentFail: recentFail[k],
		})
	}
	d.Summary = summarize(settings, d.Verifications, means, ix, src.Branch)
	return d
}

// gatesOf reports, per gate, the verifications that belong to it.
func gatesOf(vs []verificationOut) map[string][]verificationOut {
	out := map[string][]verificationOut{}
	for _, v := range vs {
		for _, g := range v.Gates {
			out[g] = append(out[g], v)
		}
	}
	return out
}

// summarize states each gate's average total and its total on the current
// branch. The time sums the current members, and the hits count every
// verification's catches at the gate, as its cells do. A gate with neither
// members nor hits has no row; a gate with no sampled member keeps its row
// with no time.
func summarize(s inventory.Settings, vs []verificationOut, means map[string]map[string]meanOut, ix index, branch string) summaryOut {
	out := summaryOut{Average: []gateAverageOut{}, Branch: []gateBranchOut{}}
	members := gatesOf(vs)
	for _, g := range gateOrder {
		count := 0
		for _, v := range vs {
			count += v.Hits[g]
		}
		member := members[g]
		if len(member) == 0 && count == 0 {
			continue
		}
		var sum int64
		sampled := false
		for _, v := range member {
			if m, ok := means[v.Key][g]; ok {
				sum += m.MS
				sampled = true
			}
		}
		row := gateAverageOut{Gate: g, Hits: count}
		if sampled {
			total := sum
			row.MS = &total
		}
		out.Average = append(out.Average, row)
	}
	if branch == "" {
		return out
	}
	for _, g := range gateOrder {
		attempt, ok := ix.latest(branch, g)
		if !ok {
			continue
		}
		row := gateBranchOut{Gate: g, Time: attempt.first}
		var sum int64
		timed := false
		for _, ms := range attempt.elapsed {
			sum += ms
			timed = true
		}
		if timed {
			total := sum
			row.MS = &total
		}
		out.Branch = append(out.Branch, row)
	}
	return out
}

// aggregate averages timed results per verification and gate, and timed
// invocations per process. Samples are already in time order.
func aggregate(lines []record.Line, limit map[string]int, halfLife int) (map[string]map[string]meanOut, []processOut) {
	type procID struct{ runner, what, unit string }
	samples := map[string]map[string][]int64{}
	procSamples := map[procID]map[string][]int64{}
	for _, l := range lines {
		switch l.Kind {
		case record.KindResult:
			if l.ElapsedMS == nil || l.Key == "" {
				continue
			}
			if samples[l.Key] == nil {
				samples[l.Key] = map[string][]int64{}
			}
			samples[l.Key][l.Gate] = append(samples[l.Key][l.Gate], *l.ElapsedMS)
		case record.KindInvocation:
			if l.ElapsedMS == nil {
				continue
			}
			id := procID{l.Runner, l.What, l.Unit}
			if procSamples[id] == nil {
				procSamples[id] = map[string][]int64{}
			}
			procSamples[id][l.Gate] = append(procSamples[id][l.Gate], *l.ElapsedMS)
		}
	}
	means := map[string]map[string]meanOut{}
	for key, byGate := range samples {
		means[key] = meansOf(byGate, limit, halfLife)
	}
	type process struct {
		processOut
		total int64
	}
	var procs []process
	for id, byGate := range procSamples {
		m := meansOf(byGate, limit, halfLife)
		p := process{processOut: processOut{Runner: id.runner, What: id.what, Unit: id.unit, Means: m}}
		for _, v := range m {
			p.total += v.MS
		}
		procs = append(procs, p)
	}
	sort.Slice(procs, func(i, j int) bool {
		if procs[i].total != procs[j].total {
			return procs[i].total > procs[j].total
		}
		if procs[i].Runner != procs[j].Runner {
			return procs[i].Runner < procs[j].Runner
		}
		if procs[i].Unit != procs[j].Unit {
			return procs[i].Unit < procs[j].Unit
		}
		return procs[i].What < procs[j].What
	})
	out := make([]processOut, 0, len(procs))
	for _, p := range procs {
		out = append(out, p.processOut)
	}
	return means, out
}

// meansOf averages the last n samples per gate, each weighted 0.5^(age/h)
// where h is halfLife percent of n, and keeps the count it used. h follows the
// window rather than the samples at hand, so a short history keeps the weights
// a full one gives its newest samples.
func meansOf(samples map[string][]int64, limit map[string]int, halfLife int) map[string]meanOut {
	out := map[string]meanOut{}
	for gate, vals := range samples {
		n := limit[gate]
		if n <= 0 {
			n = len(vals)
		}
		if len(vals) > n {
			vals = vals[len(vals)-n:]
		}
		h := float64(n) * float64(halfLife) / 100
		var sum, weight float64
		for i, v := range vals {
			w := math.Pow(0.5, float64(len(vals)-1-i)/h)
			sum += w * float64(v)
			weight += w
		}
		out[gate] = meanOut{MS: int64(math.Round(sum / weight)), K: len(vals)}
	}
	return out
}

// window keeps the lines hits are counted from: those inside the hit days, and
// per gate only the lines of its latest attempts when the ledger limits them.
// Lines keep their order, so the runs record.Hits follows stay intact.
func window(lines []record.Line, now time.Time, s inventory.Settings) []record.Line {
	from := now.AddDate(0, 0, -s.HitDays)
	kept := make([]record.Line, 0, len(lines))
	for _, l := range lines {
		if l.Time.Before(from) {
			continue
		}
		kept = append(kept, l)
	}
	if len(s.HitLast) == 0 {
		return kept
	}
	first := map[string]map[string]time.Time{}
	for _, l := range kept {
		if l.Attempt == "" {
			continue
		}
		if first[l.Gate] == nil {
			first[l.Gate] = map[string]time.Time{}
		}
		if t, ok := first[l.Gate][l.Attempt]; !ok || l.Time.Before(t) {
			first[l.Gate][l.Attempt] = l.Time
		}
	}
	keep := map[string]map[string]bool{}
	for gate, n := range s.HitLast {
		type attempt struct {
			id    string
			first time.Time
		}
		var all []attempt
		for id, t := range first[gate] {
			all = append(all, attempt{id, t})
		}
		sort.Slice(all, func(i, j int) bool {
			if !all[i].first.Equal(all[j].first) {
				return all[i].first.Before(all[j].first)
			}
			return all[i].id < all[j].id
		})
		if len(all) > n {
			all = all[len(all)-n:]
		}
		keep[gate] = map[string]bool{}
		for _, a := range all {
			keep[gate][a.id] = true
		}
	}
	out := make([]record.Line, 0, len(kept))
	for _, l := range kept {
		if attempt, limited := keep[l.Gate]; limited && l.Attempt != "" && !attempt[l.Attempt] {
			continue
		}
		out = append(out, l)
	}
	return out
}

// flags reports, per verification key, whether it has a result on the current
// branch and whether it failed at or after cutoff.
func flags(lines []record.Line, ix index, branch string, cutoff time.Time) (onBranch, recentFail map[string]bool) {
	onBranch, recentFail = map[string]bool{}, map[string]bool{}
	for _, l := range lines {
		if l.Kind != record.KindResult || l.Key == "" {
			continue
		}
		if branch != "" && ix.branch[l.Attempt] == branch {
			onBranch[l.Key] = true
		}
		if l.Outcome == string(check.Fail) && !l.Time.Before(cutoff) {
			recentFail[l.Key] = true
		}
	}
	return onBranch, recentFail
}

// index is what the lines say about each attempt: the branch it ran on, when
// it produced its first line, and its timed results.
type index struct {
	branch map[string]string    // attempt → branch
	gate   map[string]string    // attempt → gate
	first  map[string]time.Time // attempt → first line time
	timed  map[string][]int64   // attempt → timed result milliseconds
}

func newIndex(lines []record.Line, src Sources) index {
	ix := index{branch: map[string]string{}, gate: map[string]string{}, first: map[string]time.Time{}, timed: map[string][]int64{}}
	for a, b := range src.RemoteBranch {
		ix.branch[a] = b
	}
	for _, l := range lines {
		if l.Attempt == "" {
			continue
		}
		if t, ok := ix.first[l.Attempt]; !ok || l.Time.Before(t) {
			ix.first[l.Attempt] = l.Time
		}
		if l.Gate != "" {
			ix.gate[l.Attempt] = l.Gate
		}
		if l.Kind == record.KindSelection && l.Selection != nil && l.Selection.Branch != "" {
			ix.branch[l.Attempt] = l.Selection.Branch
		}
		if l.Kind == record.KindResult && l.ElapsedMS != nil {
			ix.timed[l.Attempt] = append(ix.timed[l.Attempt], *l.ElapsedMS)
		}
	}
	return ix
}

// latest returns the attempt of gate that ran last on branch. Two attempts
// that started at the same time resolve by id, so a page never moves between
// builds.
func (ix index) latest(branch, gate string) (attemptInfo, bool) {
	var best attemptInfo
	found := false
	for id, g := range ix.gate {
		if g != gate || ix.branch[id] != branch {
			continue
		}
		info := attemptInfo{id: id, first: ix.first[id], elapsed: ix.timed[id]}
		if !found || info.first.After(best.first) || info.first.Equal(best.first) && info.id < best.id {
			best, found = info, true
		}
	}
	return best, found
}

// attemptInfo is one attempt's start and the timed results it recorded.
type attemptInfo struct {
	id      string
	first   time.Time
	elapsed []int64
}

// spanOf reports the range and attempts per gate of one record population.
func spanOf(lines []record.Line) spanOut {
	s := spanOut{Attempts: map[string]int{}}
	seen := map[string]map[string]bool{}
	for _, l := range lines {
		switch l.Kind {
		case record.KindResult, record.KindInvocation, record.KindSelection:
		default:
			continue
		}
		t := l.Time
		if s.From == nil || t.Before(*s.From) {
			s.From = &t
		}
		if s.To == nil || t.After(*s.To) {
			s.To = &t
		}
		if l.Gate == "" || l.Attempt == "" {
			continue
		}
		if seen[l.Gate] == nil {
			seen[l.Gate] = map[string]bool{}
		}
		seen[l.Gate][l.Attempt] = true
	}
	for g, a := range seen {
		s.Attempts[g] = len(a)
	}
	return s
}

// omissions keeps the latest omission the records hold for each key.
func omissions(lines []record.Line) []omissionOut {
	latest := map[string]omissionOut{}
	for _, l := range lines {
		if l.Kind != record.KindSelection || l.Selection == nil {
			continue
		}
		for _, o := range l.Selection.Omissions {
			if cur, ok := latest[o.Key]; ok && !cur.Time.Before(l.Time) {
				continue
			}
			latest[o.Key] = omissionOut{Key: o.Key, Reason: o.Reason, Gate: l.Gate, Time: l.Time}
		}
	}
	out := make([]omissionOut, 0, len(latest))
	for _, o := range latest {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// withoutGate drops the lines a gate recorded.
func withoutGate(lines []record.Line, gate string) []record.Line {
	out := make([]record.Line, 0, len(lines))
	for _, l := range lines {
		if l.Gate != gate {
			out = append(out, l)
		}
	}
	return out
}

// onlyGate keeps the lines one gate recorded.
func onlyGate(lines []record.Line, gate string) []record.Line {
	out := make([]record.Line, 0, len(lines))
	for _, l := range lines {
		if l.Gate == gate {
			out = append(out, l)
		}
	}
	return out
}

// files lists the ledger files in visit order.
func files(inv inventory.Inventory) []fileOut {
	out := make([]fileOut, 0, len(inv.Files))
	for _, f := range inv.Files {
		out = append(out, fileOut{Path: f.Path, Parent: f.Parent})
	}
	return out
}

func group(g inventory.Group, owner string) groupOut {
	out := groupOut{Name: g.Name, Heading: g.Heading, Owner: owner, ExactlyOne: g.ExactlyOne, Tags: []tagOut{}}
	if owner == "ytif" {
		l := toLabel(g.Label)
		out.Label = &l
	}
	for _, t := range g.Tags {
		out.Tags = append(out.Tags, tagOut{Name: t.Name, Label: toLabel(t.Label)})
	}
	return out
}

func refs(rs []inventory.Ref) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, r.Key().String())
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ofGates keeps a verification's means at the gates it belongs to now, so its
// time agrees with the gate sums. Hits keep every gate: a catch stays evidence
// after the verification leaves the gate.
func ofGates(m map[string]meanOut, gates []string) map[string]meanOut {
	out := map[string]meanOut{}
	for _, g := range gates {
		if v, ok := m[g]; ok {
			out[g] = v
		}
	}
	return out
}
