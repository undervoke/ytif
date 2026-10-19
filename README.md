# YTiF — your time is free, or so your AI thinks.

> Built for my own projects; issues and pull requests are closed. Fork freely.

`ytif` keeps every check a project owns — tests and custom checks — on one
rail. Each check carries a contract that says why it exists and when to
delete it, runs at the gate its cost earns, and leaves a record of what it
cost. Checks without a contract, contracts without a check, gate steps that
run tests around the rail, and other gate steps that run code outside ytif
without a stated reason fail the gate.

## Install

Pin ytif per project, so hooks, CI, and every clone run the version the
project's files were written for. In a Go module (Go 1.24 or later):

```sh
go get -tool github.com/undervoke/ytif/cmd/ytif@v0.8.0
```

`go.mod` and `go.sum` then hold the version, and `go tool ytif` runs it;
read every `ytif` below as `go tool ytif`. Elsewhere, download a release
archive and verify it against `SHA256SUMS`, or run
`go run github.com/undervoke/ytif/cmd/ytif@v0.8.0`. The gates also need
the test runners the project uses.

For typing `ytif` in any project, also install it on PATH with
`go install github.com/undervoke/ytif/cmd/ytif@latest`. Inside a Go module
that pins ytif as a tool, it runs every command through `go tool ytif`
when the pinned version differs from its own, so the project's files are
always read by the version they were written for; elsewhere it runs
itself. `ytif update` replaces it with the latest release and leaves each
project's pin alone.

To adopt it, run `ytif init` from the installed ytif at the repository
root. It pins that ytif's version as a tool, creates the inventory and the
exemptions, wires `ytif commit` and `ytif push` into lefthook and
`ytif ci` into a workflow, and registers the agent guard below for Claude
Code, Codex, OpenCode, and Pi. It keeps what is already there and adds
only what is missing, so it can rerun; a file it cannot edit, such as
JSON with comments or a lefthook configuration that is not YAML, stops it
before it changes anything. It needs a root `go.mod`, does not lower a
newer pin, and leaves trusting the hooks in Codex and Pi to the user.
Before writing integration files it builds the pinned tool with `go tool -n ytif`.
If a pin change would leave an active Go vendor tree stale, init stops before
changing files and gives the pin-and-vendor commands to run before retrying.
It leaves `vendor/` untouched; an unexpected Go command failure may still leave
changes in `go.mod` or `go.sum`. CI discovery includes tracked and nonignored
unstaged workflows. When adding the push gate to a parallel pre-push hook that
already has a stdin consumer, init makes the hook sequential so both consumers
receive the complete push refs.
Then stage the changes and any new check files and run `ytif list`. Every
discovered check without a contract and every gate step that bypasses the
rail is a finding, and the gates fail until none remain. Write the
contracts and exemptions and rerun `ytif list`.

## Checks

Gate checks are discovered, never registered by hand. A key is `runner:unit:name`:

| Runner | Discovered from | Unit | Name |
|---|---|---|---|
| `go-verify` | `VerifyXxx` functions in `//go:build verification` files | package directory | function |
| `node-verify` | exported `verifyXxx` functions in `*.verify.mjs` files | file | function |
| `go-test` | `TestXxx`, `FuzzXxx`, examples with output | package directory | function |
| `bun-test`, `node-test` | `*.test.*` files importing `bun:test` or `node:test` | file | `suite > test` |
| `dotnet-test` | `.csproj` referencing `Microsoft.NET.Test.Sdk` | `.csproj` path | method |
| `vitest-test` | `vitest list` of each `vitest.config.*`, or a `vite.config.*` that mentions vitest | test file | `suite > test` |
| `playwright-test` | `playwright test --list` of each `playwright.config.*` | spec file | `describe > test` |

A Go check uses only standard-library types:

```go
func VerifyXxx(ctx context.Context, [files []string], [provided...], w io.Writer) error
```

It writes findings as `path:line: message` and fails by returning an error;
what a passing check writes is shown as its log, and `--report github` turns
its findings into warnings. A check returning `(T, error)`
provides `T` to checks of the same package that take it. A Node check is an
exported function declaration that writes findings the same way and fails by
throwing:

```js
export async function verifyXxx({ files, signal }, out) // out.write(text)
```

A passing Playwright test's stdout and stderr are shown as its log too, so
workflow commands it writes, such as `::warning`, reach GitHub.

Test names must be static; what ytif cannot list exactly fails discovery.

Discovery reads Git-tracked files only, so an untracked new test stays
invisible until staged. `ytif list` and every gate discover all runners
whatever the placement, and .NET discovery builds the test projects. A .NET
project whose discovered methods are all selected runs without a filter; a
filter too long for one command line is split across several runs.

Vitest and Playwright are listed by the project's own `node_modules/.bin`
binary from each config's directory, so their discovery needs the
dependencies installed and whatever the test files import built. A test
that several projects run is one check, and a name repeated within one file
and project fails discovery. A Vitest config's listing is cached in the git
common directory by the tracked paths, the files under the config's
directory, the lockfiles, and the listed test files; a test name computed
from any other file refreshes only when one of those changes.

What discovery and runs need generated or built first, the inventory
declares as preparation. Every `ytif list` and gate runs each command in
order from the repository root before discovery, and a failed command fails
discovery of its runners:

```yaml
prepare:
  - runners: [vitest-test]
    run: [pnpm, exec, nx, run-many, -t, prepare]
```

## Pre-gate commands

`ytif check --jobs 4` runs every command registered under `commands:` in
`ytif-inventory.yaml`, without gate preparation or test discovery. Agents
may run this command. Projects choose what is useful here; ytif does not
admit commands by a tool allowlist or an elapsed-time threshold.

```yaml
version: 2
commands:
  - name: formatting
    run: [pnpm, exec, prettier, --list-different, .]
  - name: maps
    run: [node, tools/maps/env.mjs, --check]
    exclusive: [maps]
  - name: catalog
    run: [node, tools/catalog/cli.mjs, --check]
checks: []
```

Each entry has a unique `name` and a `run` argv. `cwd` optionally changes
its directory from the repository root. `exclusive` is an optional list of
resource names: entries sharing a name never overlap. The project owns
these declarations; tokens express mutual exclusion, not dependencies,
ordering, or duplicate-work detection. `--jobs N` bounds concurrently
running entries, not workers inside their tools; it defaults to 2,
and `--jobs 1` runs sequentially in list order. There is no subset
selection. Independent entries run even after another fails. Output is
prefixed by entry name, followed by a result and elapsed time for each.
Ctrl-C cancels running process groups and stops pending entries.

Before starting any entry, ytif reads the entire list and rejects known
test runners, explicit build commands, and package/Make/Nx tasks with a
`test` or `build` word in their names. This keeps builds and tests at the
gates. It also rejects calls to ytif gates, to `check` itself (recursion),
and to `allow` (the user's exception mechanism). It follows literal shell
wrappers and package scripts with their arguments and lifecycle hooks,
using the same bounded traversal as the agent guard. For Nx it reads
`project.json`, `package.json` Nx targets/scripts, and `nx.json` target
settings, following explicit target dependencies and declared package or
implicit project edges. `nx affected` is inspected against every non-excluded
static project, since future diffs may select any of them. Build/test-named
dependencies are rejected by name even when no current dependency matches;
register the direct check command when that task name obscures a pure check.
It does not execute Nx or its plugins to classify commands. Inferred targets,
workspace packages without `project.json` or `package.json` Nx metadata,
import-inferred edges, computed options, changed/unknown directories, and
dynamic calls inside arbitrary programs remain the project's responsibility.
TypeScript emission checks read strict-JSON configs and relative `extends`;
JSONC and package-resolved configs remain unresolved. Unknown option arity
leaves file operands unresolved, but a known emitting config is still rejected.
Unknown branches pass; admission is not a guarantee that a command contains no
hidden build or test. Reclassification happens on every invocation, so
editing a registered script changes the next decision.

Completed commands record timing and outcomes under the separate `check`
lane: `ytif stats --gate check`. `--records PATH` uses another record file.
Interrupted or unstarted commands have no completed result. A pass does
not replace any gate check. Exit codes are `0` when every command passes,
`1` for execution failures or interruption, and `2` for invalid registration,
rejected commands, or a recording failure.

## The inventory

`ytif-inventory.yaml` holds one contract per discovered check:

```yaml
version: 2
vocabulary:                    # the project's own tag groups
  where:
    release: Release
    install: { en: Install, ko: 설치 }
checks:
  - runner: go-verify
    unit: internal/release
    name: VerifyChangelog
    placement: commit          # commit, push, or ci
    tags: [user, mistake, misdirection, release, silent, manual]
    requires:                  # optional: checks this one depends on
      - { runner: go-test, unit: internal/release, name: TestVersionBump }
    accident: A release bumps the version without a changelog entry.
    detection: Reads the version and the changelog, and fails when the version has no entry.
    impact: The release ships notes that do not describe it.
    delete_when: Release notes are generated from commits.
```

`accident` is an ordinary, unintended change the check catches,
`detection` what the check runs and observes so that the accident fails
it, `impact` what users see when that change ships, and `delete_when` an
observable condition for removing the check; all four are required. You
choose the placement: an earlier gate for a check worth its cost there, as
`ytif stats` shows.

Tags describe the accident. ytif ships these groups; every check carries
exactly one `visibility` and one `recovery` tag, and any number of the rest:

| Group | Tags |
|---|---|
| `what` (harm) | `code-execution`, `permission-bypass`, `exposure`, `data-loss`, `misdirection`, `outage` |
| `who` (from whom) | `user`, `agent`, `attacker`, `fault` |
| `why` (behavior prevented) | `habit`, `mistake`, `attack` |
| `visibility` | `silent`, `visible` |
| `recovery` | `irreversible`, `manual` |

`vocabulary` declares further groups; a label is one text or one per
language (`en`, `ko`), and a tag name may appear in one group only.
`requires` and `ensures` name other inventoried checks this one depends on
or keeps working. An unknown tag, a missing `visibility` or `recovery` tag,
and a relation to a check outside the inventory are findings.

## Gates

| Command | Runs checks placed at | Files given to checks |
|---|---|---|
| `ytif commit` | commit | staged files |
| `ytif push` | commit, push | files changed by the pushed refs |
| `ytif ci` | commit, push, ci | every tracked file |

Placement selects the checks; the files are input for verify checks, not a
filter on tests. In an Nx workspace, `ytif ci --base REF` also leaves out the
test checks of projects that the changes since REF do not affect; verify
checks and tests outside every project still run. Something must still run
`ytif ci` without `--base`, such as the push to the main branch.

The runners of a gate run at once, and `--jobs N` (default: the CPU count)
bounds how many processes ytif runs together while discovering and running
checks: test processes, builds, listings, and dispatchers. Preparation and
`--base` narrowing run alone, before and after discovery; `go env`,
`go mod edit`, and git run outside the bound. Parallelism inside a process,
such as a test framework's workers, stays the project's configuration.
Checks that may run together must not share ports, build outputs, or
temporary paths. Output a check passes through is printed a line at a time,
prefixed with `[runner]`; results print in runner order once every runner
has finished. One CI job running `ytif ci` does the work of a job per
runner without repeating the checkout and setup.

```yaml
# lefthook.yml
pre-commit:
  jobs:
    - run: go tool ytif commit
pre-push:
  jobs:
    - run: go tool ytif push
      use_stdin: true
```

Run `lefthook install` to activate the hooks. In GitHub Actions:

```yaml
steps:
  - uses: actions/checkout@v7
  - uses: actions/setup-go@v7
    with:
      go-version-file: go.mod
  - run: go tool ytif ci --jobs 2 --report github --records ytif-records.jsonl
  - if: always()
    uses: actions/upload-artifact@v7
    with:
      name: ytif-records
      path: ytif-records.jsonl
      if-no-files-found: ignore
```

Its actions need exemptions. Annotations do not keep records; read a
downloaded artifact with `ytif stats --records PATH`.

Every command in lefthook and GitHub workflows must call `ytif` (directly,
through `go tool ytif`, or through `go run` of its package) or be listed in
`ytif-gate-exemptions.yaml` with the reason it runs outside ytif, whether
it runs external code or the repository's own, such as an Nx build that
publishes a cache. A `run` entry matches a step's whole text, and a `uses`
entry the action's `owner/repo` without its ref. Test runners cannot be
exempted: a step that runs `go test`, `bun test`, `node --test`,
`dotnet test`, `vitest`, or `playwright test`, directly or through a
package manager's exec, fails even when listed. Commands that run no tests
can be listed: `vitest list`, `bench`, and `init`; vitest with only one of
`--help`, `--version`, `--clearCache`, `--listTags`, or `--mergeReports`;
playwright commands other than `test`; and `playwright test` with only
`--list` or `--help`. Any other form counts as a test run. Lefthook scripts
and local actions cannot be exempted either; call `ytif` from a run step
instead.

```yaml
version: 2
entries:
  - surface: github-actions
    uses: actions/checkout
    reason: Fetches the repository.
  - surface: lefthook
    run: gofmt -l {staged_files}
    reason: Formatting is judged by gofmt.
```

An exemption matches the text of the step, not what that text runs: a
script, package script, or Nx target that changes behind an unchanged step
keeps its exemption, so review such changes as you would the step.

`ytif list` shows checks and findings; `ytif stats` shows recorded time and
hits, where a hit is a run of consecutive fails that a pass ends.
`ytif ci --report github` emits annotations.

## Board

`ytif board` writes the inventory and its records as one static HTML file,
`<git-common-dir>/ytif/board.html`, and opens it; `--out PATH` only writes
it, and `--records PATH` reads other record files, such as a CI artifact.
Its pages list and filter checks by tag, draw a check's relations and
nearest checks, show each check's cost and hits, and lay out the
vocabulary, in English or Korean. Exit codes: `0` pass,
`1` fail, `2` usage or configuration error.

To work on the page itself, run from this repository's root:

```sh
go run ./internal/board/devserve <repo>
```

It serves the board of `<repo>`, a repository with an inventory and records,
at `http://127.0.0.1:8765`, rebuilding the page from
`internal/board/assets` on each request, so an edit shows on reload.

## Agent guard

Agents run registered pre-gate commands through `ytif check`; other checks
are left to the Git hooks and CI that start the gates.
`ytif guard` is a PreToolUse hook that refuses agent commands that run
checks. It refuses nothing until an agent's configuration runs it, which
`ytif init` writes. For Claude Code (`.claude/settings.json`) and Codex
(`.codex/hooks.json`), that is:

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": "go tool ytif guard" }] }
    ]
  }
}
```

For OpenCode and Pi, it writes `integrations/opencode/ytif-guard.ts` to
`.opencode/plugins/` and `integrations/pi/ytif-guard.ts` to
`.pi/extensions/`, with its `YTIF` command set to `go tool ytif`. Codex
runs a project's hooks only after the user trusts them in `/hooks`.

It refuses, directly or through `npx`, a package manager's `exec` or
`dlx`, or a package binary shorthand such as `pnpm vitest` where the
package has no script of that name:

- a ytif gate, and `ytif allow`;
- a test runner, as the gate configuration check knows them;
- a `*.verify.mjs` module run as the program of `node`, `tsx`, or `bun`;
- a package script, Nx target, or Make goal whose name has `test`,
  `check`, or `verify` as a word of its own, set off by the ends of the
  name or by characters other than letters and digits: `pnpm test:fast`,
  `nx run-many -t lint test`, `make check`, but not `pnpm typecheck`;
- `prettier --check`, `tsc --noEmit` and `vue-tsc --noEmit`, `eslint`
  without `--fix`, `go vet`, `gofmt -l` or `-d`, and `go mod tidy -diff`;
- a package script that does any of these, read from `package.json` with
  the words passed on to it and the pre and post scripts the package
  manager runs, in the packages `-r`, `--filter`, or `--workspace` select
  by name or path: `pnpm run lint --fix` passes where `pnpm run lint`
  is refused.

It reads only the scripts a command names. A script whose directory it
cannot tell, a selection by dependency, change, or exclusion, a script
passed a word that is not literal, and Nx target and Make recipe bodies
are not read, so a check run there, or one named only in an argument
(`node tools/smoke.mjs test`), passes. So does `eslint` or `gofmt -l`
given a word that is not literal, which could be the option that edits.

Only the user grants an exception: `ytif allow 8h` lets agents run checks
directly for unattended work, and `ytif allow --off` ends it. An agent's
`ytif allow` is refused even then, including one behind a package script.
A check run directly leaves no record.

## License

[MIT](LICENSE)
