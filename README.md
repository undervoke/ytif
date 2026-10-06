# YTiF — your time is free, or so your AI thinks.

> Built for my own projects; issues and pull requests are closed. Fork freely.

`ytif` keeps every check a project owns — tests and custom checks — on one
rail. Each check carries a contract that says why it exists and when to
delete it, runs at the gate its cost earns, and leaves a record of what it
cost. Checks without a contract, contracts without a check, and gate steps
that run project code around the rail fail the gate.

## Install

Download a release archive and verify it against `SHA256SUMS`, or, with
Go 1.25 or later:

```sh
go install github.com/undervoke/ytif/cmd/ytif@latest
```

Git hooks and CI need `ytif` on `PATH`, along with the test runners the
project uses.

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

`verification-inventory.yaml` holds one contract per discovered check:

```yaml
version: 1
checks:
  - runner: go-verify
    unit: internal/release
    name: VerifyChangelog
    placement: commit          # commit, push, or ci
    accident: A release bumps the version without a changelog entry.
    outcome: The release ships notes that do not describe it.
    delete_when: Release notes are generated from commits.
```

`accident` is an ordinary, unintended change the check catches, `outcome`
the wrong behavior that change would otherwise ship, and `delete_when` an
observable condition for removing the check; all three are required. You
choose the placement: an earlier gate for a check worth its cost there,
as `ytif stats` shows.

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
    - run: ytif commit
pre-push:
  jobs:
    - run: ytif push
      use_stdin: true
```

Run `lefthook install` to activate the hooks. In GitHub Actions:

```yaml
steps:
  - uses: actions/checkout@v7
  - uses: actions/setup-go@v7
    with:
      go-version: stable
  - run: go install github.com/undervoke/ytif/cmd/ytif@latest
  - run: ytif ci --report github --records ytif-records.jsonl
  - if: always()
    uses: actions/upload-artifact@v7
    with:
      name: ytif-records
      path: ytif-records.jsonl
      if-no-files-found: ignore
```

Its actions and the `go install` step need routing entries. Annotations do
not keep records; read a downloaded artifact with `ytif stats --records PATH`.

Every command in lefthook and GitHub workflows must call `ytif` or be
external code listed in `verification-routing.yaml`. A `run` entry matches
a step's whole text, and a `uses` entry the action's `owner/repo` without
its ref. Project code cannot be routed; it runs through `ytif`.

```yaml
version: 1
entries:
  - surface: github-actions
    uses: actions/checkout
    kind: infrastructure
  - surface: lefthook
    run: gofmt -l {staged_files}
    kind: external-tool
```

`ytif list` shows checks and findings; `ytif stats` shows recorded time and
failures. `ytif ci --report github` emits annotations. Exit codes: `0` pass,
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
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": "ytif guard" }] }
    ]
  }
}
```

For OpenCode, copy `integrations/opencode/ytif-guard.ts` into
`.opencode/plugins/`; for Pi, `integrations/pi/ytif-guard.ts` into
`.pi/extensions/`.

Only the user grants an exception: `ytif allow 8h` lets agents run them
directly for unattended work, and `ytif allow --off` ends it. A test runner
run directly leaves no record.

## License

[MIT](LICENSE)
