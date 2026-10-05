# YTiF — your time is free

`ytif` keeps every check a project owns — tests and custom checks — on one
rail. Each check carries a contract that says why it exists and when to
delete it, runs at the gate its cost earns, and leaves a record of what it
cost and what it caught. Checks without a contract, contracts without a
check, and gate steps that run project code around the rail all fail the
gate.

- [Install](#install)
- [How it works](#how-it-works)
- [The inventory](#the-inventory)
- [Registration sources](#registration-sources)
- [Gates](#gates)
- [Gate configuration](#gate-configuration)
- [Agent guard](#agent-guard)
- [Records](#records)
- [Limits](#limits)

## Install

Download an archive for your platform from the
[releases](https://github.com/undervoke/ytif/releases) and verify it against
`SHA256SUMS`, or build from source:

```sh
go install github.com/undervoke/ytif/cmd/ytif@latest
```

Each archive holds one directory, `ytif_<version>_<os>_<arch>/`, with the
`ytif` executable, `LICENSE`, and this README.

## How it works

1. **Discover.** Every registration source lists the checks it would run:
   test runners from their own registration rules, and the Go host from
   `VerifyXxx` functions. One rule drives both listing and running, so the
   registry is what actually runs.
2. **Reconcile.** The registry is compared with
   `verification-inventory.yaml`, and the gate configuration with
   `verification-routing.yaml`.
3. **Run.** A gate runs the managed checks placed at it or at an earlier
   gate, records each outcome and invocation time, and fails on any failed
   check, any check without an outcome, or any reconciliation finding.

A check is identified by a key, `runner:unit:name`:

| Runner | Unit | Name |
|---|---|---|
| `go-verify` | package directory | function name |
| `go-test` | package directory | test, fuzz target, or example name |
| `bun-test`, `node-test` | test file | suite and test names joined by ` > ` |
| `dotnet-test` | `.csproj` path | fully qualified method name, argument lists removed |

Paths are relative to the repository root. Only the first two colons separate
the fields, so a name may contain colons.

## The inventory

`verification-inventory.yaml` is the contract ledger. It never registers a
check; it records why each registered check may stay.

```yaml
version: 1
checks:
  - runner: go-verify
    unit: internal/release
    name: VerifyChangelog
    placement: commit
    accident: A release bumps the version without a changelog entry.
    outcome: The release ships notes that do not describe it.
    delete_when: Release notes are generated from commits.
```

- `placement` is `commit`, `push`, or `ci`. Gates are cumulative: `commit`
  runs commit checks, `push` runs commit and push checks, `ci` runs all.
- `accident` names the ordinary change that the check catches, `outcome` the
  wrong result that change would otherwise ship, and `delete_when` the
  condition under which the check is removed. All three are required.

Reconciliation fails on:

| Finding | Meaning |
|---|---|
| `unregistered` | discovered, but not in the inventory |
| `stale` | in the inventory, but not discovered |
| `missing-contract` | an empty `accident`, `outcome`, or `delete_when` |
| `invalid-placement` | a placement other than `commit`, `push`, `ci` |
| `placement-order` | running a check necessarily runs another placed at a later gate |
| `duplicate` | one key listed or discovered twice |
| `unknown-runner` | a runner no registration source owns |
| `discovery-failed` | a source could not list everything; its entries are never called stale |
| `gate-config` | a gate step bypasses the rail or is not classified |

Managed checks still run when reconciliation fails; the gate fails at the
end.

## Registration sources

### Go checks (`go-verify`)

A Go check is a top-level function in a file that builds only with the
`verification` build tag, in a package other than `main`:

```go
//go:build verification

package release

import (
	"context"
	"fmt"
	"io"
)

func VerifyChangelog(ctx context.Context, files []string, w io.Writer) error {
	// Write one finding per line as path:line: message.
	fmt.Fprintln(w, "CHANGELOG.md:1: missing entry for v1.2.0")
	return fmt.Errorf("1 finding")
}
```

- The signature is
  `func VerifyXxx(ctx context.Context, [files []string], [provided...], w io.Writer) error`,
  where `Xxx` does not start with a lowercase letter. Checks use only
  standard-library types, so the project never imports ytif.
- `files` receives the gate's files (see [Gates](#gates)).
- A check returning `(T, error)` provides `T` to checks of the same package
  that take a `T` parameter. Selecting a check also runs its providers,
  whose outcomes are recorded; a failed provider leaves its dependents
  `blocked`. A provider must be placed at its dependents' gate or earlier.
- Types match by identity as far as the package's own source shows it:
  import paths, `byte`/`rune`/`any`, and aliases declared in the package
  resolve. An unnamed import is assumed to declare its last path element
  without a major-version suffix, as goimports assumes; unqualified names
  resolve through a dot import only when the file has exactly one; aliases
  declared in other packages do not resolve.
- A panic, including `panic(nil)`, fails the check. A `nil` error passes it.
- Generic, variadic, or misshapen checks, missing or duplicate providers,
  and provider cycles fail discovery.

ytif generates a dispatcher per module and import domain that exists only
through a `go build -overlay`, builds it with the `verification` tag added to
the module's own tags, and runs it from the repository root.

### Go tests (`go-test`)

Discovery follows `go test`'s own rules without compiling: `TestXxx(*testing.T)`,
`FuzzXxx(*testing.F)`, and examples with an output comment, in files that
the go command builds in the module — under its effective `GOOS`, `GOARCH`,
`CGO_ENABLED`, Go version, and `GOFLAGS` tags (quoted as the go command
reads them), as `go env` reports them. Each
package runs as `go test -json -run '^(names)$'`. A result replayed from the
test cache records no time of its own.

### bun and node tests (`bun-test`, `node-test`)

A `*.test.{ts,tsx,js,jsx,mjs,cjs,mts,cts}` file belongs to the runner whose
module it imports, `bun:test` or `node:test`, by name, as its default
export, or through its namespace. Tests must be registered statically:
`describe`/`suite` and `test`/`it` calls at the top level or directly inside
a suite callback, each named by a string literal that is not blank and holds
no control characters or line breaks. Computed names, `.each` tables,
registrations inside loops, helpers, hooks, or other tests, and test
functions used other than by calling them, `bind`, `call`, and `apply`
included, fail discovery. Each file
runs with an anchored name pattern from the repository root. When a pattern
cannot separate two tests (bun joins names with spaces; node also matches
suite names and trims names), they run together, and a gate holds back a
check whose run would also run a check it does not select.

### .NET tests (`dotnet-test`)

A tracked `.csproj` whose own `PackageReference` includes
`Microsoft.NET.Test.Sdk` is built and listed with
`dotnet vstest --ListFullyQualifiedTests`. The rows of a parameterized method
or fixture share the method's key; the method fails if any row fails.
Selected methods run with `dotnet test --filter`, naming each listed row
exactly. Methods of a project whose names differ only in case run together,
since some adapters match filter names case-insensitively.

## Gates

| Command | Placements | Files given to checks |
|---|---|---|
| `ytif commit` | commit | staged additions, copies, modifications, and renames |
| `ytif push` | commit, push | files changed by the pushed refs |
| `ytif ci` | commit, push, ci | every tracked file |

Checks read the working tree: `commit` scopes the files to staged paths, but
an unstaged edit to a staged file is what its checks see. An empty staged
set stays empty; it never widens to the whole tree. `push` reads the refs
that a pre-push hook receives on standard input, and without them compares
`@{push}` (or `origin/HEAD`) with `HEAD`. With lefthook:

```yaml
pre-commit:
  jobs:
    - run: ytif commit
pre-push:
  jobs:
    - run: ytif push
      use_stdin: true
```

```sh
ytif list              # managed checks and findings
ytif stats [--last N]  # recent time and failures per check
```

A gate also fails when a process fails outside its checks — a build error,
a crash, a `TestMain`, or a teardown hook that fails after every test
passed — without inventing outcomes for the checks. An interrupted gate
records only the outcomes and costs known to precede the interruption.

`ytif ci --report github` adds GitHub Actions annotations, turning
`path:line: message` lines into file annotations. Exit codes: `0` pass,
`1` fail, `2` usage or configuration error.

## Gate configuration

Every command a gate runs is classified: workflow `run` steps under the
step's shell and working directory, and in the lefthook configuration that
lefthook itself would load, every `run`, `args`, `files`, `skip`/`only`
`run`, `setup`, `rc`, and `lefthook` value with templates expanded. Shell
wrappers (`sh -c`, `eval`, `env`, `xargs`, `find -exec`, …) and
package-manager `exec`/`dlx` commands are read through; relative paths
resolve against the working directory.

- **The rail**: `ytif …`, including `go run …/cmd/ytif`.
- **Repository code**: local scripts and binaries, `go run` of the
  repository's packages, package scripts, task runners, lefthook `scripts`,
  step-level local actions, and the four test runners. These fail the gate;
  call `ytif` instead. A job may call a local reusable workflow, whose
  steps are classified like any other workflow's.
- **External code only**: must be listed in `verification-routing.yaml`,
  by the exact `run` text or by the action's `owner/repo`.

```yaml
version: 1
entries:
  - surface: lefthook
    run: gofmt -l {staged_files}
    kind: external-tool
  - surface: github-actions
    uses: actions/checkout
    kind: infrastructure
```

Commands that cannot be parsed or classified fail; routing never vouches
for them. So do `${{ }}` expressions inside `run` (pass them through `env`),
shells other than bash and sh — including the pwsh default of Windows
runners and the unknown default of a computed `runs-on` — lefthook options
ytif does not know, and a main lefthook configuration that is not YAML.
Local lefthook overlays of any format, `remotes`, and `extends` fail because
they change hooks outside the file.

## Agent guard

The gates are the only consumers of checks: git hooks and CI start them, and
each gate decides what runs. `ytif guard` reads a PreToolUse hook payload and
refuses agent shell commands that run checks themselves — `go test`,
`bun test`, `node --test`, `dotnet test`, `ytif commit`, `ytif push`, or
`ytif ci`, also through the wrappers the gate configuration reads through.
Gates that git hooks start do not pass through the agent's shell, so the
guard does not see them. Refusals are recorded; one that cannot be recorded
is reported on standard error.

For unattended work where accuracy is worth the time, `ytif allow 8h` lets
agents run these commands in the repository and its worktrees until the
duration passes; `ytif allow --off` ends it early, and `ytif allow`
shows it. The guard refuses an agent's own `ytif allow`, so the permission
comes from the user; an agent can still write the permission file itself.

Claude Code (`.claude/settings.json`) and Codex (`.codex/hooks.json`):

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": "ytif guard" }] }
    ]
  }
}
```

OpenCode and Pi: copy `integrations/opencode/ytif-guard.ts` into
`.opencode/plugins/`, or `integrations/pi/ytif-guard.ts` into
`.pi/extensions/` of a trusted project. When `ytif guard` cannot run, they
allow the command and show a warning, as a failing hook command does in
Claude Code.

## Records

Gates append JSON lines to `<git-common-dir>/ytif/records.jsonl`, shared by
worktrees: one line per outcome, per build or process with its wall time,
per check left without an outcome, per reconciliation finding, and per
guard refusal. `ytif stats` computes, on demand, average time and failures
per check, average time per runner build or process and unit, and refusals
per guarded runner. `--records PATH` writes or reads another file: in CI,
write the records to a file and upload it as an artifact even when the
checks fail, as this repository's `ci.yml` does.

## Limits

- Lint and analysis engines have no adapter yet; repository rules of those
  engines pass as external tools.
- Gate configuration does not follow package-manager lifecycle scripts or
  wrappers other than those listed above.
- Multi-targeted .NET test projects fail discovery. Projects that get the
  test SDK indirectly, through `Directory.Build.props` or an SDK such as
  `MSTest.Sdk`, are not discovered.
- Tests are discovered statically; dynamically registered tests are rejected
  rather than guessed.
- The guard sees one shell command at a time and cannot stop every way of
  running code.

## License

[MIT](LICENSE)
