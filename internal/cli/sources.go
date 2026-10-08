package cli

import (
	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gate"
	"github.com/undervoke/ytif/internal/gateconf"
	"github.com/undervoke/ytif/internal/gohost"
	"github.com/undervoke/ytif/internal/runner/dotnet"
	"github.com/undervoke/ytif/internal/runner/gotest"
	"github.com/undervoke/ytif/internal/runner/jstest"
	"github.com/undervoke/ytif/internal/runner/nodeverify"
	"github.com/undervoke/ytif/internal/runner/playwright"
	"github.com/undervoke/ytif/internal/runner/vitest"
)

// sources lists every registration source the rail discovers and runs.
func sources() []check.Source {
	return []check.Source{
		&gohost.Source{},
		&gotest.Source{},
		&jstest.Source{Runner: jstest.Bun},
		&jstest.Source{Runner: jstest.Node},
		&nodeverify.Source{},
		&dotnet.Source{},
		&vitest.Source{},
		&playwright.Source{},
	}
}

// configChecker reconciles lefthook and GitHub Actions configuration.
func configChecker() gate.ConfigChecker {
	return gateconf.Checker{}
}
