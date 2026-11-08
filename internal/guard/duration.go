// Duration estimates for the agent guard.
//
// The guard refuses a recognized verification command only when its recorded
// elapsed time predicts it exceeds the ledger's guard limit. Unknown stays
// allowed and is never shown as cheap. A user grant (ytif allow) lifts the
// duration rule; it is read only after an over-limit estimate, from the
// actual executed repository. An agent's own ytif allow is always refused.
//
// Estimate rule (owner-accepted approximation): use applicable whole-command
// elapsed evidence first, undivided; otherwise aggregate the recorded
// durations of the verifications identified as part of the command as
// max(longest, sum/workers). Omitted work and estimation error may let an
// actual run exceed the limit. Threshold is inclusive for allow.
//
// Evidence is local managed records only, result and invocation lines via
// bounded Recent, run phase only, grouped by attempt, last 10 samples within
// 30 days per gate, largest gate mean. Single-run-invocation dotnet samples
// only. Process level (dotnet, go, vitest, node, playwright) uses run
// invocations; key level (node-verify, go-verify, command, nx-target) uses
// successful key results with failed-runner attempt exclusion. Serial
// sources sum first, then count as one gate-level component.
//
// Reading records costs a whole-repository scan, so the walk loads a
// repository's ledger first and touches its records only once a command
// actually needs elapsed evidence: an ordinary `ls` stops at the ledger.
package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/allow"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/record"
	"github.com/undervoke/ytif/internal/shellcmd"
)

const (
	sampleDays = 30
	sampleLast = 10
)

// durationDecision is one priced command.
type durationDecision struct {
	runner   string // short name for refusal records, e.g. "go test", "ytif ci"
	estimate time.Duration
	limit    time.Duration
	basis    string // numeric workload, samples, recency for the record detail
	known    bool   // false when no applicable evidence exists
}

// repoData is one executed repository's guard inputs. The ledger is cheap
// and loads with it; the records cost a whole-repository scan and load only
// when a command needs elapsed evidence.
type repoData struct {
	root, common string
	inv          *inventory.Inventory
	limit        time.Duration
	now          time.Time
	est          *estimator
}

// evidence returns the repository's record samples, reading its records on
// first use. A repository with no records yields an empty estimator, so a
// command priced from it stays unknown rather than falling back to a scan.
func (rd *repoData) evidence() *estimator {
	if rd.est == nil {
		lines, _, _ := record.Recent(rd.common, record.RecentMaxBytes)
		rd.est = newEstimator(lines, rd.now)
	}
	return rd.est
}

// accrual sums priced estimates per repository common directory.
type accrual struct {
	total map[string]time.Duration
	worst map[string]durationDecision
	bases map[string][]string
}

func (a *accrual) add(common string, d durationDecision) {
	if a.total == nil {
		a.total = map[string]time.Duration{}
		a.worst = map[string]durationDecision{}
		a.bases = map[string][]string{}
	}
	a.total[common] += d.estimate
	if d.estimate > a.worst[common].estimate {
		a.worst[common] = d
	}
	a.bases[common] = append(a.bases[common], d.basis)
}

// durationRefused prices the parsed command and reports whether it exceeds
// the limit: the refusing runner, the fixed model reason, the numeric record
// basis, and the common directory to record in. Unknown (no evidence,
// unreadable ledger or records, unparseable shape) allows.
func (g *guard) durationRefused(steps []shellcmd.Step) (runner, reason, basis, recCommon string) {
	now := time.Now()
	cache := map[string]*repoData{}
	acc := &accrual{}
	for _, pu := range walkSteps(steps, g.startDir(), "", false, func(dir string) *repoData {
		return g.repoFor(dir, cache, now)
	}) {
		est, ok := g.priceUnit(pu.u, pu.dir, pu.rd, 0)
		if ok && est.known {
			acc.add(pu.rd.common, est)
		}
	}
	for _, common := range g.overLimit(cache, acc) {
		if _, ok := allow.Until(common, now); ok {
			continue // the user lifted the limit for this repository
		}
		worst := acc.worst[common]
		return worst.runner, durationRefusal, strings.Join(acc.bases[common], "; "), common
	}
	return "", "", "", ""
}

// pricedUnit is one unit together with where the guard knows it runs.
type pricedUnit struct {
	u   shellcmd.Unit
	dir string
	rd  *repoData
}

// walkSteps locates work along supported success paths. A known failed cd
// stops its && chain; conditional directory changes leave later sequences
// unknown. Nested blocks share cwd, while subshells restore their parent's
// cwd. confine limits nested script evidence to the loaded repository.
func walkSteps(steps []shellcmd.Step, start, root string, confine bool, locate func(string) *repoData) []pricedUnit {
	move := followDir
	if confine {
		move = func(cur, target string) (string, bool) { return followDirIn(cur, target, root) }
	}
	var out []pricedUnit
	var walk func([]shellcmd.Step, string) (string, bool)
	walk = func(list []shellcmd.Step, cur string) (string, bool) {
		succeeded := true
		for _, st := range list {
			conditionalCd := false
			succeeded = true
			for i, u := range st.Units {
				if u.UnknownDir {
					cur = ""
					continue
				}
				if len(u.Nested) > 0 {
					next, ok := walk(u.Nested, cur)
					if !u.Isolated {
						if i > 0 && next != cur {
							conditionalCd = true
						}
						cur = next
					}
					if !ok {
						succeeded = false
						break
					}
					continue
				}
				if c, ok := singleCommand(u); ok && isDirCommand(c) {
					if target, literal := literalCdTarget(c); literal {
						if next, entered := move(cur, target); entered {
							cur = next
							conditionalCd = conditionalCd || i > 0
						} else {
							if i > 0 {
								cur = ""
							}
							succeeded = false
							break
						}
					} else {
						cur = ""
						succeeded = false
						break
					}
					continue
				}
				if cur != "" {
					if rd := locate(cur); rd != nil {
						out = append(out, pricedUnit{u: u, dir: cur, rd: rd})
					}
				}
			}
			if st.Alt || conditionalCd {
				cur = ""
			}
		}
		return cur, succeeded
	}
	walk(steps, start)
	return out
}

// overLimit lists the repositories whose own subtotal exceeds their own
// limit, most-exceeding first, so a grant in one repository cannot excuse a
// different repository the same command reaches.
func (g *guard) overLimit(cache map[string]*repoData, acc *accrual) []string {
	var out []string
	excess := map[string]time.Duration{}
	for common, total := range acc.total {
		rd := repoByCommon(cache, common)
		if rd == nil || total <= rd.limit {
			continue
		}
		out = append(out, common)
		excess[common] = total - rd.limit
	}
	sort.Slice(out, func(i, j int) bool {
		if excess[out[i]] != excess[out[j]] {
			return excess[out[i]] > excess[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// startDir returns the directory the command starts in, resolved the way Git
// reports repository paths.
func (g *guard) startDir() string {
	dir := g.dir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir
}

// repoFor locates the repository containing dir and loads its ledger. It
// returns nil when dir is unknown or unreadable, and never reads records.
func (g *guard) repoFor(dir string, cache map[string]*repoData, now time.Time) *repoData {
	if dir == "" {
		return nil
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	root, err := gitx.Root(dir)
	if err != nil {
		return nil
	}
	if rd, ok := cache[root]; ok {
		return rd
	}
	loaded, err := inventory.LoadInventory(root)
	if err != nil {
		cache[root] = nil
		return nil
	}
	common, err := gitx.CommonDir(root)
	if err != nil {
		cache[root] = nil
		return nil
	}
	rd := &repoData{root: root, common: common, inv: &loaded, limit: loaded.Guard.LimitDuration(), now: now}
	cache[root] = rd
	return rd
}

func repoByCommon(cache map[string]*repoData, common string) *repoData {
	for _, rd := range cache {
		if rd != nil && rd.common == common {
			return rd
		}
	}
	return nil
}

// priceUnit prices one unit: the best known pipeline stage, or its single
// simple command. A unit contributes at most one component, so a wrapper and
// the command it runs are never counted twice.
func (g *guard) priceUnit(u shellcmd.Unit, dir string, rd *repoData, depth int) (durationDecision, bool) {
	if depth > maxScriptDepth {
		return durationDecision{}, false
	}
	if len(u.Nested) > 0 {
		return g.priceSteps(u.Nested, dir, rd, depth)
	}
	if u.Piped {
		var best *durationDecision
		for _, stage := range u.Stages {
			if est, ok := g.priceUnit(stage, dir, rd, depth+1); ok && est.known {
				if best == nil || est.estimate > best.estimate {
					b := est
					best = &b
				}
			}
		}
		if best == nil {
			return durationDecision{}, false
		}
		return *best, true
	}
	return g.priceMany(u.Commands, rd, dir, depth)
}

// sumDecisions combines known sequential work, retaining the largest
// decision's metadata and the original order of its evidence.
func sumDecisions(decisions []durationDecision) (durationDecision, bool) {
	var total time.Duration
	var parts []string
	var worst durationDecision
	known := false
	for _, est := range decisions {
		if !est.known {
			continue
		}
		known = true
		total += est.estimate
		parts = append(parts, est.basis)
		if est.estimate > worst.estimate {
			worst = est
		}
	}
	if !known {
		return durationDecision{}, false
	}
	worst.estimate = total
	worst.basis = strings.Join(parts, " + ")
	return worst, true
}

// singleCommand reports the one command a unit runs, false for a pipeline.
func singleCommand(u shellcmd.Unit) (shellcmd.Command, bool) {
	if u.Piped || len(u.Commands) != 1 {
		return shellcmd.Command{}, false
	}
	return u.Commands[0], true
}

// isDirCommand reports a shell builtin that moves the working directory.
func isDirCommand(c shellcmd.Command) bool {
	if c.Chdir {
		return false
	}
	switch c.Name() {
	case "cd", "pushd", "popd":
		return true
	}
	return false
}

// literalCdTarget returns the directory a `cd` names, false when the target
// is not literal or the command is not a plain cd.
func literalCdTarget(c shellcmd.Command) (string, bool) {
	if c.Name() != "cd" || len(c.Args) != 2 || !c.Literal[1] {
		return "", false
	}
	if c.Args[1] == "-" || strings.HasPrefix(c.Args[1], "~") {
		return "", false
	}
	return c.Args[1], true
}

// followDir resolves a literal `cd` target from cur: an absolute target is
// used as is, a relative one joins cur. entered is false when the shell's cd
// fails there, which leaves the working directory where it was.
func followDir(cur, target string) (dir string, entered bool) {
	var abs string
	if filepath.IsAbs(target) {
		abs = filepath.Clean(target)
	} else {
		if cur == "" {
			return "", false
		}
		abs = filepath.Join(cur, filepath.FromSlash(target))
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		return "", false
	}
	if !searchable(abs) {
		return "", false
	}
	// Resolve symlinks (e.g. /tmp on macOS) so later comparisons against the
	// repository root, which git reports resolved, stay consistent.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	return resolved, true
}

// shBody returns the literal script a shell runs via `-c`, if any.
func shBody(c shellcmd.Command) (string, bool) {
	switch c.NameBase() {
	case "sh", "bash", "zsh", "dash", "ksh":
	default:
		return "", false
	}
	for i := 1; i < len(c.Args); i++ {
		if !c.Literal[i] {
			continue
		}
		if c.Args[i] == "-c" && i+1 < len(c.Args) && c.Literal[i+1] {
			return c.Args[i+1], true
		}
	}
	return "", false
}

// isHelp reports a command that only prints help or its version and runs no
// verification. A bare `help` counts only as the tool's first word: `go test
// -v` and `pytest -v` run tests, and a runner's own subcommand named help is
// resolved by that runner's recognizer.
func isHelp(c shellcmd.Command) bool {
	for i := 1; i < len(c.Args); i++ {
		if !c.Literal[i] {
			continue
		}
		switch c.Args[i] {
		case "-h", "--help", "-help", "--version":
			return true
		}
	}
	return len(c.Args) > 1 && c.Literal[1] && c.Args[1] == "help"
}

func formatDur(d time.Duration) string {
	d = d.Round(time.Second)
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if m == 0 {
		return fmt.Sprintf("%ds", s)
	}
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm%ds", m, s)
}
