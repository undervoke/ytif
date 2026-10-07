# YTiF — your time is free, or so your AI thinks.

> Built for my own projects; issues and pull requests are closed. Fork freely.

`ytif` keeps every check a project owns — tests and custom checks — on one
rail. Each check carries a contract that says why it exists and when to
delete it, runs at the gate its cost earns, and leaves a record of what it
cost. Checks without a contract, contracts without a check, and gate steps
that run project code around the rail fail the gate.

## Install

Pin ytif per project, so hooks, CI, and every clone run the version the
project's files were written for. In a Go module (Go 1.24 or later):

```sh
go get -tool github.com/undervoke/ytif/cmd/ytif@v0.1.0
```

`go.mod` and `go.sum` then hold the version, and `go tool ytif` runs it;
read every `ytif` below as `go tool ytif`. Elsewhere, download a release
archive and verify it against `SHA256SUMS`, or run
`go run github.com/undervoke/ytif/cmd/ytif@v0.1.0`. The gates also need
the test runners the project uses.

To adopt it, stage any new check files and run `ytif list`. Every
discovered check without a contract and every gate step that bypasses the
rail is a finding, and the gates fail until none remain. Write the
contracts and routing entries, wire the gates and the guard as below, and
rerun `ytif list`.

## Checks

Checks are discovered, never registered by hand. A key is `runner:unit:name`:

| Runner | Discovered from | Unit | Name |
|---|---|---|---|
| `go-verify` | `VerifyXxx` functions in `//go:build verification` files | package directory | function |
| `go-test` | `TestXxx`, `FuzzXxx`, examples with output | package directory | function |
| `bun-test`, `node-test` | `*.test.*` files importing `bun:test` or `node:test` | file | `suite > test` |
| `dotnet-test` | `.csproj` referencing `Microsoft.NET.Test.Sdk` | `.csproj` path | method |

A Go check uses only standard-library types:

```go
func VerifyXxx(ctx context.Context, [files []string], [provided...], w io.Writer) error
```

It writes findings as `path:line: message` and fails by returning an error;
what a passing check writes is shown as its log, and `--report github` turns
its findings into warnings. A check returning `(T, error)`
provides `T` to checks of the same package that take it. Test names must be
static; what ytif cannot list exactly fails discovery.

Discovery reads Git-tracked files only, so an untracked new test stays
invisible until staged. `ytif list` and every gate discover all runners
whatever the placement, and .NET discovery builds the test projects.

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
    impact: The release ships notes that do not describe it.
    delete_when: Release notes are generated from commits.
```

`accident` is an ordinary, unintended change the check catches, `impact`
what users see when that change ships, and `delete_when` an observable
condition for removing the check; all three are required. You choose the
placement: an earlier gate for a check worth its cost there, as
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

Placement alone selects the checks; the files are input for Go checks, not
a filter on tests.

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
  - run: go tool ytif ci --report github --records ytif-records.jsonl
  - if: always()
    uses: actions/upload-artifact@v7
    with:
      name: ytif-records
      path: ytif-records.jsonl
      if-no-files-found: ignore
```

Its actions need routing entries. Annotations do not keep records; read a
downloaded artifact with `ytif stats --records PATH`.

Every command in lefthook and GitHub workflows must call `ytif` (directly,
through `go tool ytif`, or through `go run` of its package) or be external
code listed in `ytif-routing.yaml`. A `run` entry matches
a step's whole text, and a `uses` entry the action's `owner/repo` without
its ref. Project code cannot be routed; it runs through `ytif`.

```yaml
version: 2
entries:
  - surface: github-actions
    uses: actions/checkout
    kind: infrastructure
  - surface: lefthook
    run: gofmt -l {staged_files}
    kind: external-tool
```

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

## Agent guard

Agents leave checks to the Git hooks and CI that start the gates.
`ytif guard` is a PreToolUse hook that refuses agent commands running test
runners or ytif gates. For Claude Code (`.claude/settings.json`) and Codex
(`.codex/hooks.json`):

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": "go tool ytif guard" }] }
    ]
  }
}
```

For OpenCode, copy `integrations/opencode/ytif-guard.ts` into
`.opencode/plugins/`; for Pi, `integrations/pi/ytif-guard.ts` into
`.pi/extensions/`, and set its `YTIF` command to how the project runs ytif.

Only the user grants an exception: `ytif allow 8h` lets agents run them
directly for unattended work, and `ytif allow --off` ends it. A test runner
run directly leaves no record.

## License

[MIT](LICENSE)
