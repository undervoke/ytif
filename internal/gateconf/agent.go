package gateconf

import (
	"slices"
	"strings"

	"github.com/undervoke/ytif/internal/shellcmd"
)

// The recognizers below serve the agent guard. The gates do not call them:
// what a gate step may run is decided by the classifier and the
// exemptions, while the guard refuses agent commands that run checks.

// ScriptRun is the package script a package manager command runs.
type ScriptRun struct {
	Manager       string   // npm, pnpm, yarn, bun, or node
	Name          string   // the script name
	Dir           string   // the directory option (-C, --dir, --prefix, --cwd), or ""
	Recursive     bool     // every workspace package
	Filters       []string // workspace packages selected by name, name pattern, or path
	Opaque        bool     // a selection or a passed word the guard does not resolve
	WorkspaceRoot bool     // the workspace root, with -w, or with the packages under --workspaces
	IgnoreScripts *bool    // npm's --ignore-scripts, which skips pre and post scripts; nil when not given
	Args          []string // the words passed on to the script, which the package manager appends to it
}

// PackageScript returns the package script cmd runs, if it runs one.
func PackageScript(cmd shellcmd.Command) (ScriptRun, bool) {
	r, _, _ := packageCall(cmd)
	return r, r.Name != ""
}

// Wrapped returns the commands cmd runs on its behalf: through npx, pnpx,
// bunx, bun x, or a package manager's exec or dlx, or the package binary a
// pnpm, yarn, or bun shorthand starts. A shorthand starts the binary only
// when the package has no script of that name, the one PackageScript names.
func Wrapped(cmd shellcmd.Command) (cmds []shellcmd.Command, shorthand bool) {
	switch cmd.NameBase() {
	case "npx", "pnpx", "bunx":
		return execCommands(cmd, 1), false
	}
	_, exec, binary := packageCall(cmd)
	switch {
	case exec >= 0:
		return execCommands(cmd, exec), false
	case binary >= 0:
		return wrappedCommands(cmd.Rest(binary)), true
	}
	return nil, false
}

// packageCall reads a package manager command: the script it runs, the
// index of the command its exec runs, and the index of a shorthand that may
// start a package binary instead; -1 for none.
func packageCall(cmd shellcmd.Command) (r ScriptRun, exec, binary int) {
	exec, binary = -1, -1
	switch name := cmd.NameBase(); name {
	case "npm", "pnpm", "yarn", "bun":
		r.Manager = name
	case "node":
		r.Manager = name
		var next int
		if r.Name, next = nodeRun(cmd); r.Name != "" {
			r = trailing(r, cmd, next)
		}
		return r, exec, binary
	default:
		return r, exec, binary
	}
	expect := false // run was named; the next word is the script
	foreach := false
	for i := 1; i < len(cmd.Args); i++ {
		if !cmd.Literal[i] {
			return ScriptRun{}, -1, -1
		}
		a := cmd.Args[i]
		if a == "--" {
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flag, val, hasVal := strings.Cut(a, "=")
			if takesValue(r.Manager, flag, foreach) && !hasVal {
				if i+1 >= len(cmd.Args) || !cmd.Literal[i+1] {
					return ScriptRun{}, -1, -1
				}
				i++
				val = cmd.Args[i]
			}
			if r.Manager == "bun" && inlineFlags["node"][flag] {
				return ScriptRun{}, -1, -1
			}
			packageFlag(&r, flag, val, foreach)
			continue
		}
		if expect {
			if r.Manager == "bun" && fileOperand(a) {
				return ScriptRun{}, -1, -1
			}
			r.Name = a
			return trailing(r, cmd, i+1), exec, binary
		}
		switch sub := a; {
		case sub == "run" || (sub == "run-script" && r.Manager != "yarn") ||
			(r.Manager == "npm" && (sub == "rum" || sub == "urn")):
			expect = true
		case r.Manager == "pnpm" && (sub == "recursive" || sub == "multi" || sub == "m"):
			r.Recursive = true
		case r.Manager == "yarn" && sub == "workspace" && i+1 < len(cmd.Args):
			r.Filters = append(r.Filters, cmd.Args[i+1])
			i++
		case r.Manager == "yarn" && sub == "workspaces" && i+1 < len(cmd.Args) && cmd.Args[i+1] == "foreach":
			r.Recursive, foreach = true, true
			i++
		case sub == "exec" || sub == "dlx" || (sub == "x" && (r.Manager == "npm" || r.Manager == "bun")):
			return ScriptRun{}, i + 1, -1
		case r.Manager == "yarn" && sub == "node":
			return ScriptRun{}, -1, i
		case r.Manager == "bun" && sub == "test":
			return ScriptRun{}, -1, -1 // a test runner
		case sub == "test" || sub == "t" || sub == "tst" ||
			(r.Manager == "npm" && slices.Contains(npmInstallTests, sub)):
			r.Name = "test"
			return trailing(r, cmd, i+1), exec, binary
		case r.Manager == "npm":
			if sub == "start" || sub == "stop" || sub == "restart" {
				r.Name = sub
				return trailing(r, cmd, i+1), exec, binary
			}
			return ScriptRun{}, -1, -1
		case r.Manager == "pnpm" && pnpmBuiltins[sub],
			r.Manager == "yarn" && yarnBuiltins[sub],
			r.Manager == "bun" && (bunBuiltins[sub] || fileOperand(sub)):
			return ScriptRun{}, -1, -1
		default:
			r.Name = sub // a script, or else a package binary
			return trailing(r, cmd, i+1), exec, i
		}
	}
	return ScriptRun{}, -1, -1
}

// trailing reads the words after the script name into r.Args, the words
// passed on to the script. npm still reads its own options there, up to --;
// the other package managers pass every word on. The first -- only
// separates them.
func trailing(r ScriptRun, cmd shellcmd.Command, i int) ScriptRun {
	sep := false
	for ; i < len(cmd.Args); i++ {
		a := cmd.Args[i]
		switch {
		case !cmd.Literal[i]:
			r.Opaque = true
			return r
		case a == "--" && !sep:
			sep = true
		case r.Manager == "npm" && !sep && strings.HasPrefix(a, "-"):
			flag, val, hasVal := strings.Cut(a, "=")
			if npmValueFlags[flag] && !hasVal && i+1 < len(cmd.Args) {
				i++
				if !cmd.Literal[i] {
					r.Opaque = true
					return r
				}
				val = cmd.Args[i]
			}
			packageFlag(&r, flag, val, false)
		default:
			r.Args = append(r.Args, a)
		}
	}
	return r
}

var npmInstallTests = []string{"it", "install-test", "cit", "install-ci-test", "clean-install-test", "sit"}

// takesValue reports whether a package manager option takes the next word.
func takesValue(manager, flag string, foreach bool) bool {
	switch {
	case foreach:
		return foreachValueFlags[flag]
	case manager == "npm":
		return npmValueFlags[flag]
	case manager == "pnpm":
		return pnpmValueFlags[flag]
	case manager == "yarn":
		return yarnValueFlags[flag]
	case manager == "bun":
		return bunValueFlags[flag]
	}
	return false
}

var foreachValueFlags = set("--from", "--include", "--exclude", "-j", "--jobs")

// packageFlag records an option that selects where the script runs. A
// boolean option is on unless its value is false, and npm's --no- form
// turns it off.
func packageFlag(r *ScriptRun, flag, val string, foreach bool) {
	on := val != "false"
	switch {
	case foreach:
		if flag == "--from" || flag == "--include" || flag == "--exclude" || flag == "--since" {
			r.Opaque = true
		}
	case r.Manager == "pnpm":
		switch flag {
		case "-C", "--dir":
			r.Dir = val
		case "-r", "--recursive":
			r.Recursive = on
		case "-F", "--filter", "--filter-prod":
			r.Filters = append(r.Filters, val)
		case "-w", "--workspace-root":
			r.WorkspaceRoot = on
		}
	case r.Manager == "npm":
		if rest, ok := strings.CutPrefix(flag, "--no-"); ok {
			flag, on = "--"+rest, false
		}
		switch flag {
		case "--prefix":
			r.Dir = val
		case "-w", "--workspace":
			r.Filters = append(r.Filters, val)
		case "-ws", "--workspaces":
			r.Recursive = on
		case "-iwr", "--include-workspace-root":
			r.WorkspaceRoot = on
		case "--ignore-scripts":
			r.IgnoreScripts = &on
		}
	case r.Manager == "yarn" || r.Manager == "bun":
		switch flag {
		case "--cwd":
			r.Dir = val
		case "-F", "--filter":
			r.Filters = append(r.Filters, val)
		}
	}
}

// fileOperand reports whether bun reads a word as a file to run rather
// than a script name.
func fileOperand(a string) bool {
	if strings.Contains(a, "/") {
		return true
	}
	switch path := strings.ToLower(a); {
	case strings.HasSuffix(path, ".js"), strings.HasSuffix(path, ".mjs"), strings.HasSuffix(path, ".cjs"),
		strings.HasSuffix(path, ".ts"), strings.HasSuffix(path, ".mts"), strings.HasSuffix(path, ".cts"),
		strings.HasSuffix(path, ".jsx"), strings.HasSuffix(path, ".tsx"):
		return true
	}
	return false
}

// nodeRun returns the script of node --run, which comes among node's own
// options, and the index of the word after it.
func nodeRun(cmd shellcmd.Command) (string, int) {
	for i := 1; i < len(cmd.Args) && cmd.Literal[i] && strings.HasPrefix(cmd.Args[i], "-"); i++ {
		a := cmd.Args[i]
		if v, ok := strings.CutPrefix(a, "--run="); ok {
			return v, i + 1
		}
		if a == "--run" && i+1 < len(cmd.Args) && cmd.Literal[i+1] {
			return cmd.Args[i+1], i + 2
		}
		if nodeValueFlags[a] {
			i++
		}
	}
	return "", 0
}
