// Command ytif tracks every check with a contract and runs each at the gate
// its cost earns.
package main

import (
	"os"
	"runtime/debug"

	"github.com/undervoke/ytif/internal/cli"
	"github.com/undervoke/ytif/internal/launch"
)

// version is set by release builds through -ldflags "-X main.version=...".
var version = "dev"

func main() {
	v := buildVersion()
	if code, ok := launch.Delegate(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, v); ok {
		os.Exit(code)
	}
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, v))
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
