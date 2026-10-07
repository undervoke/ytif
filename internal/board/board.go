// Package board renders the inventory and its records as one static HTML
// page: the check list, a relation diagram, cost and hits, and the
// vocabulary. The page makes no network requests.
package board

import (
	"embed"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/record"
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

// Render builds the page from the assets, the inventory under root, and the
// record lines.
func Render(assets fs.FS, root string, lines []record.Line, now time.Time) ([]byte, error) {
	src := map[string]string{}
	for _, name := range []string{"board.html", "board.css", "board.js", "icons.svg"} {
		b, err := fs.ReadFile(assets, name)
		if err != nil {
			return nil, err
		}
		src[name] = string(b)
	}
	inv, err := inventory.LoadInventory(root)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(build(filepath.Base(root), inv, lines, now))
	if err != nil {
		return nil, err
	}
	// json.Marshal escapes <, >, and &, so the data cannot close its script.
	out := strings.NewReplacer("/*STYLE*/", src["board.css"], "/*SCRIPT*/", src["board.js"], "/*ICONS*/", src["icons.svg"], "/*DATA*/", string(data)).Replace(src["board.html"])
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

type checkStats struct {
	Runs    int              `json:"runs"`
	Hits    int              `json:"hits"`
	TotalMS int64            `json:"total_ms"`
	Timed   int              `json:"timed"`
	ByGate  map[string]int64 `json:"by_gate"` // gate → elapsed ms
	// Outcome counts. Runs is their sum over every known and unknown
	// outcome; a check with no result lines has no stats at all, which the
	// board shows as never run rather than passed.
	Pass    int `json:"pass"`
	Fail    int `json:"fail"`
	Blocked int `json:"blocked"`
	Skip    int `json:"skip"`
	Cached  int `json:"cached"`
	// Latest observed result and where it ran. Empty commit, worktree, and
	// profile mean unknown: a record written before provenance existed,
	// shown by the board as historical.
	LastOutcome  string     `json:"last_outcome,omitempty"`
	LastDetail   string     `json:"last_detail,omitempty"`
	LastTime     *time.Time `json:"last_time,omitempty"`
	LastGate     string     `json:"last_gate,omitempty"`
	LastCommit   string     `json:"last_commit,omitempty"`
	LastWorktree string     `json:"last_worktree,omitempty"`
	LastProfile  string     `json:"last_profile,omitempty"`
}

type checkOut struct {
	Key        string      `json:"key"`
	Runner     string      `json:"runner"`
	Unit       string      `json:"unit"`
	Name       string      `json:"name"`
	Placement  string      `json:"placement"`
	Tags       []string    `json:"tags"`
	Requires   []string    `json:"requires"`
	Ensures    []string    `json:"ensures"`
	Accident   string      `json:"accident"`
	Detection  string      `json:"detection"`
	Impact     string      `json:"impact"`
	DeleteWhen string      `json:"delete_when"`
	Stats      *checkStats `json:"stats"`
}

type invocationOut struct {
	Runner  string `json:"runner"`
	What    string `json:"what"`
	Unit    string `json:"unit"`
	Runs    int    `json:"runs"`
	TotalMS int64  `json:"total_ms"`
}

type orphanOut struct {
	Key          string     `json:"key"`
	Runs         int        `json:"runs"`
	Hits         int        `json:"hits"`
	LastOutcome  string     `json:"last_outcome,omitempty"`
	LastTime     *time.Time `json:"last_time,omitempty"`
	LastGate     string     `json:"last_gate,omitempty"`
	LastCommit   string     `json:"last_commit,omitempty"`
	LastWorktree string     `json:"last_worktree,omitempty"`
	LastProfile  string     `json:"last_profile,omitempty"`
	LastFail     *time.Time `json:"last_fail,omitempty"`
	LastFailGate string     `json:"last_fail_gate,omitempty"`
}

type metaOut struct {
	Repo      string         `json:"repo"`
	Version   int            `json:"version"`
	Generated time.Time      `json:"generated"`
	From      *time.Time     `json:"from,omitempty"`
	To        *time.Time     `json:"to,omitempty"`
	Attempts  map[string]int `json:"attempts"` // gate → gate runs with results
}

type boardData struct {
	Meta        metaOut         `json:"meta"`
	Groups      []groupOut      `json:"groups"`
	Checks      []checkOut      `json:"checks"`
	Invocations []invocationOut `json:"invocations"`
	Orphans     []orphanOut     `json:"orphans"`
}

func build(repo string, inv inventory.Inventory, lines []record.Line, now time.Time) boardData {
	d := boardData{Meta: metaOut{Repo: repo, Version: inv.Version, Generated: now.UTC(), Attempts: map[string]int{}}}

	for _, g := range inventory.Builtin {
		d.Groups = append(d.Groups, group(g, "ytif"))
	}
	for _, g := range inv.Vocabulary {
		d.Groups = append(d.Groups, group(g, "project"))
	}

	stats, invs, orphans := aggregate(lines, &d.Meta)
	listed := map[string]bool{}
	for _, e := range inv.Checks {
		k := e.Key().String()
		listed[k] = true
		d.Checks = append(d.Checks, checkOut{
			Key: k, Runner: e.Runner, Unit: e.Unit, Name: e.Name, Placement: e.Placement,
			Tags: nonNil(e.Tags), Requires: refs(e.Requires), Ensures: refs(e.Ensures),
			Accident: e.Accident, Detection: e.Detection, Impact: e.Impact, DeleteWhen: e.DeleteWhen, Stats: stats[k],
		})
	}
	d.Invocations = invs
	for k, o := range orphans {
		if !listed[k] {
			d.Orphans = append(d.Orphans, *o)
		}
	}
	sort.Slice(d.Orphans, func(i, j int) bool { return d.Orphans[i].Key < d.Orphans[j].Key })
	return d
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

// aggregate sums result and invocation lines over every gate and context.
// Hits come from record.Stats, so the board and ytif stats count alike.
// Per-check stats derive ONLY from KindResult lines: an invocation's success
// never invents a pass, so a check with no result lines keeps nil stats and
// the board shows it as never run. Timings cover only lines that carry
// elapsed_ms; skip and cached outcomes must not carry one.
func aggregate(lines []record.Line, meta *metaOut) (map[string]*checkStats, []invocationOut, map[string]*orphanOut) {
	stats := map[string]*checkStats{}
	orphans := map[string]*orphanOut{}
	type invID struct{ runner, what, unit string }
	invs := map[invID]*invocationOut{}
	attempts := map[string]map[string]bool{}

	ordered := append([]record.Line(nil), lines...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Time.Before(ordered[j].Time) })
	for _, l := range ordered {
		switch l.Kind {
		case record.KindResult, record.KindInvocation:
		case record.KindDispatch:
			if l.Key == "" {
				continue
			}
			l.Outcome = record.OutcomeBlocked
		default:
			continue
		}
		t := l.Time
		if meta.From == nil {
			meta.From = &t
		}
		meta.To = &t
		if l.Gate != "" {
			if attempts[l.Gate] == nil {
				attempts[l.Gate] = map[string]bool{}
			}
			attempts[l.Gate][l.Attempt] = true
		}
		if l.Kind == record.KindInvocation {
			id := invID{l.Runner, l.What, l.Unit}
			v := invs[id]
			if v == nil {
				v = &invocationOut{Runner: l.Runner, What: l.What, Unit: l.Unit}
				invs[id] = v
			}
			v.Runs++
			if l.ElapsedMS != nil {
				v.TotalMS += *l.ElapsedMS
			}
			continue
		}
		s := stats[l.Key]
		if s == nil {
			s = &checkStats{ByGate: map[string]int64{}}
			stats[l.Key] = s
		}
		s.Runs++
		switch l.Outcome {
		case record.OutcomePass:
			s.Pass++
		case record.OutcomeFail:
			s.Fail++
		case record.OutcomeBlocked:
			s.Blocked++
		case record.OutcomeSkip:
			s.Skip++
		case record.OutcomeCached:
			s.Cached++
		}
		s.LastOutcome, s.LastGate = l.Outcome, l.Gate
		s.LastDetail = l.Detail
		s.LastTime = &t
		s.LastCommit, s.LastWorktree, s.LastProfile = l.Commit, l.Worktree, l.Profile
		if l.ElapsedMS != nil {
			s.TotalMS += *l.ElapsedMS
			s.Timed++
			s.ByGate[l.Gate] += *l.ElapsedMS
		}
		o := orphans[l.Key]
		if o == nil {
			o = &orphanOut{Key: l.Key}
			orphans[l.Key] = o
		}
		o.Runs++
		o.LastOutcome, o.LastGate = l.Outcome, l.Gate
		o.LastTime = &t
		o.LastCommit, o.LastWorktree, o.LastProfile = l.Commit, l.Worktree, l.Profile
		if l.Outcome == record.OutcomeFail {
			o.LastFail, o.LastFailGate = &t, l.Gate
		}
	}
	for g, as := range attempts {
		meta.Attempts[g] = len(as)
	}

	checks, _, _ := record.Stats(lines, record.Filter{}, 0)
	for _, c := range checks {
		if s := stats[c.Key]; s != nil {
			s.Hits += c.Hits
		}
		if o := orphans[c.Key]; o != nil {
			o.Hits += c.Hits
		}
	}

	out := []invocationOut{}
	for _, v := range invs {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalMS != out[j].TotalMS {
			return out[i].TotalMS > out[j].TotalMS
		}
		return out[i].Unit < out[j].Unit
	})
	return stats, out, orphans
}
