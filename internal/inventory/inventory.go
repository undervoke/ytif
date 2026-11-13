// Package inventory reads the verification ledger and the gate exemptions.
//
// The verifications ledger records contracts and gate membership, split
// across a root file and optionally included fragments. The exemptions
// list gate entries running outside ytif, with their reasons.
package inventory

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/undervoke/ytif/internal/check"
)

const (
	// InventoryFile is the root verification ledger.
	InventoryFile = "ytif-verifications.yaml"
	// LegacyInventoryFile is the previous single-file ledger. LoadInventory
	// refuses to run with it and points at Migrate instead.
	LegacyInventoryFile = "ytif-inventory.yaml"
	// ExemptionsFile lists gate entries running outside ytif.
	ExemptionsFile = "ytif-gate-exemptions.yaml"

	inventoryVersion  = 4
	exemptionsVersion = 2

	// CommandRunner is the runner name for project-owned direct commands
	// folded into the verification list.
	CommandRunner = "command"
	// NxTargetRunner is the runner name for Nx targets marked as verifications.
	NxTargetRunner = "nx-target"
)

// Inventory is the verification ledger: shared configuration from the root
// file plus the merged verification list. NxTargets enrolls the nx-target
// runner. Only the root file carries it, so losing a fragment cannot switch
// off the discovery that reports the loss.
// Guard carries the agent-guard duration limit. Only the root file carries
// it, like NxTargets.
type Inventory struct {
	Version       int          `yaml:"version"`
	Gate          GateConfig   `yaml:"gate"`
	Vocabulary    Vocabulary   `yaml:"vocabulary,omitempty"`
	Prepare       []Prep       `yaml:"prepare,omitempty"`
	Board         BoardConfig  `yaml:"board,omitempty"`
	Guard         GuardConfig  `yaml:"guard,omitempty"`
	NxTargets     bool         `yaml:"nx-targets,omitempty"`
	Includes      []string     `yaml:"includes,omitempty"`
	Verifications []Entry      `yaml:"verifications"`
	Files         []LedgerFile `yaml:"-"`
}

// GuardConfig sets the agent-guard duration limit. Zero keeps the default,
// so a ledger states only what differs.
type GuardConfig struct {
	Limit string `yaml:"limit,omitempty"`
}

// DefaultGuardLimit is the duration estimate above which the agent guard
// refuses a verification command.
const DefaultGuardLimit = 2 * time.Minute

// LimitDuration returns the configured guard limit, or the default when unset.
func (g GuardConfig) LimitDuration() time.Duration {
	if strings.TrimSpace(g.Limit) == "" {
		return DefaultGuardLimit
	}
	d, err := time.ParseDuration(strings.TrimSpace(g.Limit))
	if err != nil || d <= 0 {
		return DefaultGuardLimit
	}
	return d
}

func (g GuardConfig) validate(display string) error {
	if strings.TrimSpace(g.Limit) == "" {
		return nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(g.Limit))
	if err != nil {
		return fmt.Errorf("%s: guard.limit %q: want a duration like 2m", display, g.Limit)
	}
	if d <= 0 {
		return fmt.Errorf("%s: guard.limit %q: want a positive duration", display, g.Limit)
	}
	return nil
}

// BoardConfig sets the statistics windows the board uses. Zero values keep
// the defaults, so a ledger states only what differs.
type BoardConfig struct {
	Average         GateCounts `yaml:"average,omitempty"`
	HalfLifePercent int        `yaml:"half_life_percent,omitempty"`
	Hits            HitWindow  `yaml:"hits,omitempty"`
	RecentFailDays  int        `yaml:"recent_fail_days,omitempty"`
	Scales          Scales     `yaml:"scales,omitempty"`
}

// Scales are the tick values, in milliseconds, that the board's time bars
// are drawn against: each interval between neighbors takes an equal width.
// Unit draws group and process totals; verification draws single
// verifications.
type Scales struct {
	Unit         []int64 `yaml:"unit,omitempty"`
	Verification []int64 `yaml:"verification,omitempty"`
}

// GateCounts counts something per gate. Strict decoding rejects a misnamed
// gate, so a typo cannot silently keep the default.
type GateCounts struct {
	Commit int `yaml:"commit,omitempty"`
	Push   int `yaml:"push,omitempty"`
	CI     int `yaml:"ci,omitempty"`
}

// HitWindow bounds the population a hit is counted in: how many days it
// reaches back, and per gate how many of that gate's latest attempts count.
type HitWindow struct {
	Days int        `yaml:"days,omitempty"`
	Last GateCounts `yaml:"last,omitempty"`
}

// Settings are the board windows with the defaults applied.
type Settings struct {
	Average        map[string]int // gate → timed samples the mean uses
	HalfLife       int            // percent of a gate's average window over which a sample's weight halves
	HitDays        int            // how far back a hit is counted
	HitLast        map[string]int // gate → latest attempts a hit is counted in
	RecentFailDays int            // how far back a fail marks a verification
	Scales         Scales         // time bar ticks
}

// Defaults for the board windows.
const (
	defaultAverage   = 100 // timed samples per verification and gate
	defaultCIAverage = 10  // ci runs cost orders of magnitude more
	defaultHalfLife  = 50  // percent of the average window
	defaultHitDays   = 365
	defaultFailDays  = 30
)

// Default time bar ticks: 0 to 1,000s for a verification, 0 to 10,000s for a total.
var (
	defaultVerificationScale = []int64{0, 100, 1_000, 10_000, 100_000, 1_000_000}
	defaultUnitScale         = []int64{0, 10_000, 100_000, 1_000_000, 10_000_000}
)

// Settings resolves the board windows. An unset average is 100 timed samples
// per gate and 10 for ci, an unset half-life 50 percent of that window, an
// unset hit window 365 days, an unset per-gate hit
// limit counts every attempt, and an unset fail window 30 days.
func (b BoardConfig) Settings() Settings {
	s := Settings{Average: map[string]int{}, HalfLife: b.HalfLifePercent, HitDays: b.Hits.Days, HitLast: map[string]int{}, RecentFailDays: b.RecentFailDays, Scales: b.Scales}
	if s.HalfLife == 0 {
		s.HalfLife = defaultHalfLife
	}
	if len(s.Scales.Verification) == 0 {
		s.Scales.Verification = defaultVerificationScale
	}
	if len(s.Scales.Unit) == 0 {
		s.Scales.Unit = defaultUnitScale
	}
	for gate, n := range b.Average.byGate() {
		if n == 0 {
			n = defaultAverage
			if gate == check.GateCI {
				n = defaultCIAverage
			}
		}
		s.Average[gate] = n
	}
	if s.HitDays == 0 {
		s.HitDays = defaultHitDays
	}
	if s.RecentFailDays == 0 {
		s.RecentFailDays = defaultFailDays
	}
	for gate, n := range b.Hits.Last.byGate() {
		if n > 0 {
			s.HitLast[gate] = n
		}
	}
	return s
}

func (g GateCounts) byGate() map[string]int {
	return map[string]int{
		check.GateCommit: g.Commit,
		check.GatePush:   g.Push,
		check.GateCI:     g.CI,
	}
}

// validate rejects negative windows: a count of samples or days cannot be
// negative, and treating one as unset would hide a typo.
func (b BoardConfig) validate() error {
	for _, f := range []struct {
		field string
		g     GateCounts
	}{{"average", b.Average}, {"hits: last", b.Hits.Last}} {
		for _, n := range []struct {
			gate string
			v    int
		}{{"commit", f.g.Commit}, {"push", f.g.Push}, {"ci", f.g.CI}} {
			if n.v < 0 {
				return fmt.Errorf("%s: %s %d: want 0 or more", f.field, n.gate, n.v)
			}
		}
	}
	if b.HalfLifePercent < 0 {
		return fmt.Errorf("half_life_percent %d: want 0 or more", b.HalfLifePercent)
	}
	if b.Hits.Days < 0 {
		return fmt.Errorf("hits: days %d: want 0 or more", b.Hits.Days)
	}
	if b.RecentFailDays < 0 {
		return fmt.Errorf("recent_fail_days %d: want 0 or more", b.RecentFailDays)
	}
	// a bar position divides by each interval, so ticks must rise
	for name, ticks := range map[string][]int64{"unit": b.Scales.Unit, "verification": b.Scales.Verification} {
		if len(ticks) == 0 {
			continue
		}
		if len(ticks) < 2 || ticks[0] < 0 {
			return fmt.Errorf("scales: %s %v: want two or more ticks from 0 or more", name, ticks)
		}
		for i := 1; i < len(ticks); i++ {
			if ticks[i] <= ticks[i-1] {
				return fmt.Errorf("scales: %s %v: want rising ticks", name, ticks)
			}
		}
	}
	return nil
}

// LedgerFile is one ledger file in visit order, with the file that included
// it. The root file names no parent. A diamond include keeps its first visit.
type LedgerFile struct {
	Path   string
	Parent string
}

// Prep is a command that produces what the named runners' discovery and
// runs need, such as generated sources or built dependencies. It judges
// nothing, so it carries no contract.
type Prep struct {
	Runners []string `yaml:"runners"`
	Run     []string `yaml:"run"` // argv, run from the repository root
}

// Entry is one verification's contract and gate membership. Tags describe
// the accident in the built-in and project vocabularies; Requires and
// Ensures name other inventoried verifications this one directly depends
// on or keeps working. Commit places the entry explicitly; Push is tri-state
// (absent inherits gate.push.default, true includes, false excludes): every
// entry belongs to CI. Run, Cwd and Exclusive are set only for CommandRunner
// entries and keep the direct-command execution fields. Origin identifies
// the file and list position the entry was loaded from; it is diagnostics
// only and never serialized. pushMember/pushSet hold the default-resolved
// push membership fixed during composition so Member needs no gate argument.
type Entry struct {
	Runner     string                       `yaml:"runner"`
	Unit       string                       `yaml:"unit,omitempty"`
	Name       string                       `yaml:"name"`
	Commit     bool                         `yaml:"commit,omitempty"`
	Push       *bool                        `yaml:"push,omitempty"`
	Scope      map[string]check.TargetScope `yaml:"scope,omitempty"`
	Run        []string                     `yaml:"run,omitempty"`
	Cwd        string                       `yaml:"cwd,omitempty"`
	Exclusive  []string                     `yaml:"exclusive,omitempty"`
	Tags       []string                     `yaml:"tags,omitempty"`
	Requires   []Ref                        `yaml:"requires,omitempty"`
	Ensures    []Ref                        `yaml:"ensures,omitempty"`
	Accident   string                       `yaml:"accident,omitempty"`
	Detection  string                       `yaml:"detection,omitempty"`
	Impact     string                       `yaml:"impact,omitempty"`
	DeleteWhen string                       `yaml:"delete_when,omitempty"`
	Origin     string                       `yaml:"-"` // file: verifications[i], diagnostics only
	File       string                       `yaml:"-"` // file the entry was defined in

	pushMember bool `yaml:"-"`
	pushSet    bool `yaml:"-"`
}

// Ref names another inventoried verification.
type Ref struct {
	Runner string `yaml:"runner"`
	Unit   string `yaml:"unit"`
	Name   string `yaml:"name"`
}

// Key returns the referenced verification key.
func (r Ref) Key() check.Key {
	return check.Key{Runner: r.Runner, Unit: r.Unit, Name: r.Name}
}

// Key returns the entry's verification key. Command entries derive the unit
// from Cwd, defaulting to ".", exactly as the old command lane did; every
// other runner keeps Unit.
func (e Entry) Key() check.Key {
	if e.Runner == CommandRunner {
		unit := e.Cwd
		if unit == "" {
			unit = "."
		}
		return check.Key{Runner: e.Runner, Unit: unit, Name: e.Name}
	}
	return check.Key{Runner: e.Runner, Unit: e.Unit, Name: e.Name}
}

// Member reports whether the entry runs at the named gate. Commit
// membership is explicit; push membership is the composed default-resolved
// bit when available, otherwise the raw intent with omitted defaulting to
// include; every entry belongs to CI. Unknown gate names are not members.
func (e Entry) Member(gate string) bool {
	switch gate {
	case check.GateCommit:
		return e.Commit
	case check.GatePush:
		if e.pushSet {
			return e.pushMember
		}
		if e.Push != nil {
			return *e.Push
		}
		return true
	case check.GateCI:
		return true
	}
	return false
}

// validateFields rejects execution fields on the wrong runner kind. A
// command entry with Unit set would silently run elsewhere: command keys
// derive the unit from Cwd, so Unit is ignored. A non-command entry with
// Run, Cwd or Exclusive set would silently drop execution or mutex
// intent, since only command entries execute run argv or serialize on
// tokens. Empty optional fields stay compatible.
func (e Entry) validateFields() error {
	if e.Runner == CommandRunner {
		if strings.TrimSpace(e.Unit) != "" {
			return fmt.Errorf("unit %q is ignored for runner %q: the key derives the unit from cwd, so set cwd instead", e.Unit, CommandRunner)
		}
		return nil
	}
	if len(e.Run) > 0 {
		return fmt.Errorf("run is ignored for runner %q: only command entries execute run argv", e.Runner)
	}
	if strings.TrimSpace(e.Cwd) != "" {
		return fmt.Errorf("cwd is ignored for runner %q: only command entries use cwd", e.Runner)
	}
	if len(e.Exclusive) > 0 {
		return fmt.Errorf("exclusive is ignored for runner %q: only command entries serialize on mutex tokens", e.Runner)
	}
	return nil
}

// MissingContract names the contract fields the entry leaves empty.
// Self-registered command entries carry no contract, so they never miss one.
func (e Entry) MissingContract() []string {
	if e.Runner == CommandRunner {
		return nil
	}
	var missing []string
	for _, f := range []struct{ name, value string }{
		{"accident", e.Accident},
		{"detection", e.Detection},
		{"impact", e.Impact},
		{"delete_when", e.DeleteWhen},
	} {
		if strings.TrimSpace(f.value) == "" {
			missing = append(missing, f.name)
		}
	}
	return missing
}

// Exemptions lists the gate entries that run outside ytif.
type Exemptions struct {
	Version int         `yaml:"version"`
	Entries []Exemption `yaml:"entries"`
}

// Exemption accepts one gate entry that runs outside ytif. Run matches a
// command exactly; Uses matches a GitHub Actions reference without its @ref.
// Reason says why the entry runs outside ytif; it is for review, and ytif
// does not interpret it.
type Exemption struct {
	Surface string `yaml:"surface"`
	Run     string `yaml:"run,omitempty"`
	Uses    string `yaml:"uses,omitempty"`
	Reason  string `yaml:"reason"`
}

const (
	SurfaceLefthook      = "lefthook"
	SurfaceGitHubActions = "github-actions"
)

// fragmentFile is one included ledger fragment. Only includes and
// verifications may appear here; strict decoding rejects the root-only
// version, vocabulary, prepare, board, guard, gate and nx-targets fields.
type fragmentFile struct {
	Includes      []string `yaml:"includes,omitempty"`
	Verifications []Entry  `yaml:"verifications"`
}

// composeInventory is shared by the file loader and init's byte preflight.
// realRoot seeds include-cycle detection when the root file already exists.
// It fixes the root push default onto every entry so Member needs no gate
// argument: explicit push wins, omission inherits gate.push.default with
// omitted default meaning include.
func composeInventory(root string, raw Inventory, realRoot string) (Inventory, error) {
	const display = InventoryFile
	if raw.Version != inventoryVersion {
		return Inventory{}, fmt.Errorf("%s: version %d, want %d", display, raw.Version, inventoryVersion)
	}
	if err := raw.Vocabulary.validate(); err != nil {
		return Inventory{}, fmt.Errorf("%s: vocabulary: %w", display, err)
	}
	if err := validatePrepare(raw.Prepare, display); err != nil {
		return Inventory{}, err
	}
	if err := raw.Board.validate(); err != nil {
		return Inventory{}, fmt.Errorf("%s: board: %w", display, err)
	}
	if err := raw.Guard.validate(display); err != nil {
		return Inventory{}, fmt.Errorf("%s: %w", display, err)
	}
	if err := raw.Gate.validate(display); err != nil {
		return Inventory{}, err
	}
	visited := map[string]bool{}
	var stack []includeFrame
	if realRoot != "" {
		visited[realRoot] = true
		stack = []includeFrame{{real: realRoot, display: display}}
	}
	l := &fileLoader{root: root, visited: visited, seen: map[check.Key]string{}, files: []LedgerFile{{Path: display}}}
	if err := l.addEntries(display, raw.Verifications); err != nil {
		return Inventory{}, err
	}
	if err := l.addIncludes(root, display, raw.Includes, stack); err != nil {
		return Inventory{}, err
	}
	include := raw.Gate.Push.includeByDefault()
	for i := range l.out {
		if l.out[i].Push != nil {
			l.out[i].pushMember = *l.out[i].Push
		} else {
			l.out[i].pushMember = include
		}
		l.out[i].pushSet = true
	}
	if err := validateEntryScopes(l.out, raw.Gate, display); err != nil {
		return Inventory{}, err
	}
	raw.Verifications, raw.Files = l.out, l.files
	return raw, nil
}

// LoadInventory reads InventoryFile under root and merges its recursively
// included fragments. A legacy file alone, or beside the new root, is an error:
// the first points at migration, the second is a conflict. Includes resolve
// relative to the file that declares them and must stay inside that file's
// directory subtree, following symlinks; absolute references, escapes,
// missing or non-file targets, cycles, duplicate verification keys and
// execution fields on the wrong runner kind all fail before anything runs.
func LoadInventory(root string) (Inventory, error) {
	newPath := filepath.Join(root, InventoryFile)
	oldPath := filepath.Join(root, LegacyInventoryFile)
	newStat, newErr := os.Stat(newPath)
	if newErr != nil && !errors.Is(newErr, fs.ErrNotExist) {
		return Inventory{}, newErr
	}
	newExists := newErr == nil
	_, oldErr := os.Stat(oldPath)
	if oldErr != nil && !errors.Is(oldErr, fs.ErrNotExist) {
		return Inventory{}, oldErr
	}
	oldExists := oldErr == nil
	switch {
	case newExists && oldExists:
		return Inventory{}, fmt.Errorf("%s and %s both exist; keep one ledger and remove the other", LegacyInventoryFile, InventoryFile)
	case !newExists && oldExists:
		return Inventory{}, fmt.Errorf("%s is the previous ledger format; run ytif migrate to convert it to %s", LegacyInventoryFile, InventoryFile)
	case !newExists:
		return Inventory{}, fmt.Errorf("%s: missing ledger with required gate settings; run ytif init", InventoryFile)
	}
	if !newStat.Mode().IsRegular() {
		return Inventory{}, fmt.Errorf("%s: not a regular file", InventoryFile)
	}
	var raw Inventory
	if err := decodeStrict(newPath, InventoryFile, &raw); err != nil {
		return Inventory{}, err
	}
	realRoot, err := filepath.EvalSymlinks(newPath)
	if err != nil {
		return Inventory{}, fmt.Errorf("%s: %w", InventoryFile, err)
	}
	return composeInventory(root, raw, realRoot)
}

// LoadExemptions reads ExemptionsFile under root. A missing file is an empty
// list.
func LoadExemptions(root string) (Exemptions, error) {
	x := Exemptions{Version: exemptionsVersion}
	found, err := loadCompatible(filepath.Join(root, ExemptionsFile), ExemptionsFile, exemptionsVersion, &x)
	if err != nil || !found {
		return Exemptions{Version: exemptionsVersion}, err
	}
	for i, e := range x.Entries {
		if err := e.validate(); err != nil {
			return Exemptions{}, fmt.Errorf("%s: entries[%d]: %w", ExemptionsFile, i, err)
		}
	}
	return x, nil
}

func (e Exemption) validate() error {
	switch e.Surface {
	case SurfaceLefthook, SurfaceGitHubActions:
	default:
		return fmt.Errorf("surface %q: want %s or %s", e.Surface, SurfaceLefthook, SurfaceGitHubActions)
	}
	if strings.TrimSpace(e.Reason) == "" {
		return errors.New("reason: state why this entry runs outside ytif")
	}
	if (e.Run == "") == (e.Uses == "") {
		return errors.New("set exactly one of run or uses")
	}
	if e.Uses != "" && e.Surface != SurfaceGitHubActions {
		return fmt.Errorf("uses applies only to %s", SurfaceGitHubActions)
	}
	return nil
}

// validatePrepare rejects prepare steps without runners or argv. A step
// that names nothing to prepare or nothing to run is a typo, so loading
// and migration share this check.
func validatePrepare(prep []Prep, display string) error {
	for i, p := range prep {
		if len(p.Runners) == 0 || len(p.Run) == 0 || p.Run[0] == "" {
			return fmt.Errorf("%s: prepare[%d]: set runners and run", display, i)
		}
	}
	return nil
}

// legacyInventory mirrors the version-2 ledger Migrate reads. Strictly
// pre-decoding into it rejects v3-only fields and duplicate keys exactly
// as the original loader did, before the node tree is rewritten.
type legacyInventory struct {
	Version    int             `yaml:"version"`
	Vocabulary Vocabulary      `yaml:"vocabulary,omitempty"`
	Prepare    []Prep          `yaml:"prepare,omitempty"`
	Commands   []legacyCommand `yaml:"commands,omitempty"`
	Checks     []legacyEntry   `yaml:"checks"`
}

// legacyCommand mirrors one version-2 direct-command lane entry.
type legacyCommand struct {
	Name      string   `yaml:"name"`
	Run       []string `yaml:"run"`
	Cwd       string   `yaml:"cwd,omitempty"`
	Exclusive []string `yaml:"exclusive,omitempty"`
}

// legacyEntry mirrors one version-2 check: contract plus placement.
type legacyEntry struct {
	Runner     string   `yaml:"runner"`
	Unit       string   `yaml:"unit"`
	Name       string   `yaml:"name"`
	Placement  string   `yaml:"placement"`
	Tags       []string `yaml:"tags,omitempty"`
	Requires   []Ref    `yaml:"requires,omitempty"`
	Ensures    []Ref    `yaml:"ensures,omitempty"`
	Accident   string   `yaml:"accident"`
	Detection  string   `yaml:"detection"`
	Impact     string   `yaml:"impact"`
	DeleteWhen string   `yaml:"delete_when"`
}

// decodeLegacy strictly reads the version-2 ledger, applying the same
// shape, version, vocabulary and prepare validation the original loader
// did. Invalid input fails here, before transformation or any write, so
// the legacy file is preserved untouched.
func decodeLegacy(data []byte) (legacyInventory, error) {
	var old legacyInventory
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&old); err != nil {
		if errors.Is(err, io.EOF) {
			return old, fmt.Errorf("%s: empty file", LegacyInventoryFile)
		}
		return old, fmt.Errorf("%s: %w", LegacyInventoryFile, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return old, fmt.Errorf("%s: want exactly one YAML document", LegacyInventoryFile)
	}
	if old.Version != 2 {
		return old, fmt.Errorf("%s: version %d: can only migrate version 2", LegacyInventoryFile, old.Version)
	}
	if err := old.Vocabulary.validate(); err != nil {
		return old, fmt.Errorf("%s: vocabulary: %w", LegacyInventoryFile, err)
	}
	if err := validatePrepare(old.Prepare, LegacyInventoryFile); err != nil {
		return old, err
	}
	return old, nil
}

// loadCompatible decodes one strict YAML document with a shared version
// const. It reports false for a missing file.
func loadCompatible(path, display string, want int, v any) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return false, fmt.Errorf("%s: empty file; start it with version: %d", display, want)
		}
		return false, fmt.Errorf("%s: %w", display, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("%s: want exactly one YAML document", display)
	}
	switch t := v.(type) {
	case *Exemptions:
		if t.Version != want {
			return false, fmt.Errorf("%s: version %d, want %d", display, t.Version, want)
		}
	}
	return true, nil
}

// decodeStrict decodes exactly one strict YAML document from path,
// identifying the file as display in errors.
func decodeStrict(path, display string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return decodeDocument(f, display, v)
}

func decodeDocument(r io.Reader, display string, v any) error {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s: empty file", display)
		}
		return fmt.Errorf("%s: %w", display, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: want exactly one YAML document", display)
	}
	return nil
}

type includeFrame struct {
	real    string
	display string
}

// fileLoader merges ledger files depth-first: each file's own entries,
// then its includes in listed order. visited dedupes diamond references
// so a repeated fragment merges once instead of overriding keys; seen
// rejects duplicate verification keys with both provenances.
type fileLoader struct {
	root    string
	visited map[string]bool
	seen    map[check.Key]string
	out     []Entry
	files   []LedgerFile
}

func (l *fileLoader) addEntries(display string, entries []Entry) error {
	for i := range entries {
		entries[i].Origin = fmt.Sprintf("%s: verifications[%d]", display, i)
		entries[i].File = display
		k := entries[i].Key()
		if prev, dup := l.seen[k]; dup {
			return fmt.Errorf("duplicate verification %q: first %s, also %s", k, prev, entries[i].Origin)
		}
		if err := entries[i].validateFields(); err != nil {
			return fmt.Errorf("%s: %w", entries[i].Origin, err)
		}
		l.seen[k] = entries[i].Origin
		l.out = append(l.out, entries[i])
	}
	return nil
}

func (l *fileLoader) addIncludes(declDir, declDisplay string, includes []string, stack []includeFrame) error {
	for _, inc := range includes {
		target, err := ResolveInclude(declDir, declDisplay, inc)
		if err != nil {
			return err
		}
		real, err := filepath.EvalSymlinks(target)
		if err != nil {
			return fmt.Errorf("%s: include %q: %w", declDisplay, inc, err)
		}
		for _, s := range stack {
			if s.real == real {
				chain := make([]string, 0, len(stack)+1)
				for _, f := range stack {
					chain = append(chain, f.display)
				}
				chain = append(chain, relDisplay(l.root, target))
				return fmt.Errorf("include cycle: %s", strings.Join(chain, " -> "))
			}
		}
		if l.visited[real] {
			continue
		}
		l.visited[real] = true
		display := relDisplay(l.root, target)
		l.files = append(l.files, LedgerFile{Path: display, Parent: declDisplay})
		var frag fragmentFile
		if err := decodeStrict(target, display, &frag); err != nil {
			return err
		}
		if err := l.addEntries(display, frag.Verifications); err != nil {
			return err
		}
		if err := l.addIncludes(filepath.Dir(target), display, frag.Includes, append(stack, includeFrame{real: real, display: display})); err != nil {
			return err
		}
	}
	return nil
}

// ResolveInclude maps one include reference to a filesystem path. The
// reference is relative to the declaring file's directory and must name an
// existing YAML regular file inside that directory's subtree, before and
// after symlink resolution.
func ResolveInclude(declDir, declDisplay, inc string) (string, error) {
	if strings.TrimSpace(inc) == "" {
		return "", fmt.Errorf("%s: empty include", declDisplay)
	}
	if filepath.IsAbs(inc) {
		return "", fmt.Errorf("%s: include %q: absolute paths are not allowed", declDisplay, inc)
	}
	if lower := strings.ToLower(inc); !strings.HasSuffix(lower, ".yaml") && !strings.HasSuffix(lower, ".yml") {
		return "", fmt.Errorf("%s: include %q: only YAML files may be included", declDisplay, inc)
	}
	joined := filepath.Join(declDir, inc)
	if rel, err := filepath.Rel(declDir, joined); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: include %q: must stay within the declaring file's directory", declDisplay, inc)
	}
	st, err := os.Stat(joined)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%s: include %q: no such file", declDisplay, inc)
		}
		return "", fmt.Errorf("%s: include %q: %w", declDisplay, inc, err)
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s: include %q: not a regular file", declDisplay, inc)
	}
	realDecl, err := filepath.EvalSymlinks(declDir)
	if err != nil {
		return "", fmt.Errorf("%s: include %q: %w", declDisplay, inc, err)
	}
	realTarget, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("%s: include %q: %w", declDisplay, inc, err)
	}
	if rel, err := filepath.Rel(realDecl, realTarget); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: include %q: resolves outside the declaring file's directory", declDisplay, inc)
	}
	return joined, nil
}

func relDisplay(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return path
}

// Migrate converts the legacy ledger at root/ytif-inventory.yaml (version 2)
// to root/ytif-verifications.yaml (version 4), or a version-3 ledger to
// version 4. It validates the old shape, rewrites the document tree so
// comments survive, and preflights the result through the new strict reader
// before saving. v2 checks become verifications: a commit placement becomes
// commit: true, push and ci placements become the push-and-CI default.
// Commands become runner: command entries keeping their name, run, cwd and
// exclusive fields. Push membership is never assigned automatically; the
// notice names the resulting push and CI expansion: the notice lists the
// exact keys that gain push membership so the host can add explicit
// exclusions. v3 migration inverts every explicit skip-push boolean
// (true->push:false, false->push:true), preserves omissions and YAML
// anchors/merges/includes/comments, drops scope.push only when every
// included entry's resolved push scope already equals the CI scope, and
// leaves all files unchanged on mismatch. The legacy file is removed only
// after the new file is safely written; collisions preserve the input and
// reruns leave a migrated root unchanged.
func Migrate(root string, out io.Writer) error {
	oldPath := filepath.Join(root, LegacyInventoryFile)
	newPath := filepath.Join(root, InventoryFile)
	oldData, err := os.ReadFile(oldPath)
	if errors.Is(err, fs.ErrNotExist) {
		data, rerr := os.ReadFile(newPath)
		if rerr != nil {
			if errors.Is(rerr, fs.ErrNotExist) {
				return fmt.Errorf("no %s in %s; nothing to migrate", LegacyInventoryFile, root)
			}
			return rerr
		}
		ver, verr := ledgerVersion(data)
		if verr != nil {
			return verr
		}
		switch ver {
		case inventoryVersion:
			if _, lerr := LoadInventory(root); lerr != nil {
				return lerr
			}
			fmt.Fprintf(out, "already migrated: %s is current\n", InventoryFile)
			return nil
		case 3:
			return migrateV3ToV4(root, out)
		default:
			return fmt.Errorf("%s: version %d, want %d; run ytif migrate from a supported ledger", InventoryFile, ver, inventoryVersion)
		}
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("%s and %s both exist; keep one ledger and remove the other", LegacyInventoryFile, InventoryFile)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	old, err := decodeLegacy(oldData)
	if err != nil {
		return err
	}
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(oldData))
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s: empty file", LegacyInventoryFile)
		}
		return fmt.Errorf("%s: %w", LegacyInventoryFile, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: want exactly one YAML document", LegacyInventoryFile)
	}
	counts, err := transformLegacy(&doc, old.Checks)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	var pre Inventory
	pdec := yaml.NewDecoder(bytes.NewReader(buf.Bytes()))
	pdec.KnownFields(true)
	if err := pdec.Decode(&pre); err != nil {
		return fmt.Errorf("migrated output failed to load: %w; expand YAML merges and aliases in %s and retry", err, LegacyInventoryFile)
	}
	if pre.Version != inventoryVersion {
		return fmt.Errorf("migrated output has version %d, want %d", pre.Version, inventoryVersion)
	}
	if err := pre.Vocabulary.validate(); err != nil {
		return fmt.Errorf("migrated output has invalid vocabulary: %w", err)
	}
	if err := pre.Gate.validate(InventoryFile); err != nil {
		return fmt.Errorf("migrated output has invalid gate: %w", err)
	}
	seen := map[check.Key]string{}
	for i := range pre.Verifications {
		e := &pre.Verifications[i]
		e.Origin = fmt.Sprintf("%s: verifications[%d]", InventoryFile, i)
		k := e.Key()
		if prev, dup := seen[k]; dup {
			return fmt.Errorf("migrated output has duplicate verification %q: first %s, also %s", k, prev, e.Origin)
		}
		if err := e.validateFields(); err != nil {
			return fmt.Errorf("migrated output %s: %w", e.Origin, err)
		}
		seen[k] = e.Origin
	}
	if err := validateEntryScopes(pre.Verifications, pre.Gate, InventoryFile); err != nil {
		return fmt.Errorf("migrated output has invalid scope: %w", err)
	}
	f, err := os.OpenFile(newPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s and %s both exist; keep one ledger and remove the other", LegacyInventoryFile, InventoryFile)
		}
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		os.Remove(newPath)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(newPath)
		return err
	}
	if err := os.Remove(oldPath); err != nil {
		return fmt.Errorf("wrote %s but could not remove %s: %w", InventoryFile, LegacyInventoryFile, err)
	}
	total := counts.commit + counts.preset + counts.commands
	fmt.Fprintf(out, "migrated %s -> %s: %d verifications (%d commit:true, %d push+CI default, %d from commands)\n",
		LegacyInventoryFile, InventoryFile, total, counts.commit, counts.preset, counts.commands)
	if len(counts.gainedPush) > 0 {
		fmt.Fprintf(out, "note: these gain push membership (old ci-only checks and commands); ytif did not assign push, add \"push: false\" where push must not run them:\n")
		for _, k := range counts.gainedPush {
			fmt.Fprintf(out, "  - %s\n", k)
		}
	}
	return nil
}

type migrateCounts struct {
	commit   int
	preset   int
	commands int
	// gainedPush lists the new keys that newly belong to push, in output
	// order: old ci-only checks and check-only commands. Old push and
	// commit checks already ran at push, so they are not listed.
	gainedPush []string
}

// transformLegacy rewrites a legacy version-2 document tree in place into
// the version-4 shape, preserving comments and unrelated content.
func transformLegacy(doc *yaml.Node, checks []legacyEntry) (migrateCounts, error) {
	var counts migrateCounts
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return counts, fmt.Errorf("%s: want a mapping", LegacyInventoryFile)
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return counts, fmt.Errorf("%s: want a mapping", LegacyInventoryFile)
	}
	seenTop := map[string]bool{}
	var checksKey, checksVal, commandsVal *yaml.Node
	commandsIdx := -1
	for i := 0; i+1 < len(top.Content); i += 2 {
		k, v := top.Content[i], top.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return counts, fmt.Errorf("%s: want field names as text", LegacyInventoryFile)
		}
		if seenTop[k.Value] {
			return counts, fmt.Errorf("%s: duplicate field %q", LegacyInventoryFile, k.Value)
		}
		seenTop[k.Value] = true
		switch k.Value {
		case "version":
			if v.Kind != yaml.ScalarNode || v.Value != "2" {
				return counts, fmt.Errorf("%s: version %q: can only migrate version 2", LegacyInventoryFile, v.Value)
			}
			v.Value = "4"
			v.Tag = "!!int"
		case "vocabulary", "prepare":
		case "checks":
			checksKey, checksVal = k, v
		case "commands":
			commandsVal = v
			commandsIdx = i
		default:
			return counts, fmt.Errorf("%s: unknown field %q: want version, vocabulary, prepare, commands or checks", LegacyInventoryFile, k.Value)
		}
	}
	if checksVal != nil {
		if err := transformLegacyChecks(checksVal, checks, &counts); err != nil {
			return counts, err
		}
		checksKey.Value = "verifications"
	}
	var moved []*yaml.Node
	if commandsVal != nil {
		var err error
		moved, err = transformLegacyCommands(commandsVal, &counts)
		if err != nil {
			return counts, err
		}
	}
	if moved != nil {
		switch {
		case checksVal != nil && checksVal.Kind == yaml.SequenceNode:
			checksVal.Content = append(checksVal.Content, moved...)
		case checksVal != nil && isNull(checksVal):
			checksVal.Kind = yaml.SequenceNode
			checksVal.Tag = "!!seq"
			checksVal.Value = ""
			checksVal.Content = moved
		default:
			seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: moved}
			top.Content = append(top.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "verifications"}, seq)
		}
	}
	if moved == nil && checksVal == nil {
		top.Content = append(top.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "verifications"},
			&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
	}
	if commandsIdx >= 0 {
		top.Content = append(top.Content[:commandsIdx], top.Content[commandsIdx+2:]...)
	}
	insertLegacyGate(top)
	insertLegacyGuard(top)
	return counts, nil
}

// insertLegacyGuard adds the init guard after the gate when missing.
func insertLegacyGuard(top *yaml.Node) {
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "guard" {
			return
		}
	}
	limit := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "limit"},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "2m"},
	}}
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "guard"}
	at := len(top.Content)
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "gate" {
			at = i + 2
			break
		}
	}
	top.Content = append(top.Content[:at], append([]*yaml.Node{key, limit}, top.Content[at:]...)...)
}

// insertLegacyGate adds the init gate after version.
func insertLegacyGate(top *yaml.Node) {
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "gate" {
			return
		}
	}
	scope := func(v string) *yaml.Node {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "scope"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: v},
		}}
	}
	gate := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "commit"}, scope("file"),
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "push"}, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "ci"}, scope("full"),
	}}
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "gate"}
	at := len(top.Content)
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "version" {
			at = i + 2
			break
		}
	}
	top.Content = append(top.Content[:at], append([]*yaml.Node{key, gate}, top.Content[at:]...)...)
}

// transformLegacyChecks converts each legacy check entry's placement into
// the new membership booleans: commit becomes commit: true, push and ci
// become the push-and-CI default. Entries retain the resolved values from
// before any anchor donors were rewritten.
func transformLegacyChecks(seq *yaml.Node, entries []legacyEntry, counts *migrateCounts) error {
	if isNull(seq) {
		return nil
	}
	if seq.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s: checks: want a list", LegacyInventoryFile)
	}
	for n, item := range seq.Content {
		if item.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: checks[%d]: want a mapping", LegacyInventoryFile, n)
		}
		e := entries[n]
		placeIdx := -1
		merged := false
		for i := 0; i+1 < len(item.Content); i += 2 {
			switch item.Content[i].Value {
			case "<<":
				merged = true
			case "placement":
				placeIdx = i
			}
		}
		if strings.TrimSpace(e.Runner) == "" || strings.TrimSpace(e.Name) == "" {
			return fmt.Errorf("%s: checks[%d]: set runner and name", LegacyInventoryFile, n)
		}
		if placeIdx < 0 {
			return fmt.Errorf("%s: checks[%d]: set placement to commit, push or ci", LegacyInventoryFile, n)
		}
		switch e.Placement {
		case "commit":
			item.Content[placeIdx].Value = "commit"
			item.Content[placeIdx+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}
			counts.commit++
		case "push", "ci":
			if merged {
				// Removing placement alone would expose a donor's migrated commit value.
				item.Content[placeIdx].Value = "commit"
				item.Content[placeIdx+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}
			} else {
				item.Content = append(item.Content[:placeIdx], item.Content[placeIdx+2:]...)
			}
			counts.preset++
			if e.Placement == "ci" {
				counts.gainedPush = append(counts.gainedPush, check.Key{Runner: e.Runner, Unit: e.Unit, Name: e.Name}.String())
			}
		default:
			return fmt.Errorf("%s: checks[%d]: placement %q: want commit, push or ci", LegacyInventoryFile, n, e.Placement)
		}
	}
	return nil
}

// transformLegacyCommands converts each legacy command into a command
// verification entry reusing the original value nodes so
// comments survive.
func transformLegacyCommands(seq *yaml.Node, counts *migrateCounts) ([]*yaml.Node, error) {
	if isNull(seq) {
		return nil, nil
	}
	if seq.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s: commands: want a list", LegacyInventoryFile)
	}
	var out []*yaml.Node
	for n, item := range seq.Content {
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s: commands[%d]: want a mapping", LegacyInventoryFile, n)
		}
		var name, run, cwd, exclusive *yaml.Node
		for i := 0; i+1 < len(item.Content); i += 2 {
			k, v := item.Content[i], item.Content[i+1]
			switch k.Value {
			case "name":
				name = v
			case "run":
				run = v
			case "cwd":
				cwd = v
			case "exclusive":
				exclusive = v
			default:
				return nil, fmt.Errorf("%s: commands[%d]: unknown field %q: want name, run, cwd or exclusive", LegacyInventoryFile, n, k.Value)
			}
		}
		if name == nil || strings.TrimSpace(name.Value) == "" {
			return nil, fmt.Errorf("%s: commands[%d]: set name and run argv", LegacyInventoryFile, n)
		}
		if run == nil || run.Kind != yaml.SequenceNode || len(run.Content) == 0 || strings.TrimSpace(run.Content[0].Value) == "" {
			return nil, fmt.Errorf("%s: commands[%d]: set name and run argv", LegacyInventoryFile, n)
		}
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		entry.HeadComment, entry.LineComment, entry.FootComment = item.HeadComment, item.LineComment, item.FootComment
		add := func(key string, val *yaml.Node) {
			entry.Content = append(entry.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
		}
		add("runner", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: CommandRunner})
		add("name", name)
		add("run", run)
		unit := "."
		if cwd != nil {
			add("cwd", cwd)
			if strings.TrimSpace(cwd.Value) != "" {
				unit = cwd.Value
			}
		}
		if exclusive != nil {
			add("exclusive", exclusive)
		}
		out = append(out, entry)
		counts.commands++
		counts.gainedPush = append(counts.gainedPush, check.Key{Runner: CommandRunner, Unit: unit, Name: name.Value}.String())
	}
	return out, nil
}

func isNull(n *yaml.Node) bool {
	return n.Tag == "!!null" || (n.Kind == yaml.ScalarNode && n.Value == "")
}

// ledgerVersion reads only the version field so Migrate can dispatch
// between v2, v3 and current ledgers without strict full decoding.
func ledgerVersion(data []byte) (int, error) {
	var v struct {
		Version int `yaml:"version"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&v); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("%s: empty file", InventoryFile)
		}
		return 0, fmt.Errorf("%s: %w", InventoryFile, err)
	}
	return v.Version, nil
}

// v3PushScope mirrors the retired v3 push policy for migration comparison.
type v3PushScope struct {
	Scope     *check.TargetScope `yaml:"scope,omitempty"`
	CiProfile string             `yaml:"ci-profile,omitempty"`
}

// v3GateConfig mirrors the v3 gate for migration comparison.
type v3GateConfig struct {
	Commit GateScope   `yaml:"commit"`
	Push   v3PushScope `yaml:"push"`
	CI     CIScope     `yaml:"ci"`
}

// v3Entry mirrors the v3 verification for migration reads.
type v3Entry struct {
	Runner     string                       `yaml:"runner"`
	Unit       string                       `yaml:"unit,omitempty"`
	Name       string                       `yaml:"name"`
	Commit     bool                         `yaml:"commit,omitempty"`
	SkipPush   bool                         `yaml:"skip-push,omitempty"`
	Scope      map[string]check.TargetScope `yaml:"scope,omitempty"`
	Run        []string                     `yaml:"run,omitempty"`
	Cwd        string                       `yaml:"cwd,omitempty"`
	Exclusive  []string                     `yaml:"exclusive,omitempty"`
	Tags       []string                     `yaml:"tags,omitempty"`
	Requires   []Ref                        `yaml:"requires,omitempty"`
	Ensures    []Ref                        `yaml:"ensures,omitempty"`
	Accident   string                       `yaml:"accident,omitempty"`
	Detection  string                       `yaml:"detection,omitempty"`
	Impact     string                       `yaml:"impact,omitempty"`
	DeleteWhen string                       `yaml:"delete_when,omitempty"`
}

// Key returns the verification key, matching Entry.Key.
func (e v3Entry) Key() check.Key {
	if e.Runner == CommandRunner {
		unit := e.Cwd
		if unit == "" {
			unit = "."
		}
		return check.Key{Runner: e.Runner, Unit: unit, Name: e.Name}
	}
	return check.Key{Runner: e.Runner, Unit: e.Unit, Name: e.Name}
}

// v3Inventory mirrors the v3 root for migration reads.
type v3Inventory struct {
	Version       int          `yaml:"version"`
	Gate          v3GateConfig `yaml:"gate"`
	Vocabulary    Vocabulary   `yaml:"vocabulary,omitempty"`
	Prepare       []Prep       `yaml:"prepare,omitempty"`
	Board         BoardConfig  `yaml:"board,omitempty"`
	Guard         GuardConfig  `yaml:"guard,omitempty"`
	NxTargets     bool         `yaml:"nx-targets,omitempty"`
	Includes      []string     `yaml:"includes,omitempty"`
	Verifications []v3Entry    `yaml:"verifications"`
}

// v3Fragment mirrors a v3 included fragment.
type v3Fragment struct {
	Includes      []string  `yaml:"includes,omitempty"`
	Verifications []v3Entry `yaml:"verifications"`
}

type v3FileData struct {
	rel           string
	doc           yaml.Node
	isRoot        bool
	verifications []v3Entry
	includes      []string
	data          []byte
	transformed   []byte
}

// oldPushScope resolves the v3 requested push scope and its origin.
func oldPushScope(g v3GateConfig, e v3Entry) (check.TargetScope, string) {
	if g.Push.CiProfile != "" {
		prof := g.Push.CiProfile
		if s, ok := e.Scope[check.GateCI+"/"+prof]; ok {
			return s, "scope.ci/" + prof
		}
		if s, ok := e.Scope[check.GateCI]; ok {
			return s, "scope.ci"
		}
		p := g.CI.Profiles[prof]
		if p.Scope != nil {
			return *p.Scope, "gate.ci.profiles." + prof + ".scope"
		}
		return g.CI.Scope, "gate.ci.scope"
	}
	if s, ok := e.Scope[check.GatePush]; ok {
		return s, "scope.push"
	}
	if g.Push.Scope != nil {
		return *g.Push.Scope, "gate.push.scope"
	}
	return "", "gate.push.scope"
}

// newPushScope resolves the v4 requested push scope (always the CI chain)
// and its origin, using the preserved CI policy and ci-profile.
func newPushScope(g v3GateConfig, e v3Entry) (check.TargetScope, string) {
	prof := g.Push.CiProfile
	if prof != "" {
		if s, ok := e.Scope[check.GateCI+"/"+prof]; ok {
			return s, "scope.ci/" + prof
		}
	}
	if s, ok := e.Scope[check.GateCI]; ok {
		return s, "scope.ci"
	}
	if prof != "" {
		p := g.CI.Profiles[prof]
		if p.Scope != nil {
			return *p.Scope, "gate.ci.profiles." + prof + ".scope"
		}
	}
	return g.CI.Scope, "gate.ci.scope"
}

func (g v3GateConfig) validateV3(display string) error {
	if g.Commit.Scope == "" || !g.Commit.Scope.Valid() {
		if g.Commit.Scope == "" {
			return fmt.Errorf("%s: gate.commit.scope: set scope to file, affected or full", display)
		}
		return fmt.Errorf("%s: gate.commit.scope %q: want file, affected or full", display, string(g.Commit.Scope))
	}
	if g.CI.Scope == "" || !g.CI.Scope.Valid() {
		if g.CI.Scope == "" {
			return fmt.Errorf("%s: gate.ci.scope: set scope to file, affected or full", display)
		}
		return fmt.Errorf("%s: gate.ci.scope %q: want file, affected or full", display, string(g.CI.Scope))
	}
	hasScope := g.Push.Scope != nil
	hasProfile := g.Push.CiProfile != ""
	switch {
	case hasScope && hasProfile:
		return fmt.Errorf("%s: gate.push: set exactly one of scope or ci-profile, have both", display)
	case !hasScope && !hasProfile:
		return fmt.Errorf("%s: gate.push: set exactly one of scope or ci-profile", display)
	case hasScope && !g.Push.Scope.Valid():
		return fmt.Errorf("%s: gate.push.scope %q: want file, affected or full", display, string(*g.Push.Scope))
	}
	for name, p := range g.CI.Profiles {
		if name == "" {
			return fmt.Errorf("%s: gate.ci.profiles: profile name is empty", display)
		}
		if p.Scope != nil && !p.Scope.Valid() {
			return fmt.Errorf("%s: gate.ci.profiles.%s.scope %q: want file, affected or full", display, name, string(*p.Scope))
		}
	}
	if hasProfile {
		if _, ok := g.CI.Profiles[g.Push.CiProfile]; !ok {
			return fmt.Errorf("%s: gate.push ci-profile %q: unknown profile", display, g.Push.CiProfile)
		}
	}
	return nil
}

// validateV3EntryScopes rejects invalid v3 scope keys before comparison so a
// v3 ledger that never loaded does not migrate into a silently fixed v4.
func validateV3EntryScopes(entries []v3Entry, gate v3GateConfig, display string) error {
	profiles := map[string]bool{}
	for name := range gate.CI.Profiles {
		profiles[name] = true
	}
	mapped := gate.Push.CiProfile != ""
	for idx, e := range entries {
		loc := fmt.Sprintf("%s: verifications[%d]", display, idx)
		keys := make([]string, 0, len(e.Scope))
		for k := range e.Scope {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := e.Scope[k]
			if !v.Valid() {
				return fmt.Errorf("%s: scope.%s %q: want file, affected or full", loc, k, string(v))
			}
			switch k {
			case check.GateCommit, check.GatePush, check.GateCI:
			default:
				name, ok := strings.CutPrefix(k, check.GateCI+"/")
				if !ok || name == "" {
					return fmt.Errorf("%s: scope.%q: unknown scope, want commit, push, ci or ci/<profile>", loc, k)
				}
				if !profiles[name] {
					return fmt.Errorf("%s: scope.%q: unknown profile", loc, k)
				}
			}
		}
		if mapped {
			if _, ok := e.Scope[check.GatePush]; ok {
				return fmt.Errorf("%s: scope.push conflicts with gate.push ci-profile %q; set scope.ci/%s or scope.ci instead", loc, gate.Push.CiProfile, gate.Push.CiProfile)
			}
		}
	}
	return nil
}

// collectV3Ledger reads the v3 root and all recursively included fragments.
func collectV3Ledger(root string) ([]*v3FileData, v3GateConfig, error) {
	rootPath := filepath.Join(root, InventoryFile)
	realRoot, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return nil, v3GateConfig{}, fmt.Errorf("%s: %w", InventoryFile, err)
	}
	visited := map[string]bool{realRoot: true}
	stack := []includeFrame{{real: realRoot, display: InventoryFile}}
	var files []*v3FileData
	var gate v3GateConfig
	var gateSet bool
	seen := map[check.Key]string{}
	var visit func(target, display string, isRoot bool, stack []includeFrame) error
	visit = func(target, display string, isRoot bool, stack []includeFrame) error {
		data, err := os.ReadFile(target)
		if err != nil {
			return fmt.Errorf("%s: %w", display, err)
		}
		var doc yaml.Node
		dec := yaml.NewDecoder(bytes.NewReader(data))
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("%s: empty file", display)
			}
			return fmt.Errorf("%s: %w", display, err)
		}
		var extra yaml.Node
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			return fmt.Errorf("%s: want exactly one YAML document", display)
		}
		fd := &v3FileData{rel: display, doc: doc, isRoot: isRoot, data: data}
		if isRoot {
			var raw v3Inventory
			pdec := yaml.NewDecoder(bytes.NewReader(data))
			pdec.KnownFields(true)
			if err := pdec.Decode(&raw); err != nil {
				return fmt.Errorf("%s: %w", display, err)
			}
			if raw.Version != 3 {
				return fmt.Errorf("%s: version %d, want 3 for v3 migration", display, raw.Version)
			}
			if err := raw.Gate.validateV3(display); err != nil {
				return err
			}
			gate, gateSet = raw.Gate, true
			fd.verifications = raw.Verifications
			fd.includes = raw.Includes
			for i, e := range raw.Verifications {
				k := e.Key()
				loc := fmt.Sprintf("%s: verifications[%d]", display, i)
				if prev, dup := seen[k]; dup {
					return fmt.Errorf("duplicate verification %q: first %s, also %s", k, prev, loc)
				}
				seen[k] = loc
			}
		} else {
			var frag v3Fragment
			pdec := yaml.NewDecoder(bytes.NewReader(data))
			pdec.KnownFields(true)
			if err := pdec.Decode(&frag); err != nil {
				return fmt.Errorf("%s: %w", display, err)
			}
			fd.verifications = frag.Verifications
			fd.includes = frag.Includes
			for i, e := range frag.Verifications {
				k := e.Key()
				loc := fmt.Sprintf("%s: verifications[%d]", display, i)
				if prev, dup := seen[k]; dup {
					return fmt.Errorf("duplicate verification %q: first %s, also %s", k, prev, loc)
				}
				seen[k] = loc
			}
		}
		files = append(files, fd)
		declDir := filepath.Dir(target)
		for _, inc := range fd.includes {
			next, err := ResolveInclude(declDir, display, inc)
			if err != nil {
				return err
			}
			real, err := filepath.EvalSymlinks(next)
			if err != nil {
				return fmt.Errorf("%s: include %q: %w", display, inc, err)
			}
			for _, s := range stack {
				if s.real == real {
					chain := make([]string, 0, len(stack)+1)
					for _, f := range stack {
						chain = append(chain, f.display)
					}
					chain = append(chain, relDisplay(root, next))
					return fmt.Errorf("include cycle: %s", strings.Join(chain, " -> "))
				}
			}
			if visited[real] {
				continue
			}
			visited[real] = true
			disp := relDisplay(root, next)
			if err := visit(next, disp, false, append(stack, includeFrame{real: real, display: disp})); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(rootPath, InventoryFile, true, stack); err != nil {
		return nil, v3GateConfig{}, err
	}
	if !gateSet {
		return nil, v3GateConfig{}, fmt.Errorf("%s: missing gate", InventoryFile)
	}
	return files, gate, nil
}

// mapKeyIndex finds a key in a mapping node.
func mapKeyIndex(m *yaml.Node, key string) int {
	if m == nil || m.Kind != yaml.MappingNode {
		return -1
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	idx := mapKeyIndex(m, key)
	if idx < 0 {
		return nil
	}
	return m.Content[idx+1]
}

func mapRemoveKey(m *yaml.Node, key string) bool {
	idx := mapKeyIndex(m, key)
	if idx < 0 {
		return false
	}
	m.Content = append(m.Content[:idx], m.Content[idx+2:]...)
	return true
}

// rewriteV3Node visits only verification entries, their merge donors, and
// their scope maps; vocabulary and other user mappings retain their keys.
func rewriteV3Node(n *yaml.Node, rel string) (inverted, removed int, err error) {
	seen := map[*yaml.Node]bool{}
	var visit func(*yaml.Node, bool) error
	visit = func(node *yaml.Node, scope bool) error {
		if node == nil {
			return nil
		}
		if node.Kind == yaml.AliasNode {
			node = node.Alias
		}
		if seen[node] {
			return nil
		}
		seen[node] = true
		if node.Kind == yaml.SequenceNode {
			for _, child := range node.Content {
				if err := visit(child, scope); err != nil {
					return err
				}
			}
			return nil
		}
		if node.Kind != yaml.MappingNode {
			return nil
		}
		if scope && mapRemoveKey(node, "push") {
			removed++
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			switch {
			case key.Value == "<<":
				if err := visit(value, scope); err != nil {
					return err
				}
			case !scope && key.Value == "scope":
				if err := visit(value, true); err != nil {
					return err
				}
			case !scope && key.Value == "skip-push":
				var b bool
				if err := value.Decode(&b); err != nil {
					return fmt.Errorf("%s: skip-push: %w", rel, err)
				}
				key.Value = "push"
				value.Tag, value.Value = "!!bool", strconv.FormatBool(!b)
				inverted++
			}
		}
		return nil
	}
	err = visit(n, false)
	return
}

// migrateV3ToV4 rewrites every ledger file from v3 to v4.
func migrateV3ToV4(root string, out io.Writer) error {
	files, gate, err := collectV3Ledger(root)
	if err != nil {
		return err
	}
	for _, fd := range files {
		if err := validateV3EntryScopes(fd.verifications, gate, fd.rel); err != nil {
			return err
		}
	}
	type mismatch struct {
		key, oldScope, oldFrom, newScope, newFrom, loc string
	}
	var bad []mismatch
	for _, fd := range files {
		for i, e := range fd.verifications {
			if e.SkipPush {
				continue
			}
			oldScope, oldFrom := oldPushScope(gate, e)
			newScope, newFrom := newPushScope(gate, e)
			if oldScope != newScope {
				loc := fmt.Sprintf("%s: verifications[%d]", fd.rel, i)
				bad = append(bad, mismatch{key: e.Key().String(), oldScope: string(oldScope), oldFrom: oldFrom, newScope: string(newScope), newFrom: newFrom, loc: loc})
			}
		}
	}
	if len(bad) > 0 {
		sort.Slice(bad, func(i, j int) bool { return bad[i].key < bad[j].key })
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s: push scope would change for %d verification(s); adjust gate.ci scope/profile then retry, leaving all files unchanged:\n", InventoryFile, len(bad))
		for _, m := range bad {
			fmt.Fprintf(&sb, "  - %s (%s): was %s from %s, would be %s from %s\n", m.key, m.loc, m.oldScope, m.oldFrom, m.newScope, m.newFrom)
		}
		return errors.New(strings.TrimSuffix(sb.String(), "\n"))
	}
	total, inverted, scopes := 0, 0, 0
	for _, fd := range files {
		if len(fd.doc.Content) != 1 || fd.doc.Content[0].Kind != yaml.MappingNode {
			return fmt.Errorf("%s: want a mapping", fd.rel)
		}
		top := fd.doc.Content[0]
		if fd.isRoot {
			idx := mapKeyIndex(top, "version")
			if idx < 0 {
				return fmt.Errorf("%s: missing version", fd.rel)
			}
			top.Content[idx+1].Value = "4"
			top.Content[idx+1].Tag = "!!int"
			gateNode := mapValue(top, "gate")
			if gateNode == nil || gateNode.Kind != yaml.MappingNode {
				return fmt.Errorf("%s: missing gate", fd.rel)
			}
			pushNode := mapValue(gateNode, "push")
			if pushNode == nil || pushNode.Kind != yaml.MappingNode {
				return fmt.Errorf("%s: missing gate.push", fd.rel)
			}
			if mapRemoveKey(pushNode, "scope") {
				scopes++
			}
		}
		total += len(fd.verifications)
		expandScalarAliases(&fd.doc)
		iv, rm, err := rewriteV3Node(mapValue(top, "verifications"), fd.rel)
		if err != nil {
			return err
		}
		inverted += iv
		scopes += rm
	}
	for _, fd := range files {
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		if err := enc.Encode(&fd.doc); err != nil {
			enc.Close()
			return fmt.Errorf("%s: migrate: %w", fd.rel, err)
		}
		if err := enc.Close(); err != nil {
			return fmt.Errorf("%s: migrate: %w", fd.rel, err)
		}
		fd.transformed = buf.Bytes()
	}
	tmp, err := os.MkdirTemp("", "ytif-migrate-v4-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, fd := range files {
		dest := filepath.Join(tmp, filepath.FromSlash(fd.rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, fd.transformed, 0o644); err != nil {
			return err
		}
	}
	if _, err := LoadInventory(tmp); err != nil {
		return fmt.Errorf("migrated output failed to load: %w; expand YAML merges and aliases and retry", err)
	}
	var written []*v3FileData
	for _, fd := range files {
		target := filepath.Join(root, filepath.FromSlash(fd.rel))
		if err := os.WriteFile(target, fd.transformed, 0o644); err != nil {
			for _, prev := range written {
				pp := filepath.Join(root, filepath.FromSlash(prev.rel))
				_ = os.WriteFile(pp, prev.data, 0o644)
			}
			return fmt.Errorf("%s: %w", fd.rel, err)
		}
		written = append(written, fd)
	}
	fmt.Fprintf(out, "migrated %s: version 3 -> 4: %d verifications (%d push inverted, %d scope.push removed)\n", InventoryFile, total, inverted, scopes)
	return nil
}

// expandScalarAliases preserves the original value at consumers before a
// membership scalar anchor is inverted.
func expandScalarAliases(n *yaml.Node) {
	if n.Kind == yaml.AliasNode && n.Alias.Kind == yaml.ScalarNode {
		copy := *n.Alias
		copy.Anchor = ""
		*n = copy
	}
	for _, child := range n.Content {
		expandScalarAliases(child)
	}
}
