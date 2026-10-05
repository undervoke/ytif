// Command ytif tracks every check with a contract and runs each at the gate
// its cost earns.
package main

import (
	"os"
	"runtime/debug"

	"github.com/undervoke/ytif/internal/cli"
)

// version is set by release builds through -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, buildVersion()))
}

func buildVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}
