// Package adapter runs checks through an external project adapter: a
// command configured in ytif-execution.yaml that discovers checks and runs
// them with the project's own orchestration.
//
// The adapter speaks JSON over stdio, with lowercase field names. The rail
// sends one request on stdin:
//
//	{version:1, operation:"discover"|"select"|"run", root, tracked,
//	 profile, gate, scope, files, inventory, selected?}
//
// and reads one JSON document from stdout; stderr carries progress, which
// the rail forwards to its log. A discover reply holds
// {checks:[{runner,unit,name}]}. A select reply holds {selected:[...]} and
// runs nothing: the rail validates the selection's runners, inventory
// membership, and placement before any side effects. A run reply holds
// {selected:[...], results:[{runner,unit,name,outcome,
// elapsedMs?,output?}], skipped?:[...],
// invocations?:[{runner,unit?,what,elapsedMs?,error?}]}; its request echoes
// the approved selection, which the adapter must run exactly. Outcomes are
// pass, fail, blocked, or cached; cached results carry no fresh check timing.
// elapsedMs accepts integers and fractions and
// rounds to the nearest millisecond. Whatever the adapter does not report
// stays without an outcome rather than becoming a pass, and a process
// failure with valid JSON keeps the partial results it parsed.
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gohost"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/proc"
	"github.com/undervoke/ytif/internal/runner/dotnet"
	"github.com/undervoke/ytif/internal/runner/gotest"
	"github.com/undervoke/ytif/internal/runner/jstest"
)

// protocolVersion is the only adapter protocol version the rail speaks.
const protocolVersion = 1

// Operations the rail requests.
const (
	opDiscover = "discover"
	opSelect   = "select"
	opRun      = "run"
)

// nativeRunners are the runners built-in sources own. A profile run may
// report their keys; discovery may not invent them.
var nativeRunners = []string{gohost.Runner, gotest.Runner, jstest.Bun, jstest.Node, dotnet.Runner}

// Source runs an external adapter command.
type Source struct {
	command []string
	runners []string
	// Profile selects the adapter's orchestration: "" for a full gate,
	// a ytif-execution.yaml profile name for a profile gate.
	Profile string
}

// New builds a source over command owning runners.
func New(command, runners []string, profile string) *Source {
	return &Source{command: command, runners: runners, Profile: profile}
}

// Runners lists the runners this source owns in full gates.
func (s *Source) Runners() []string { return s.runners }

// Claimed lists every runner whose keys a run of s may report: its
// declared runners, and in a profile run the native runners the profile
// orchestrates. Gates own every claimed runner with this source.
func (s *Source) Claimed() []string {
	out := append([]string(nil), s.runners...)
	if s.Profile != "" {
		out = append(out, nativeRunners...)
	}
	return out
}

// accept reports whether a response may name runner.
func (s *Source) accept(runner string) bool {
	for _, r := range s.Claimed() {
		if r == runner {
			return true
		}
	}
	return false
}

// request is one adapter invocation. Selected carries the approved
// selection in a run request; it is absent otherwise.
type request struct {
	Version   int               `json:"version"`
	Operation string            `json:"operation"`
	Root      string            `json:"root"`
	Tracked   []string          `json:"tracked"`
	Profile   string            `json:"profile"`
	Gate      string            `json:"gate,omitempty"`
	Scope     string            `json:"scope,omitempty"`
	Files     []string          `json:"files,omitempty"`
	Inventory []inventory.Entry `json:"inventory,omitempty"`
	Selected  []keyJSON         `json:"selected,omitempty"`
}

// keyJSON is one check reference.
type keyJSON struct {
	Runner string `json:"runner"`
	Unit   string `json:"unit"`
	Name   string `json:"name"`
}

func keyOf(k check.Key) keyJSON {
	return keyJSON{Runner: k.Runner, Unit: k.Unit, Name: k.Name}
}

func (k keyJSON) key() (check.Key, error) {
	if k.Runner == "" || k.Unit == "" || k.Name == "" {
		return check.Key{}, fmt.Errorf("check %+v: want non-empty runner, unit, and name", k)
	}
	return check.Key{Runner: k.Runner, Unit: k.Unit, Name: k.Name}, nil
}

// millis is elapsed milliseconds in a reply: an integer or a fraction,
// rounded to the nearest millisecond like the reporters that measure it.
type millis struct {
	MS int64
}

// UnmarshalJSON accepts an integer or fractional number of milliseconds.
// Fractions round half away from zero; negatives and non-numbers fail.
func (m *millis) UnmarshalJSON(data []byte) error {
	var f float64
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("elapsedMs %s: want a number of milliseconds", strings.TrimSpace(string(data)))
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("elapsedMs %s: want a finite number of milliseconds", strings.TrimSpace(string(data)))
	}
	if f < 0 {
		return fmt.Errorf("elapsedMs %s: want a non-negative number of milliseconds", strings.TrimSpace(string(data)))
	}
	m.MS = int64(math.Round(f))
	return nil
}

func (m *millis) duration() time.Duration {
	if m == nil {
		return 0
	}
	return time.Duration(m.MS) * time.Millisecond
}

// resultJSON is one reported check outcome.
type resultJSON struct {
	Runner    string        `json:"runner"`
	Unit      string        `json:"unit"`
	Name      string        `json:"name"`
	Outcome   check.Outcome `json:"outcome"`
	ElapsedMS *millis       `json:"elapsedMs,omitempty"`
	Output    string        `json:"output,omitempty"`
}

// invocationJSON is one process the adapter started.
type invocationJSON struct {
	Runner    string  `json:"runner"`
	Unit      string  `json:"unit,omitempty"`
	What      string  `json:"what"`
	ElapsedMS *millis `json:"elapsedMs,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// Discover asks the adapter for its checks.
func (s *Source) Discover(ctx context.Context, repo check.Repo) ([]check.Key, []check.Invocation, error) {
	req := request{Version: protocolVersion, Operation: opDiscover, Root: repo.Root, Tracked: repo.Tracked}
	data, err := s.invoke(ctx, repo, req)
	if err != nil {
		return nil, nil, err
	}
	var res struct {
		Checks *[]keyJSON `json:"checks"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, nil, fmt.Errorf("adapter %s: read discover reply: %w", s.name(), err)
	}
	if res.Checks == nil {
		return nil, nil, fmt.Errorf("adapter %s: discover reply holds no checks", s.name())
	}
	var keys []check.Key
	var errs []error
	for i, c := range *res.Checks {
		k, kerr := c.key()
		if kerr != nil {
			errs = append(errs, fmt.Errorf("adapter %s: checks[%d]: %w", s.name(), i, kerr))
			continue
		}
		if !s.accept(k.Runner) {
			errs = append(errs, fmt.Errorf("adapter %s: discovered %s, which is outside its declared runners", s.name(), k))
			continue
		}
		keys = append(keys, k)
	}
	return keys, nil, errors.Join(errs...)
}

// Select asks the adapter what it would run for in, without running
// anything. Gates preflight the selection before any side effects.
func (s *Source) Select(ctx context.Context, repo check.Repo, in check.Input) ([]check.Key, inventory.Inventory, error) {
	inv, err := inventory.LoadInventory(repo.Root)
	if err != nil {
		return nil, inventory.Inventory{}, err
	}
	req := selectRequest(repo, s.Profile, in, inv.Checks)
	data, runErr := s.invoke(ctx, repo, req)
	if runErr != nil && len(data) == 0 {
		return nil, inv, runErr
	}
	selected, serr := parseSelected(s.name(), s.accept, data)
	return selected, inv, errors.Join(serr, runErr)
}

// Preflight reports why selected must not run: an invalid placement, or a
// check placed after the gate. Unknown runners and duplicates already fail
// reply parsing; uninventoried keys pass through to reconciliation, which
// fails the gate without trusting an unknown check's placement. It runs
// nothing.
func Preflight(gate string, inv inventory.Inventory, selected []check.Key) error {
	place := map[check.Key]string{}
	for _, e := range inv.Checks {
		place[e.Key()] = e.Placement
	}
	rank := check.Rank(gate)
	var errs []error
	for _, k := range selected {
		p, ok := place[k]
		if !ok {
			continue
		}
		switch {
		case check.Rank(p) == 0:
			errs = append(errs, fmt.Errorf("selected %s with invalid placement %q", k, p))
		case check.Rank(p) > rank:
			errs = append(errs, fmt.Errorf("selected %s, placed at %s, for gate %s; place it at %s or earlier", k, p, gate, gate))
		}
	}
	return errors.Join(errs...)
}

// Run executes keys through the adapter: select, preflight, then the
// approved run. The adapter selects from the gate and inventory carried in
// the request; this returns its report, with any reply violations as the
// error so the gate keeps partial results and explains the checks left
// without one.
func (s *Source) Run(ctx context.Context, repo check.Repo, keys []check.Key, in check.Input) (check.Report, error) {
	_, rep, err := s.RunSelected(ctx, repo, in)
	return rep, err
}

// RunSelected selects, preflights, and runs, returning the selection with
// its report. Profile gates use the selection for reconciliation.
func (s *Source) RunSelected(ctx context.Context, repo check.Repo, in check.Input) ([]check.Key, check.Report, error) {
	selected, inv, err := s.Select(ctx, repo, in)
	if err != nil {
		return selected, check.Report{}, err
	}
	if err := Preflight(in.Gate, inv, selected); err != nil {
		return selected, check.Report{}, err
	}
	rep, err := s.runApproved(ctx, repo, in, inv.Checks, selected)
	return selected, rep, err
}

// runApproved runs exactly the approved selection: the run request echoes
// it, and a reply that selects anything else fails. Reply violations join
// into the error while the usable outcomes are kept.
func (s *Source) runApproved(ctx context.Context, repo check.Repo, in check.Input, inv []inventory.Entry, approved []check.Key) (check.Report, error) {
	req := selectRequest(repo, s.Profile, in, inv)
	req.Operation = opRun
	for _, k := range approved {
		req.Selected = append(req.Selected, keyOf(k))
	}
	data, runErr := s.invoke(ctx, repo, req)
	if runErr != nil && len(data) == 0 {
		return check.Report{}, runErr
	}
	selected, rep, verr := parseRun(s.name(), s.accept, data)
	if !sameKeys(selected, approved) {
		verr = errors.Join(verr, fmt.Errorf("adapter %s: run selection %v differs from the approved %v", s.name(), keysString(selected), keysString(approved)))
	}
	return rep, errors.Join(verr, runErr)
}

// selectRequest builds the select request; runApproved turns it into a run
// request by switching the operation and echoing the approved selection.
func selectRequest(repo check.Repo, profile string, in check.Input, inv []inventory.Entry) request {
	req := request{
		Version: protocolVersion, Operation: opSelect, Root: repo.Root, Tracked: repo.Tracked,
		Profile: profile, Gate: in.Gate, Scope: in.Scope, Files: in.Files, Inventory: inv,
	}
	if req.Tracked == nil {
		req.Tracked = []string{}
	}
	if req.Files == nil {
		req.Files = []string{}
	}
	return req
}

// parseSelected validates a select reply.
func parseSelected(name string, accept func(string) bool, data []byte) ([]check.Key, error) {
	var res struct {
		Selected *[]keyJSON `json:"selected"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("adapter %s: read select reply: %w", name, err)
	}
	if res.Selected == nil {
		return nil, fmt.Errorf("adapter %s: select reply holds no selected checks", name)
	}
	var selected []check.Key
	inSelected := map[check.Key]bool{}
	var errs []error
	for i, c := range *res.Selected {
		k, kerr := c.key()
		if kerr != nil {
			errs = append(errs, fmt.Errorf("adapter %s: selected[%d]: %v", name, i, kerr))
			continue
		}
		if !accept(k.Runner) {
			errs = append(errs, fmt.Errorf("adapter %s: selected %s: unknown runner %q", name, k, k.Runner))
			continue
		}
		if inSelected[k] {
			errs = append(errs, fmt.Errorf("adapter %s: selected %s twice", name, k))
			continue
		}
		inSelected[k] = true
		selected = append(selected, k)
	}
	return selected, errors.Join(errs...)
}

// parseRun validates a run reply and builds its report. Semantic violations
// join into the error while the usable outcomes are kept.
func parseRun(name string, accept func(string) bool, data []byte) ([]check.Key, check.Report, error) {
	var res struct {
		Selected    *[]keyJSON       `json:"selected"`
		Results     *[]resultJSON    `json:"results"`
		Skipped     []keyJSON        `json:"skipped"`
		Invocations []invocationJSON `json:"invocations"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, check.Report{}, fmt.Errorf("adapter %s: read run reply: %w", name, err)
	}
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("adapter %s: "+format, append([]any{name}, args...)...))
	}
	if res.Selected == nil {
		fail("run reply holds no selected checks")
	}
	if res.Results == nil {
		fail("run reply holds no results")
	}
	var selected []check.Key
	inSelected := map[check.Key]bool{}
	if res.Selected != nil {
		for i, c := range *res.Selected {
			k, kerr := c.key()
			if kerr != nil {
				fail("selected[%d]: %v", i, kerr)
				continue
			}
			if !accept(k.Runner) {
				fail("selected %s: unknown runner %q", k, k.Runner)
				continue
			}
			if inSelected[k] {
				fail("selected %s twice", k)
				continue
			}
			inSelected[k] = true
			selected = append(selected, k)
		}
	}
	var rep check.Report
	reported := map[check.Key]bool{}
	if res.Results != nil {
		for i, r := range *res.Results {
			k, kerr := keyJSON{Runner: r.Runner, Unit: r.Unit, Name: r.Name}.key()
			if kerr != nil {
				fail("results[%d]: %v", i, kerr)
				continue
			}
			if !accept(k.Runner) {
				fail("reported %s: unknown runner %q", k, k.Runner)
				continue
			}
			if !inSelected[k] {
				fail("reported %s, which it did not select", k)
				continue
			}
			if reported[k] {
				fail("reported %s more than once", k)
				continue
			}
			reported[k] = true
			out := check.Result{Key: k, Outcome: r.Outcome, Output: r.Output}
			switch r.Outcome {
			case check.Pass, check.Fail, check.Blocked, check.Cached:
			default:
				fail("reported %s with the unknown outcome %q", k, r.Outcome)
				continue
			}
			if r.ElapsedMS != nil && r.Outcome != check.Cached {
				out.Elapsed, out.Timed = r.ElapsedMS.duration(), true
			}
			rep.Results = append(rep.Results, out)
		}
	}
	for i, c := range res.Skipped {
		k, kerr := c.key()
		if kerr != nil {
			fail("skipped[%d]: %v", i, kerr)
			continue
		}
		if !accept(k.Runner) {
			fail("skipped %s: unknown runner %q", k, k.Runner)
			continue
		}
		if !inSelected[k] {
			fail("skipped %s, which it did not select", k)
			continue
		}
		if reported[k] {
			fail("skipped %s, which it also reported", k)
			continue
		}
		reported[k] = true
		rep.Skipped = append(rep.Skipped, k)
	}
	for i, v := range res.Invocations {
		if v.Runner == "" || v.What == "" {
			fail("invocations[%d]: want a non-empty runner and what", i)
			continue
		}
		if !accept(v.Runner) {
			fail("invocations[%d]: unknown runner %q", i, v.Runner)
			continue
		}
		inv := check.Invocation{Runner: v.Runner, Unit: v.Unit, What: v.What, Elapsed: v.ElapsedMS.duration()}
		if v.Error != "" {
			inv.Err = errors.New(v.Error)
		}
		rep.Invocations = append(rep.Invocations, inv)
	}
	return selected, rep, errors.Join(errs...)
}

// sameKeys reports whether a and b hold the same keys.
func sameKeys(a, b []check.Key) bool {
	if len(a) != len(b) {
		return false
	}
	in := map[check.Key]int{}
	for _, k := range a {
		in[k]++
	}
	for _, k := range b {
		if in[k] == 0 {
			return false
		}
		in[k]--
	}
	return true
}

func keysString(keys []check.Key) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.String()
	}
	return out
}

// invoke runs the adapter command with req on stdin. Stdout must be the
// reply JSON; stderr streams to the rail log. A process failure with a
// parsable reply keeps the reply: the caller joins the failure so partial
// results survive.
func (s *Source) invoke(ctx context.Context, repo check.Repo, req request) ([]byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if len(s.command) == 0 {
		return nil, fmt.Errorf("adapter %s: no command configured", s.name())
	}
	cmd := exec.CommandContext(ctx, s.command[0], s.command[1:]...)
	cmd.Dir = repo.Root
	cmd.Stdin = bytes.NewReader(body)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	log := repo.Log
	if log == nil {
		log = io.Discard
	}
	cmd.Stderr = log
	if err := proc.Run(cmd); err != nil {
		return bytes.TrimSpace(stdout.Bytes()), fmt.Errorf("adapter %s: %v", s.name(), err)
	}
	return bytes.TrimSpace(stdout.Bytes()), nil
}

func (s *Source) name() string {
	return strings.Join(s.command, " ")
}
