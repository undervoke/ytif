// Package launch lets a ytif installed on PATH run each command with the
// ytif version the working directory's Go module pins as a tool, and
// replaces that installed ytif with the latest release.
package launch

import (
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/undervoke/ytif/internal/gate"
)

const (
	module  = "github.com/undervoke/ytif"
	command = module + "/cmd/ytif"

	// delegatedEnv marks a ytif that Delegate started. A pinned build reports
	// its pinned version and so runs itself anyway; the mark keeps a build
	// that reports anything else from starting itself again without end.
	delegatedEnv = "YTIF_DELEGATED"
)

// Delegate runs args through `go tool ytif` when the working directory's
// module pins a ytif version other than self, and returns that run's exit
// code. It reports false without running anything when this ytif should run
// args itself: for update, for a development build, and outside a module
// that pins ytif.
func Delegate(args []string, stdin io.Reader, stdout, stderr io.Writer, self string) (int, bool) {
	if self == "dev" || os.Getenv(delegatedEnv) != "" || len(args) > 0 && args[0] == "update" {
		return 0, false
	}
	pin, ok := pinned()
	if !ok || pin == self {
		return 0, false
	}
	cmd := exec.Command("go", append([]string{"tool", "ytif"}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = append(os.Environ(), delegatedEnv+"=1")
	// An interrupt from the terminal reaches the pinned ytif too; this one
	// waits for it to stop its checks instead of exiting first.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	err := cmd.Run()
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) {
		return exit.ExitCode(), true
	}
	if err != nil {
		fmt.Fprintf(stderr, "ytif: run the pinned ytif %s: %v\n", pin, err)
		return gate.ExitUsage, true
	}
	return gate.ExitPass, true
}

// pinned returns the ytif version required by the working directory's
// module when that module declares ytif as a tool.
func pinned() (string, bool) {
	out, err := exec.Command("go", "mod", "edit", "-json").Output()
	if err != nil {
		return "", false
	}
	var mod struct {
		Require []struct{ Path, Version string }
		Tool    []struct{ Path string }
	}
	if json.Unmarshal(out, &mod) != nil {
		return "", false
	}
	if !slices.ContainsFunc(mod.Tool, func(t struct{ Path string }) bool { return t.Path == command }) {
		return "", false
	}
	for _, r := range mod.Require {
		if r.Path == module {
			return r.Version, true
		}
	}
	return "", false
}

// Update installs the latest ytif release with go install and reports the
// version change, warning when the install directory is not where this ytif
// runs from.
func Update(stdout, stderr io.Writer, self string) int {
	install := exec.Command("go", "install", command+"@latest")
	install.Stdout, install.Stderr = stdout, stderr
	if err := install.Run(); err != nil {
		fmt.Fprintf(stderr, "ytif update: %v\n", err)
		return gate.ExitUsage
	}
	path, err := installedPath()
	if err != nil {
		fmt.Fprintf(stderr, "ytif update: %v\n", err)
		return gate.ExitUsage
	}
	installed := "unknown"
	if info, err := buildinfo.ReadFile(path); err == nil {
		installed = info.Main.Version
	}
	fmt.Fprintf(stdout, "ytif %s -> %s (%s)\n", self, installed, path)
	if running, err := os.Executable(); err == nil && !samePath(running, path) {
		fmt.Fprintf(stderr, "ytif update: this ytif runs from %s; put %s first on PATH to use the update\n",
			running, filepath.Dir(path))
	}
	return gate.ExitPass
}

// installedPath returns where go install puts the ytif command.
func installedPath() (string, error) {
	out, err := exec.Command("go", "env", "-json", "GOBIN", "GOPATH").Output()
	if err != nil {
		return "", fmt.Errorf("go env: %w", err)
	}
	var env struct{ GOBIN, GOPATH string }
	if err := json.Unmarshal(out, &env); err != nil {
		return "", fmt.Errorf("go env: %w", err)
	}
	dir := env.GOBIN
	if dir == "" {
		dir = filepath.Join(filepath.SplitList(env.GOPATH)[0], "bin")
	}
	name := "ytif"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name), nil
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
