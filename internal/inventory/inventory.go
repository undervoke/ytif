// Package inventory reads the contract ledger and the gate exemptions.
//
// The inventory records each check's contract and placement; it never
// registers a check. The exemptions list the gate entries that run outside
// ytif, each with the reason it is accepted, and carry no contracts.
package inventory

import (
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
	InventoryFile  = "ytif-inventory.yaml"
	ExemptionsFile = "ytif-gate-exemptions.yaml"
	version        = 2
)

// Inventory is the contract ledger.
type Inventory struct {
	Version    int        `yaml:"version"`
	Vocabulary Vocabulary `yaml:"vocabulary,omitempty"`
	Prepare    []Prep     `yaml:"prepare,omitempty"`
	Checks     []Entry    `yaml:"checks"`
}

// Prep is a command that produces what the named runners' discovery and
// runs need, such as generated sources or built dependencies. It judges
// nothing, so it carries no contract.
type Prep struct {
	Runners []string `yaml:"runners"`
	Run     []string `yaml:"run"` // argv, run from the repository root
}

// Entry is one check's contract and placement. Tags describe the accident
// in the built-in and project vocabularies; Requires and Ensures name other
// inventoried checks this one directly depends on or keeps working.
type Entry struct {
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

// Ref names another inventoried check.
type Ref struct {
	Runner string `yaml:"runner"`
	Unit   string `yaml:"unit"`
	Name   string `yaml:"name"`
}

// Key returns the referenced check key.
func (r Ref) Key() check.Key {
	return check.Key{Runner: r.Runner, Unit: r.Unit, Name: r.Name}
}

// Key returns the entry's check key.
func (e Entry) Key() check.Key {
	return check.Key{Runner: e.Runner, Unit: e.Unit, Name: e.Name}
}

// MissingContract names the contract fields the entry leaves empty.
func (e Entry) MissingContract() []string {
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

// LoadInventory reads InventoryFile under root. A missing file is an empty
// inventory.
func LoadInventory(root string) (Inventory, error) {
	inv := Inventory{Version: version}
	found, err := load(filepath.Join(root, InventoryFile), &inv)
	if err != nil || !found {
		return Inventory{Version: version}, err
	}
	if inv.Version != version {
		return Inventory{}, fmt.Errorf("%s: version %d, want %d", InventoryFile, inv.Version, version)
	}
	if err := inv.Vocabulary.validate(); err != nil {
		return Inventory{}, fmt.Errorf("%s: vocabulary: %w", InventoryFile, err)
	}
	for i, p := range inv.Prepare {
		if len(p.Runners) == 0 || len(p.Run) == 0 || p.Run[0] == "" {
			return Inventory{}, fmt.Errorf("%s: prepare[%d]: set runners and run", InventoryFile, i)
		}
	}
	return inv, nil
}

// LoadExemptions reads ExemptionsFile under root. A missing file is an empty
// list.
func LoadExemptions(root string) (Exemptions, error) {
	x := Exemptions{Version: version}
	found, err := load(filepath.Join(root, ExemptionsFile), &x)
	if err != nil || !found {
		return Exemptions{Version: version}, err
	}
	if x.Version != version {
		return Exemptions{}, fmt.Errorf("%s: version %d, want %d", ExemptionsFile, x.Version, version)
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

// load decodes one strict YAML document. It reports false for a missing file.
func load(path string, v any) (bool, error) {
	name := filepath.Base(path)
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
			return false, fmt.Errorf("%s: empty file; start it with version: %d", name, version)
		}
		return false, fmt.Errorf("%s: %w", name, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("%s: want exactly one YAML document", name)
	}
	return true, nil
}
