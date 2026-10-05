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
	InventoryFile = "verification-inventory.yaml"
	RoutingFile   = "verification-routing.yaml"
	version       = 1
)

// Inventory is the contract ledger.
type Inventory struct {
	Version int     `yaml:"version"`
	Checks  []Entry `yaml:"checks"`
}

// Entry is one check's contract and placement.
type Entry struct {
	Runner     string `yaml:"runner"`
	Unit       string `yaml:"unit"`
	Name       string `yaml:"name"`
	Placement  string `yaml:"placement"`
	Accident   string `yaml:"accident"`
	Outcome    string `yaml:"outcome"`
	DeleteWhen string `yaml:"delete_when"`
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
		{"outcome", e.Outcome},
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
