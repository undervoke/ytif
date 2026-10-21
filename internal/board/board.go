// Package board renders the verification ledger and its records as one
// static HTML page: the verification list, a relation diagram, cost and
// hits, run selection context, and the vocabulary. The page makes no
// network requests.
package board

import (
	"embed"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/check"
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

// GateCheck aliases the agent-invocable gate name for board display order.
const GateCheck = check.GateCheck

// gateOrder is the display order for membership badges and cost totals.
var gateOrder = []string{check.GateCheck, check.GateCommit, check.GatePush, check.GateCI}

// Render builds the page from the assets, the verification ledger under
// root, and the record lines.
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
	Runs        int              `json:"runs"`
	Hits        int              `json:"hits"`
	TotalMS     int64            `json:"total_ms"`
	Timed       int              `json:"timed"`
	ByGate      map[string]int64 `json:"by_gate"`       // gate → elapsed ms, from history only
	ByGateTimed map[string]int   `json:"by_gate_timed"` // gate → timed outcomes, so unknown time renders as unknown
}

type verificationOut struct {
	Key        string      `json:"key"`
	Runner     string      `json:"runner"`
	Unit       string      `json:"unit"`
	Name       string      `json:"name"`
	Check      bool        `json:"check"`
	Commit     bool        `json:"commit"`
	SkipPush   bool        `json:"skip_push"`
	Gates      []string    `json:"gates"` // member gates in gateOrder
	IsCommand  bool        `json:"is_command"`
	Run        []string    `json:"run,omitempty"`
	Cwd        string      `json:"cwd,omitempty"`
	Exclusive  []string    `json:"exclusive,omitempty"`
	Origin     string      `json:"origin,omitempty"`
	Tags       []string    `json:"tags"`
	Requires   []string    `json:"requires"`
	Ensures    []string    `json:"ensures"`
	Accident   string      `json:"accident,omitempty"`
	Detection  string      `json:"detection,omitempty"`
	Impact     string      `json:"impact,omitempty"`
	DeleteWhen string      `json:"delete_when,omitempty"`
	Stats      *checkStats `json:"stats"`
}

type invocationOut struct {
	Runner  string `json:"runner"`
	What    string `json:"what"`
	Unit    string `json:"unit"`
	Runs    int    `json:"runs"`
	Timed   int    `json:"timed"`
	TotalMS int64  `json:"total_ms"`
}

type orphanOut struct {
	Key          string     `json:"key"`
	Runs         int        `json:"runs"`
	Hits         int        `json:"hits"`
	LastFail     *time.Time `json:"last_fail,omitempty"`
	LastFailGate string     `json:"last_fail_gate,omitempty"`
}

type selectionOut struct {
	Time    time.Time `json:"time"`
	Attempt string    `json:"attempt"`
	Gate    string    `json:"gate"`
	Context string    `json:"context,omitempty"`
	Outcome string    `json:"outcome,omitempty"`
	record.Selection
	// Known reports whether the record carried a selection payload. Old
	// records predate selection tracking and show mode unknown.
	Known bool `json:"known"`
}

type metaOut struct {
	Repo       string         `json:"repo"`
	Version    int            `json:"version"`
	Generated  time.Time      `json:"generated"`
	From       *time.Time     `json:"from,omitempty"`
	To         *time.Time     `json:"to,omitempty"`
	Attempts   map[string]int `json:"attempts"`   // gate → gate runs with results or selections
	Selections int            `json:"selections"` // selection records seen
}

type boardData struct {
	Meta          metaOut           `json:"meta"`
	Groups        []groupOut        `json:"groups"`
	Verifications []verificationOut `json:"verifications"`
	Selections    []selectionOut    `json:"selections"`
	Invocations   []invocationOut   `json:"invocations"`
	Orphans       []orphanOut       `json:"orphans"`
}

func build(repo string, inv inventory.Inventory, lines []record.Line, now time.Time) boardData {
	d := boardData{Meta: metaOut{Repo: repo, Version: inv.Version, Generated: now.UTC(), Attempts: map[string]int{}}}

	for _, g := range inventory.Builtin {
		d.Groups = append(d.Groups, group(g, "ytif"))
	}
	for _, g := range inv.Vocabulary {
		d.Groups = append(d.Groups, group(g, "project"))
	}

	stats, invs, orphans, selections := aggregate(lines, &d.Meta)
	d.Selections = selections
	listed := map[string]bool{}
	for _, e := range inv.Verifications {
		k := e.Key().String()
		listed[k] = true
		gates := []string{}
		for _, g := range gateOrder {
			if e.Member(g) {
				gates = append(gates, g)
			}
		}
		d.Verifications = append(d.Verifications, verificationOut{
			Key: k, Runner: e.Runner, Unit: e.Key().Unit, Name: e.Name,
			Check: e.Check, Commit: e.Commit, SkipPush: e.SkipPush, Gates: gates,
			IsCommand: e.Runner == inventory.CommandRunner,
			Run:       append([]string(nil), e.Run...),
			Cwd:       e.Cwd, Exclusive: append([]string(nil), e.Exclusive...),
			Origin: e.Origin,
			Tags:   nonNil(e.Tags), Requires: refs(e.Requires), Ensures: refs(e.Ensures),
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
// Costs come from history only, never from present-day membership: a gate
// total is the elapsed time its runs recorded. Selection lines share the
// run's attempt, time, gate and context, so a run with zero selections
// still counts as an attempt and an omission never becomes a pass or a
// zero-duration result.
func aggregate(lines []record.Line, meta *metaOut) (map[string]*checkStats, []invocationOut, map[string]*orphanOut, []selectionOut) {
	stats := map[string]*checkStats{}
	orphans := map[string]*orphanOut{}
	type invID struct{ runner, what, unit string }
	invs := map[invID]*invocationOut{}
	attempts := map[string]map[string]bool{}
	var selections []selectionOut
	seenSelections := map[string]bool{}
	type oldGroup struct {
		gate, context, attempt string
		first                  time.Time
		hasResult              bool
		hasFail                bool
	}
	olds := map[string]*oldGroup{}
	seenReal := 0

	ordered := append([]record.Line(nil), lines...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Time.Before(ordered[j].Time) })
	for _, l := range ordered {
		switch l.Kind {
		case record.KindResult, record.KindInvocation, record.KindSelection:
		default:
			continue
		}
		t := l.Time
		if meta.From == nil {
			meta.From = &t
		}
		meta.To = &t
		if l.Gate != "" && l.Attempt != "" {
			if attempts[l.Gate] == nil {
				attempts[l.Gate] = map[string]bool{}
			}
			attempts[l.Gate][l.Attempt] = true
		}
		if l.Kind == record.KindSelection {
			p := record.Selection{}
			ok := l.Selection != nil
			if ok {
				p = *l.Selection
			}
			if p.Eligible == nil {
				p.Eligible = []string{}
			}
			if p.Selected == nil {
				p.Selected = []string{}
			}
			if p.Omissions == nil {
				p.Omissions = []record.Omission{}
			}
			if p.Bases == nil {
				p.Bases = []string{}
			}
			if p.Tips == nil {
				p.Tips = []string{}
			}
			if p.MergeParents == nil {
				p.MergeParents = []string{}
			}
			selections = append(selections, selectionOut{
				Time: l.Time, Attempt: l.Attempt, Gate: l.Gate, Context: l.Context,
				Outcome: l.Outcome, Selection: p, Known: ok,
			})
			if l.Gate != "" {
				seenSelections[l.Gate+"\x00"+l.Context+"\x00"+l.Attempt] = true
			}
			seenReal++
			continue
		}
		if l.Gate != "" && l.Attempt != "" && (l.Kind == record.KindResult || l.Kind == record.KindInvocation) {
			k := l.Gate + "\x00" + l.Context + "\x00" + l.Attempt
			g := olds[k]
			if g == nil {
				g = &oldGroup{gate: l.Gate, context: l.Context, attempt: l.Attempt, first: l.Time}
				olds[k] = g
			} else if l.Time.Before(g.first) {
				g.first = l.Time
			}
			if l.Kind == record.KindResult {
				g.hasResult = true
				if l.Outcome == string(check.Fail) {
					g.hasFail = true
				}
			}
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
				v.Timed++
				v.TotalMS += *l.ElapsedMS
			}
			continue
		}
		s := stats[l.Key]
		if s == nil {
			s = &checkStats{ByGate: map[string]int64{}, ByGateTimed: map[string]int{}}
			stats[l.Key] = s
		}
		s.Runs++
		if l.ElapsedMS != nil {
			s.TotalMS += *l.ElapsedMS
			s.Timed++
			s.ByGate[l.Gate] += *l.ElapsedMS
			s.ByGateTimed[l.Gate]++
		}
		o := orphans[l.Key]
		if o == nil {
			o = &orphanOut{Key: l.Key}
			orphans[l.Key] = o
		}
		o.Runs++
		if l.Outcome == string(check.Fail) {
			o.LastFail, o.LastFailGate = &t, l.Gate
		}
	}
	for g, as := range attempts {
		meta.Attempts[g] = len(as)
	}
	meta.Selections = seenReal
	// Synthesize one Known:false row per old gate/context/attempt that has
	// results or invocations but no selection line, so pre-selection history
	// shows mode unknown instead of vanishing. Attempts that already carry
	// a selection line are never duplicated.
	for k, g := range olds {
		if seenSelections[k] {
			continue
		}
		outcome := ""
		if g.hasFail {
			outcome = string(check.Fail)
		} else if g.hasResult {
			outcome = string(check.Pass)
		}
		selections = append(selections, selectionOut{
			Time: g.first, Attempt: g.attempt, Gate: g.gate, Context: g.context,
			Outcome: outcome,
			Selection: record.Selection{
				Eligible: []string{}, Selected: []string{}, Omissions: []record.Omission{},
				Bases: []string{}, Tips: []string{}, MergeParents: []string{},
			},
			Known: false,
		})
	}
	sort.SliceStable(selections, func(i, j int) bool { return selections[i].Time.Before(selections[j].Time) })

	for k, n := range record.Hits(lines) {
		if s := stats[k]; s != nil {
			s.Hits = n
		}
		if o := orphans[k]; o != nil {
			o.Hits = n
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
	return stats, out, orphans, selections
}
