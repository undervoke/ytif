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
	"strings"

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

	inventoryVersion  = 3
	exemptionsVersion = 2

	// CommandRunner is the runner name for project-owned direct commands
	// folded into the verification list.
	CommandRunner = "command"
)

// Inventory is the verification ledger: shared configuration from the root
// file plus the merged verification list.
type Inventory struct {
	Version       int          `yaml:"version"`
	Vocabulary    Vocabulary   `yaml:"vocabulary,omitempty"`
	Prepare       []Prep       `yaml:"prepare,omitempty"`
	Board         BoardConfig  `yaml:"board,omitempty"`
	Includes      []string     `yaml:"includes,omitempty"`
	Verifications []Entry      `yaml:"verifications"`
	Files         []LedgerFile `yaml:"-"`
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
	Check  int `yaml:"check,omitempty"`
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
		check.GateCheck:  g.Check,
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
		}{{"check", f.g.Check}, {"commit", f.g.Commit}, {"push", f.g.Push}, {"ci", f.g.CI}} {
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
// on or keeps working. Check, Commit and SkipPush place the entry: every
// entry belongs to CI and, unless skipped, to push. Run, Cwd and Exclusive
// are set only for CommandRunner entries and keep the direct-command
// execution fields. Origin identifies the file and list position the entry
// was loaded from; it is diagnostics only and never serialized.
type Entry struct {
	Runner     string   `yaml:"runner"`
	Unit       string   `yaml:"unit,omitempty"`
	Name       string   `yaml:"name"`
	Check      bool     `yaml:"check,omitempty"`
	Commit     bool     `yaml:"commit,omitempty"`
	SkipPush   bool     `yaml:"skip-push,omitempty"`
	Run        []string `yaml:"run,omitempty"`
	Cwd        string   `yaml:"cwd,omitempty"`
	Exclusive  []string `yaml:"exclusive,omitempty"`
	Tags       []string `yaml:"tags,omitempty"`
	Requires   []Ref    `yaml:"requires,omitempty"`
	Ensures    []Ref    `yaml:"ensures,omitempty"`
	Accident   string   `yaml:"accident,omitempty"`
	Detection  string   `yaml:"detection,omitempty"`
	Impact     string   `yaml:"impact,omitempty"`
	DeleteWhen string   `yaml:"delete_when,omitempty"`
	Origin     string   `yaml:"-"` // file: verifications[i], diagnostics only
	File       string   `yaml:"-"` // file the entry was defined in
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

// Member reports whether the entry runs at the named gate. Check and commit
// membership is explicit; every entry belongs to CI and, unless skipped,
// to push. Unknown gate names are not members.
func (e Entry) Member(gate string) bool {
	switch gate {
	case "check":
		return e.Check
	case check.GateCommit:
		return e.Commit
	case check.GatePush:
		return !e.SkipPush
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
// version, vocabulary, prepare and board fields.
type fragmentFile struct {
	Includes      []string `yaml:"includes,omitempty"`
	Verifications []Entry  `yaml:"verifications"`
}

// LoadInventory reads InventoryFile under root and merges its recursively
// included fragments. A missing root and a missing legacy file load as an
// empty ledger. A legacy file alone, or beside the new root, is an error:
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
		return Inventory{Version: inventoryVersion}, nil
	}
	if !newStat.Mode().IsRegular() {
		return Inventory{}, fmt.Errorf("%s: not a regular file", InventoryFile)
	}
	var raw Inventory
	if err := decodeStrict(newPath, InventoryFile, &raw); err != nil {
		return Inventory{}, err
	}
	if raw.Version != inventoryVersion {
		return Inventory{}, fmt.Errorf("%s: version %d, want %d", InventoryFile, raw.Version, inventoryVersion)
	}
	if err := raw.Vocabulary.validate(); err != nil {
		return Inventory{}, fmt.Errorf("%s: vocabulary: %w", InventoryFile, err)
	}
	if err := validatePrepare(raw.Prepare, InventoryFile); err != nil {
		return Inventory{}, err
	}
	if err := raw.Board.validate(); err != nil {
		return Inventory{}, fmt.Errorf("%s: board: %w", InventoryFile, err)
	}
	realRoot, err := filepath.EvalSymlinks(newPath)
	if err != nil {
		return Inventory{}, fmt.Errorf("%s: %w", InventoryFile, err)
	}
	l := &fileLoader{root: root, visited: map[string]bool{realRoot: true}, seen: map[check.Key]string{}, files: []LedgerFile{{Path: InventoryFile}}}
	if err := l.addEntries(InventoryFile, raw.Verifications); err != nil {
		return Inventory{}, err
	}
	if err := l.addIncludes(filepath.Dir(newPath), InventoryFile, raw.Includes, []includeFrame{{real: realRoot, display: InventoryFile}}); err != nil {
		return Inventory{}, err
	}
	return Inventory{
		Version:       raw.Version,
		Vocabulary:    raw.Vocabulary,
		Prepare:       raw.Prepare,
		Board:         raw.Board,
		Includes:      raw.Includes,
		Verifications: l.out,
		Files:         l.files,
	}, nil
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
	dec := yaml.NewDecoder(f)
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
		target, err := resolveInclude(declDir, declDisplay, inc)
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

// resolveInclude maps one include reference to a filesystem path. The
// reference is relative to the declaring file's directory and must name an
// existing YAML regular file inside that directory's subtree, before and
// after symlink resolution.
func resolveInclude(declDir, declDisplay, inc string) (string, error) {
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
// to root/ytif-verifications.yaml (version 3). It validates the old shape,
// rewrites the document tree so comments survive, and preflights the result
// through the new strict reader before saving. Checks become verifications:
// a commit placement becomes commit: true, push and ci placements become the
// new push-and-CI default. Commands become runner: command entries with
// check: true, keeping their name, run, cwd and exclusive fields. Skip-push
// is never assigned automatically; the notice names the resulting push and
// CI expansion: the notice lists the exact keys that gain push membership
// so the host can add explicit exclusions. The legacy file is
// removed only after the new file is safely written; collisions preserve the
// input and reruns leave a migrated root unchanged.
func Migrate(root string, out io.Writer) error {
	oldPath := filepath.Join(root, LegacyInventoryFile)
	newPath := filepath.Join(root, InventoryFile)
	oldData, err := os.ReadFile(oldPath)
	if errors.Is(err, fs.ErrNotExist) {
		if _, nerr := os.Stat(newPath); nerr == nil {
			if _, lerr := LoadInventory(root); lerr != nil {
				return lerr
			}
			fmt.Fprintf(out, "already migrated: %s is current\n", InventoryFile)
			return nil
		}
		return fmt.Errorf("no %s in %s; nothing to migrate", LegacyInventoryFile, root)
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("%s and %s both exist; keep one ledger and remove the other", LegacyInventoryFile, InventoryFile)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := decodeLegacy(oldData); err != nil {
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
	counts, err := transformLegacy(&doc)
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
		return fmt.Errorf("migrated output failed to load: %w", err)
	}
	if pre.Version != inventoryVersion {
		return fmt.Errorf("migrated output has version %d, want %d", pre.Version, inventoryVersion)
	}
	if err := pre.Vocabulary.validate(); err != nil {
		return fmt.Errorf("migrated output has invalid vocabulary: %w", err)
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
	fmt.Fprintf(out, "migrated %s -> %s: %d verifications (%d commit:true, %d push+CI default, %d from commands as check:true)\n",
		LegacyInventoryFile, InventoryFile, total, counts.commit, counts.preset, counts.commands)
	if len(counts.gainedPush) > 0 {
		fmt.Fprintf(out, "note: these gain push membership (old ci-only checks and check-only commands); ytif did not assign skip-push, add \"skip-push: true\" where push must not run them:\n")
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
// the version-3 shape, preserving comments and unrelated content.
func transformLegacy(doc *yaml.Node) (migrateCounts, error) {
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
			v.Value = "3"
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
		if err := transformLegacyChecks(checksVal, &counts); err != nil {
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
	return counts, nil
}

// transformLegacyChecks converts each legacy check entry's placement into
// the new membership booleans: commit becomes commit: true, push and ci
// become the push-and-CI default.
func transformLegacyChecks(seq *yaml.Node, counts *migrateCounts) error {
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
		runner, unit, name, placement := "", "", "", ""
		placeIdx := -1
		for i := 0; i+1 < len(item.Content); i += 2 {
			switch item.Content[i].Value {
			case "runner":
				runner = item.Content[i+1].Value
			case "unit":
				unit = item.Content[i+1].Value
			case "name":
				name = item.Content[i+1].Value
			case "placement":
				placement = item.Content[i+1].Value
				placeIdx = i
			}
		}
		if strings.TrimSpace(runner) == "" || strings.TrimSpace(name) == "" {
			return fmt.Errorf("%s: checks[%d]: set runner and name", LegacyInventoryFile, n)
		}
		if placeIdx < 0 {
			return fmt.Errorf("%s: checks[%d]: set placement to commit, push or ci", LegacyInventoryFile, n)
		}
		switch placement {
		case "commit":
			item.Content[placeIdx].Value = "commit"
			item.Content[placeIdx+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}
			counts.commit++
		case "push":
			item.Content = append(item.Content[:placeIdx], item.Content[placeIdx+2:]...)
			counts.preset++
		case "ci":
			item.Content = append(item.Content[:placeIdx], item.Content[placeIdx+2:]...)
			counts.preset++
			counts.gainedPush = append(counts.gainedPush, check.Key{Runner: runner, Unit: unit, Name: name}.String())
		default:
			return fmt.Errorf("%s: checks[%d]: placement %q: want commit, push or ci", LegacyInventoryFile, n, placement)
		}
	}
	return nil
}

// transformLegacyCommands converts each legacy command into a command
// verification entry with check: true, reusing the original value nodes so
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
		add("check", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
		out = append(out, entry)
		counts.commands++
		counts.gainedPush = append(counts.gainedPush, check.Key{Runner: CommandRunner, Unit: unit, Name: name.Value}.String())
	}
	return out, nil
}

func isNull(n *yaml.Node) bool {
	return n.Tag == "!!null" || (n.Kind == yaml.ScalarNode && n.Value == "")
}
