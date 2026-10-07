package cli

import (
	"fmt"
	"sort"

	"github.com/undervoke/ytif/internal/adapter"
	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/execution"
	"github.com/undervoke/ytif/internal/gate"
	"github.com/undervoke/ytif/internal/gateconf"
	"github.com/undervoke/ytif/internal/gohost"
	"github.com/undervoke/ytif/internal/runner/dotnet"
	"github.com/undervoke/ytif/internal/runner/gotest"
	"github.com/undervoke/ytif/internal/runner/jstest"
)

// sources lists every registration source the rail discovers and runs: the
// native sources, plus the execution adapter when the repository configures
// one. An adapter runner that collides with a native runner is a
// configuration error, since one runner has exactly one owner.
func sources(root string) ([]check.Source, error) {
	srcs := []check.Source{
		&gohost.Source{},
		&gotest.Source{},
		&jstest.Source{Runner: jstest.Bun},
		&jstest.Source{Runner: jstest.Node},
		&dotnet.Source{},
	}
	ex, err := execution.Load(root)
	if err != nil {
		return nil, err
	}
	if ex == nil {
		return srcs, nil
	}
	owned := map[string]bool{}
	for _, s := range srcs {
		for _, r := range s.Runners() {
			owned[r] = true
		}
	}
	for _, r := range ex.Adapter.Runners {
		if owned[r] {
			return nil, fmt.Errorf("%s: adapter runner %q is already owned by a native source", execution.File, r)
		}
	}
	return append(srcs, adapter.New(ex.Adapter.Command, ex.Adapter.Runners, "")), nil
}

// configChecker reconciles lefthook and GitHub Actions configuration.
func configChecker() gate.ConfigChecker {
	return gateconf.Checker{}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
