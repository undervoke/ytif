# YTiF — your time is free, or so your AI thinks.

> Built for my own projects; issues and pull requests are closed.

`ytif` keeps every verification a project owns — tests, custom checks, and
direct commands — on one rail. Each discovered verification carries a contract
that says why it exists and when to delete it; every verification belongs to
the gates its ledger entry names and leaves a record of what it cost.
Verifications without a contract,
contracts without a verification, and gate steps that run code outside ytif
without a stated reason fail the gate.

## Install

Pin ytif per project, so hooks, CI, and every clone run the same version
(Go 1.24 or later):

```sh
go get -tool github.com/undervoke/ytif/cmd/ytif@v0.10.0
```

Read every `ytif` below as `go tool ytif`. Outside a Go module, download a
release archive and verify it against `SHA256SUMS`.

Existing projects add `--hook` to their lefthook commit and push commands to
keep recording.

To upgrade a project whose ledger is a version 2 `ytif-inventory.yaml`, raise
the pin and run `ytif migrate`. It lists the verifications that now also run
at push; add `skip-push: true` where push must not run them.

## Adopt

```sh
ytif init --hosts claude-code,codex
```

Init needs a root `go.mod`. It pins ytif as a tool, creates the ledger and the
exemptions, wires `ytif commit` and `ytif push` into lefthook and `ytif ci`
into a GitHub workflow, and registers the agent guard for the selected hosts. <!-- dinyd: internal/initcmd/initcmd.go#Run s1:268a94cada7b997c -->
In ytif's own repository, init registers the checkout's command for gates. <!-- dinyd: internal/initcmd/initcmd.go#pinStep s1:cc8001f009c097c4 -->
Agent guards in this repository use the installed ytif release on PATH, keeping their executable independent of source edits. <!-- dinyd: internal/initcmd/initcmd.go#plan s1:af77baff63db28bd -->
Run `lefthook install`, then follow the approval steps init prints for each
host.

Then stage new verification files and run `ytif list`. Every discovered
verification without a contract and every gate step that bypasses the rail is
a finding; the gates fail until none remain.

## Verifications

Tests and verify functions are discovered from Git-tracked files; direct
commands are registered in the ledger. A key is `runner:unit:name`:

| Runner | Discovered from | Unit | Name |
|---|---|---|---|
| `go-verify` | `VerifyXxx` functions in `//go:build verification` files | package directory | function |
| `node-verify` | exported `verifyXxx` functions in `*.verify.mjs` files | file | function |
| `go-test` | `TestXxx`, `FuzzXxx`, examples with output | package directory | function |
| `bun-test`, `node-test` | `*.test.*` files importing `bun:test` or `node:test` | file | `suite > test` |
| `dotnet-test` | `.csproj` referencing `Microsoft.NET.Test.Sdk` | `.csproj` path | method |
| `vitest-test` | `vitest list` of each `vitest.config.*`, or a `vite.config.*` that mentions vitest | test file | `suite > test` |
| `playwright-test` | `playwright test --list` of each `playwright.config.*` | spec file | `describe > test` |
| `command` | ledger `run` argv | `cwd` or `.` | entry `name` |

Verify functions write findings as `path:line: message` and fail by returning
an error (Go) or throwing (Node):

```go
func VerifyXxx(ctx context.Context, [files []string], [provided...], w io.Writer) error
```

```js
export async function verifyXxx({ files, signal, mode, base, head }, out) // out.write(text)
```

A Go verify function returning `(T, error)` provides `T` to verify functions of
the same package that take it. Test names must be static. Anything discovery
needs built first is declared as `prepare` in the ledger:

```yaml
prepare:
  - runners: [vitest-test]
    run: [pnpm, exec, nx, run-many, -t, prepare]
```

## The ledger

`ytif-verifications.yaml` holds the verification list and may include
fragments under its own directory:

```yaml
version: 3
vocabulary:                    # the project's own tag groups
  where:
    release: Release
    install: Install
includes:
  - teams/billing/verifications.yaml
verifications:
  - runner: go-verify
    unit: internal/release
    name: VerifyChangelog
    commit: true
    tags: [user, mistake, misdirection, release, silent, manual]
    requires:
      - { runner: go-test, unit: internal/release, name: TestVersionBump }
    accident: A release bumps the version without a changelog entry.
    detection: Reads the version and the changelog, and fails when the version has no entry.
    impact: The release ships notes that do not describe it.
    delete_when: Release notes are generated from commits.
  - runner: command
    name: formatting
    run: [pnpm, exec, prettier, --list-different, .]
    check: true
```

`check`, `commit`, and `skip-push` set the gates an entry belongs to; see
[Gates](#gates).

A discovered verification's contract has four fields: `accident`, an ordinary
unintended change it catches; `detection`, what it runs so that the accident
fails it; `impact`, what users see when that change ships; and `delete_when`,
an observable condition for removing it. `command` entries carry no contract.

Tags describe the accident. Every discovered verification carries exactly one
`visibility` and one `recovery` tag, and any number of the rest:

| Group | Tags |
|---|---|
| `what` (harm) | `code-execution`, `permission-bypass`, `exposure`, `data-loss`, `misdirection`, `outage` |
| `who` (from whom) | `user`, `agent`, `attacker`, `fault` |
| `why` (behavior prevented) | `habit`, `mistake`, `attack` |
| `visibility` | `silent`, `visible` |
| `recovery` | `irreversible`, `manual` |

`vocabulary` adds project groups. `requires` and `ensures` name verifications
this one depends on or keeps working.

## Gates

| Command | Runs | Files given to verifications |
|---|---|---|
| `ytif check` | `check: true` | tracked paths |
| `ytif commit` | `commit: true` | staged paths |
| `ytif push` | every verification unless `skip-push: true` | tracked paths |
| `ytif ci` | every verification | tracked paths |

Every gate runs on the current working tree; the last column only names the
paths verify functions receive.

Without `--base`, a gate runs every member. `--base REF` drops the members in
Nx projects unaffected since the merge base with `REF`; commands, units
outside every Nx project, and verifications that must run with a selected one
still run. Without a usable Nx result the gate runs every member and records
why. An affected run discovers only the runners it selects, so `ytif list` and
a full `ytif ci` are what find every new test.

`ytif check` is the gate agents may run. Before anything starts it rejects
test runners and `go-verify` marked `check: true`, and `command` and `prepare`
steps that it recognizes as builds, tests, ytif gates, or `ytif allow`. It does
not read what a `node-verify` function runs.

```yaml
# lefthook.yml
pre-commit:
  jobs:
    - run: go tool ytif commit --hook
pre-push:
  jobs:
    - run: go tool ytif push --hook
      use_stdin: true
```

```yaml
# GitHub Actions
steps:
  - uses: actions/checkout@v7
  - uses: actions/setup-go@v7
    with:
      go-version-file: go.mod
  - run: go tool ytif ci --report github --records ytif-records.jsonl
  - if: always()
    uses: actions/upload-artifact@v7
    with:
      name: ytif-records
      path: ytif-records.jsonl
```

Every lefthook and workflow command must call `ytif` or be listed in
`ytif-gate-exemptions.yaml` with its reason. A `run` entry matches a step's
whole text, and a `uses` entry the action's `owner/repo` without its ref. An
exemption matches that text, not what it runs: a script or package script that
changes behind an unchanged step keeps its exemption. Test runner steps cannot
be exempted, so tests stay on the rail.

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

## Records and board

`ytif check` and lefthook's `ytif commit --hook` / `ytif push --hook`
append results, timings, and selection to `<git-common-dir>/ytif/records.jsonl`;
`--records PATH` selects another destination. Remote CI workflows record with
`ytif ci --records PATH` and publish that file as an artifact.
`ytif list` shows verifications and findings.

`ytif board` writes the ledger and its records as one HTML file and opens it;
`--out PATH` only writes it, and `--records PATH` reads those files instead of
the default one.

On a repository whose origin is GitHub, the board also syncs the `ytif-records`
artifacts of that repository into `<git-common-dir>/ytif/ci-cache/`. It lists
the catalog at most every five minutes, downloads at most twenty unseen
artifacts per build (newest first), and keeps what it downloaded, so the next
build resumes where the last one stopped. The token comes from `GH_TOKEN`, then
`GITHUB_TOKEN`, then `gh auth token --hostname github.com`. Without
credentials, a network, or any artifact, the page renders the cached records
and names on the source line why the sync stopped.

A gate's time is the sum of its current members' means. <!-- dinyd: internal/board/board.go#summarize s1:824cf197241d735e -->
Each mean uses the last N timed results of that verification at that gate. <!-- dinyd: internal/board/board.go#aggregate s1:a73be260f7500632 --> <!-- dinyd: internal/board/board.go#meansOf s1:23e869a0e30312a9 --> <!-- dinyd: internal/board/board.go#build s1:44265c6be1cd490f -->
A fail counts as one hit; later fails of that verification at that gate count again only after a pass. <!-- dinyd: internal/record/hits.go#Hits s1:d5b7e4a02af8b181 -->
Hits are counted over the last `days` days, with an optional latest-attempt limit per gate. <!-- dinyd: internal/board/board.go#window s1:374dbfd9604443da -->
Only the root ledger sets the windows:

```yaml
board:
  average: {check: 100, commit: 100, push: 100, ci: 10}
  hits: {days: 365, last: {commit: 50}}
```

An omitted value keeps the default: 100 timed results per gate, 10 for ci, and 365 hit days. <!-- dinyd: internal/inventory/inventory.go#BoardConfig.Settings s1:93e76a905a006717 --> <!-- dinyd: internal/inventory/inventory.go#defaultAverage s1:47f8d6f4c819e8af --> <!-- dinyd: internal/inventory/inventory.go#defaultCIAverage s1:7175079900b572ce --> <!-- dinyd: internal/inventory/inventory.go#defaultHitDays s1:afc36fd3bd525bdc -->
`hits.last` counts a gate's hits only in its latest N attempts; leave it out to count every attempt inside the days.

## Agent guard

`ytif guard` is a PreToolUse hook that refuses agent commands that run checks
outside `ytif check`, registered by `ytif init` for the selected hosts:

| Host | Project registration |
|---|---|
| Claude Code | `.claude/settings.json`, `PreToolUse` |
| Codex | `.codex/hooks.json`, `PreToolUse` |
| OpenCode | `.opencode/plugins/ytif-guard.ts` |
| Kilo Code | `.kilo/plugin/ytif-guard.ts` |
| Cursor | `.cursor/hooks.json`, `beforeShellExecution` |
| Pi | `.pi/extensions/ytif-guard.ts` |

Consumer-project hooks and plugins run `go tool ytif`, so Go must be on the
host's `PATH`, including hosts launched from a GUI. This repository's guards
use the installed release described under [Adopt](#adopt).
When a guard cannot start, the OpenCode, Kilo Code, and Pi plugins allow the
command with a warning.

It refuses, directly or through a package manager:

- `ytif commit`, `ytif push`, `ytif ci`, and `ytif allow`;
- a test runner;
- a `*.verify.mjs` module run by `node`, `tsx`, or `bun`;
- a package script, Nx target, or Make goal named with the word `test`,
  `check`, or `verify`: `pnpm test:fast` and `make check`, but not
  `pnpm typecheck`;
- `prettier --check`, `tsc --noEmit`, `eslint` without `--fix`, `go vet`,
  `gofmt -l`, and `go mod tidy -diff`;
- a package script that runs any of these.

The guard reads command text and the package scripts it names; work started
from another file or program passes.

`ytif allow 8h` lets agents run checks directly for unattended work, and
`ytif allow --off` ends it. Only the user can grant it.

## License

[MIT](LICENSE)
