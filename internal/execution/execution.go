// Package execution reads the execution list: the external project
// adapter a repository routes discovery and profile runs through, and the
// profiles its gates may select.
//
// The adapter owns its declared runners in full gates and profile gates;
// profiles own the project's existing command orchestration and report
// outcomes for inventoried checks, including native runner keys. A missing
// file means plain native execution: no adapter, no profiles.
package execution

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// File is the execution list under the repository root.
const File = "ytif-execution.yaml"

// version is the only execution list version this rail reads.
const version = 1

// Execution is the decoded execution list.
type Execution struct {
	Version  int                `yaml:"version"`
	Adapter  Adapter            `yaml:"adapter"`
	Profiles map[string]Profile `yaml:"profiles,omitempty"`
	// RequireProfile refuses unprofiled gates: native sources would run
	// the checks outside the adapter's fixture ownership, locks, and
	// timeouts. Missing means false, so repositories without owned
	// fixtures keep native execution.
	RequireProfile bool `yaml:"require_profile,omitempty"`
}

// Adapter is the external command the rail shells discovery and profile
// runs out to, and the runners whose keys it may discover.
type Adapter struct {
	Command []string `yaml:"command"`
	Runners []string `yaml:"runners"`
}

// Profile is one named selection of the adapter's orchestration. The
// description documents what the profile runs for gates and boards.
type Profile struct {
	Description string `yaml:"description"`
}

// Load reads File under root. It returns nil when the file is missing, so
// repositories without an adapter keep native execution.
func Load(root string) (*Execution, error) {
	path := filepath.Join(root, File)
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var ex Execution
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&ex); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: empty file; start it with version: %d", File, version)
		}
		return nil, fmt.Errorf("%s: %w", File, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: want exactly one YAML document", File)
	}
	if err := ex.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", File, err)
	}
	return &ex, nil
}

func (ex Execution) validate() error {
	var errs []error
	if ex.Version != version {
		errs = append(errs, fmt.Errorf("version %d, want %d", ex.Version, version))
	}
	if len(ex.Adapter.Command) == 0 {
		errs = append(errs, errors.New("adapter.command names no command"))
	}
	seen := map[string]bool{}
	for _, r := range ex.Adapter.Runners {
		switch {
		case r == "":
			errs = append(errs, errors.New("adapter.runners holds an empty runner"))
		case seen[r]:
			errs = append(errs, fmt.Errorf("adapter.runners lists %q twice", r))
		}
		seen[r] = true
	}
	if len(ex.Adapter.Runners) == 0 {
		errs = append(errs, errors.New("adapter.runners names no runner"))
	}
	for name, p := range ex.Profiles {
		if name == "" {
			errs = append(errs, errors.New("profiles holds an empty name"))
		}
		if p.Description == "" {
			errs = append(errs, fmt.Errorf("profiles %q has no description", name))
		}
	}
	return errors.Join(errs...)
}
