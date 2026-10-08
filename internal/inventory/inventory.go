// Package inventory reads the contract ledger and the routing list.
//
// The inventory records each check's contract and placement; it never
// registers a check. The routing list classifies gate entries that execute
// only external code and carries no contracts.
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
	InventoryFile = "ytif-inventory.yaml"
	RoutingFile   = "ytif-routing.yaml"
	version       = 2
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

// Routing classifies gate entries that execute only external code.
type Routing struct {
	Version int     `yaml:"version"`
	Entries []Route `yaml:"entries"`
}

// Route classifies one gate entry. Run matches a command exactly; Uses matches
// a GitHub Actions reference without its @ref.
type Route struct {
	Surface string `yaml:"surface"`
	Run     string `yaml:"run,omitempty"`
	Uses    string `yaml:"uses,omitempty"`
	Kind    string `yaml:"kind"`
}

const (
	SurfaceLefthook      = "lefthook"
	SurfaceGitHubActions = "github-actions"
	KindExternalTool     = "external-tool"
	KindInfrastructure   = "infrastructure"
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

// LoadRouting reads RoutingFile under root. A missing file is an empty list.
func LoadRouting(root string) (Routing, error) {
	r := Routing{Version: version}
	found, err := load(filepath.Join(root, RoutingFile), &r)
	if err != nil || !found {
		return Routing{Version: version}, err
	}
	if r.Version != version {
		return Routing{}, fmt.Errorf("%s: version %d, want %d", RoutingFile, r.Version, version)
	}
	for i, e := range r.Entries {
		if err := e.validate(); err != nil {
			return Routing{}, fmt.Errorf("%s: entries[%d]: %w", RoutingFile, i, err)
		}
	}
	return r, nil
}

func (e Route) validate() error {
	switch e.Surface {
	case SurfaceLefthook, SurfaceGitHubActions:
	default:
		return fmt.Errorf("surface %q: want %s or %s", e.Surface, SurfaceLefthook, SurfaceGitHubActions)
	}
	switch e.Kind {
	case KindExternalTool, KindInfrastructure:
	default:
		return fmt.Errorf("kind %q: want %s or %s", e.Kind, KindExternalTool, KindInfrastructure)
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
