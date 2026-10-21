# YTiF — your time is free, or so your AI thinks.

> Built for my own projects; issues and pull requests are closed. Fork freely.

`ytif` keeps every verification a project owns — tests, custom checks, and
direct commands — on one rail. Each verification belongs to CI and, unless
skipped, to push; `check` and `commit` are independent opt-ins. Discovered
verifications carry a contract that says why they exist and when to delete
them, and leave a record of what they cost. Verifications without a contract,
contracts without a verification, gate steps that run tests around the rail,
and other gate steps that run code outside ytif without a stated reason fail
the gate.

## Install

Pin ytif per project, so hooks, CI, and every clone run the version the
project's files were written for. In a Go module (Go 1.24 or later):

```sh
go get -tool github.com/undervoke/ytif/cmd/ytif@v0.9.0
```

`go.mod` and `go.sum` then hold the version, and `go tool ytif` runs it;
read every `ytif` below as `go tool ytif`. Elsewhere, download a release
archive and verify it against `SHA256SUMS`, or run
`go run github.com/undervoke/ytif/cmd/ytif@v0.9.0`. The gates also need
the test runners the project uses.

For typing `ytif` in any project, also install it on PATH with
`go install github.com/undervoke/ytif/cmd/ytif@latest`. Inside a Go module
that pins ytif as a tool, it runs every command through `go tool ytif`
when the pinned version differs from its own, so the project's files are
always read by the version they were written for; elsewhere it runs
itself. `ytif update` replaces it with the latest release and leaves each
project's pin alone.

To adopt it, run `ytif init` from the installed ytif at the repository
root and select the agent hosts to configure. It pins that ytif's version
as a tool, creates the inventory and exemptions, wires `ytif commit` and
`ytif push` into lefthook and `ytif ci` into a workflow, and registers the
agent guard for the selected hosts.

```sh
ytif init --hosts claude-code,codex
ytif init --hosts opencode,kilo,cursor
ytif init --hosts none  # gates only
```

Without `--hosts`, init prompts in a terminal, with existing ytif
registrations preselected. Noninteractive runs specify `--hosts` explicitly.
Unselected hosts' files are left as they are; choosing `none` does not
uninstall earlier registrations. Rerun init with a host selected to refresh
its plugin or add its missing hook.

Init uses Prettier from the repository's `node_modules/.bin` to format only
files it creates or updates, following the project's configuration and
ignore rules. Generated plugins are formatted before comparison, so a rerun
preserves an identical project-formatted copy. Formatting runs through stdin
from the repository root before any writes and reports the tool and target
path; a formatter error stops init before the pin or integration files change.
When Prettier is absent, init uses its generated formatting. If Biome is also
installed, init reports the ambiguity and skips automatic formatting.

It preserves unrelated settings and checks every selected file before
writing; a file it cannot edit, such as JSON with comments or a lefthook
configuration that is not YAML, stops it before it changes anything.
It needs a root `go.mod`, does not lower a newer pin, and leaves each
host's trust and hook approval to the user.
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

## Verifications

Test and verify verifications are discovered, never registered by hand. Direct
commands are self-registered in the ledger. A key is `runner:unit:name`:

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

A Go verification uses only standard-library types:

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
invisible until staged. `ytif list` and full gates discover eligible runners
whatever the gate, and .NET discovery builds the test projects. A .NET
project whose discovered methods are all selected runs without a filter; a
filter too long for one command line is split across several runs.

Vitest and Playwright are listed by the project's own `node_modules/.bin`
binary from each config's directory, so their discovery needs the
dependencies installed and whatever the test files import built. A test
that several projects run is one verification, and a name repeated within one file
and project fails discovery. A Vitest config's listing is cached in the git
common directory by the tracked paths, the files under the config's
directory, the lockfiles, and the listed test files; a test name computed
from any other file refreshes only when one of those changes.

What discovery and runs need generated or built first, the ledger
declares as preparation. Every `ytif list` and gate runs each command in
order from the repository root before discovery, and a failed command fails
discovery of its runners:

```yaml
prepare:
  - runners: [vitest-test]
    run: [pnpm, exec, nx, run-many, -t, prepare]
```

## The check gate

`ytif check` runs the ledger verifications with `check: true`, through the same
selection, record, and board model as the other gates. Agents may run this
command; it never invokes another gate through the check lane. `--jobs N`
bounds concurrently running entries, not workers inside their tools; it
defaults to 2, while the other gates default to the CPU count. With
`--jobs 1` entries run sequentially in ledger order. There is no subset
selection. Independent entries run even after another fails. Output is
prefixed by entry name, followed by a result and elapsed time for each.
Ctrl-C cancels running process groups and stops pending entries.

```yaml
version: 3
verifications:
  - runner: command
    name: formatting
    run: [pnpm, exec, prettier, --list-different, .]
    check: true
  - runner: command
    name: maps
    run: [node, tools/maps/env.mjs, --check]
    exclusive: [maps]
    check: true
```

Command entries keep their `name`, `run`, `cwd`, and `exclusive` fields.
`cwd` optionally changes the directory from the repository root. `exclusive`
names resources that never overlap across gate runs at any job count. Tokens
express mutual exclusion, not dependencies, ordering, or duplicate-work
detection.

Before starting any entry, ytif reads the entire check selection and rejects
test-runner kinds and `go-verify` by kind: tests belong at the gates, and
`go-verify` needs its dispatcher `go build`. Direct commands and applicable
`prepare` argv go through the existing bounded static inspector, which rejects
known test runners, explicit build commands, and package/Make/Nx tasks with a
`test` or `build` word in their names. It also rejects calls to ytif gates, to
`check` itself (recursion), and to `allow` (the user's exception mechanism).
It follows literal shell wrappers and package scripts with their arguments and
lifecycle hooks, using the same bounded traversal as the agent guard. For Nx it
reads `project.json`, `package.json` Nx targets/scripts, and `nx.json` target
settings, following explicit target dependencies and declared package or
implicit project edges. `nx affected` is inspected against every non-excluded
static project, since future diffs may select any of them. Build/test-named
dependencies are rejected by name even when no current dependency matches;
register the direct command when that task name obscures a pure check.
It does not execute Nx or its plugins to classify commands. Inferred targets,
workspace packages without `project.json` or `package.json` Nx metadata,
import-inferred edges, computed options, changed/unknown directories, and
dynamic calls inside arbitrary programs remain the project's responsibility.
TypeScript emission checks read strict-JSON configs and relative `extends`;
JSONC and package-resolved configs remain unresolved. Unknown option arity
leaves file operands unresolved, but a known emitting config is still rejected.
Unknown branches pass; admission is not a guarantee that a command contains no
hidden build or test. `node-verify` runs through `node` with no compile step,
so it is admissible at check, but ytif reads only its exports: a `verifyXxx`
body can still spawn build or test work that static admission cannot see, and
keeping such work out of check stays the host's responsibility. Reclassification
happens on every invocation, so editing a registered script changes the next
decision. Invalid check placement fails before anything runs and is also
surfaced by `list` and CI reconciliation, so an affected omission cannot hide it.

Completed check runs record timing and outcomes under the `check` gate:
`ytif stats --gate check`. `--records PATH` uses another record file.
Interrupted or unstarted entries have no completed result. A pass does
not replace any other gate. Like the other gates, check also fails on shared
ledger or gate-configuration findings, including findings on entries it does
not execute. Exit codes are `0` when execution and reconciliation pass,
`1` for execution or reconciliation failures or interruption, and `2` for
invalid registration, rejected entries, or a recording failure.

## The ledger

`ytif-verifications.yaml` (version 3) holds the verification list. The root file
carries the shared `version`, `vocabulary`, and `prepare` configuration; every
file, root or included, may carry `verifications` and `includes`. Includes
resolve relative to the file that declares them and must stay inside that
file's directory subtree: no absolute paths, no parent escapes, and no symlink
escapes. Missing, non-file, malformed, cyclic, or duplicate-key references fail
before anything runs, with file and entry provenance. Splitting never creates
gate, scheduler, Nx, or identity boundaries; merging keeps ledger order.

```yaml
version: 3
vocabulary:                    # the project's own tag groups
  where:
    release: Release
    install: { en: Install, ko: 설치 }
includes:
  - teams/billing/verifications.yaml
verifications:
  - runner: go-verify
    unit: internal/release
    name: VerifyChangelog
    commit: true               # check/commit opt-ins; push+CI are the default
    tags: [user, mistake, misdirection, release, silent, manual]
    requires:                  # optional: verifications this one depends on
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

Every verification belongs to CI and, unless `skip-push: true`, to push.
`check: true` and `commit: true` independently add those gates; all three
booleans default to `false` and `skip-push` carries no reason. The host owns
`skip-push` choices; ytif never assigns them. `unit` and relative `cwd` stay
repository-root relative.

`accident` is an ordinary, unintended change the verification catches,
`detection` what the verification runs and observes so that the accident fails
it, `impact` what users see when that change ships, and `delete_when` an
observable condition for removing the verification; all four are required for
discovered entries. Self-registered `command` entries carry no contract and
are exempt from contract reconciliation. Choose `check`/`commit` for work worth
its cost there, as `ytif stats` shows.

Tags describe the accident. ytif ships these groups; every verification carries
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
`requires` and `ensures` name other inventoried verifications this one depends on
or keeps working. An unknown tag, a missing `visibility` or `recovery` tag,
and a relation to a verification outside the ledger are findings.

`ytif migrate` converts `ytif-inventory.yaml` (version 2) to
`ytif-verifications.yaml` (version 3) with the new binary, without delegating
to an older pinned ytif. It maps `checks` to `verifications`,
`placement: commit` to `commit: true`, `push`/`ci` placements to the new push+CI
default, and `commands` to `runner: command` entries with `check: true`, keeping
`name`, `run`, `cwd`, and `exclusive`. It never assigns `skip-push`; the notice
names the resulting push and CI expansion so the host can add explicit
exclusions. Keys are preserved, collisions fail, and reruns are stable.
Exemptions stay at version 2 with unchanged meaning.

## Gates

| Command | Runs | Files given to verifications |
|---|---|---|
| `ytif check` | `check: true` | tracked paths, working-tree content |
| `ytif commit` | `commit: true` | staged paths listed, working-tree content read |
| `ytif push` | every verification unless `skip-push: true` | tracked tree at the checkout |
| `ytif ci` | every verification | tracked tree at the checkout |

Membership selects the verifications; the files are input for verify
verifications, not a filter on tests. `push` and CI at equivalent revisions use
the same tracked-tree execution scope, so file-consuming verifications cost
more at push than a pushed-files scope would. Supported verifications also
receive the mode, base, and head context, plus `YTIF_MODE`, `YTIF_BASE`, and
`YTIF_HEAD` in the environment, so self-narrowing wrappers can use them.
This context describes ytif's selection; it does not prove which work a
wrapper or its child commands actually executed.

`--base REF` opts any gate into affected mode; without it the gate runs full.
The host chooses when to run full, including on the final CI. Nx owns the
dependency and affected analysis and ytif applies it to every verification
kind through the existing path-to-deepest-project mapping. A verification with
unknown scope and an unmapped verification still run; unavailable affected
support falls back to the eligible set with a brief notice, an invalid
`--base` is an input error, and a valid empty
affected set stays empty. There is no mandatory per-verification Nx binding and
no global rerun merely because the ledger changed.

Local `check` and `commit` judge the working tree: the merge base of `--base`
with HEAD through the current tree, including staged, unstaged, deleted, old
rename paths, and untracked files. A typical local call is
`ytif check --base HEAD`. `push` and PR CI judge the same host-supplied PR
comparison: the merge base of `--base` with each pushed tip or with the
checkout HEAD. The host supplies the same PR base to `push` and CI and never
guesses it from the push destination. Multi-ref pushes union their change sets;
CI records the checkout HEAD and, for a synthetic merge, its second parent.
When the working tree differs from a pushed tip, the run records a revision
mismatch and prints one notice; it does not fail by default and ytif never
requires a clean tree.

The runners of a gate run at once, and `--jobs N` (default: 2 for `check`,
the CPU count otherwise) bounds how many processes ytif runs together while
discovering and running verifications: test processes, builds, listings, and
dispatchers. Affected selection happens before preparation and discovery;
only runners with selected verifications are prepared and discovered.
Affected runs therefore do not certify a complete census of new tests:
`list` and full CI reconcile every runner, including a CI run that falls back
to full. The host chooses when those full reconciliations run.
Preparation runs sequentially before discovery; `go env`, `go mod edit`, and
git run outside the process bound. Parallelism
inside a process, such as a test framework's workers, stays the project's
configuration. Verifications that may run together must not share ports, build
outputs, or temporary paths. Output a verification passes through is printed a
line at a time, prefixed with `[runner]`; results print in runner order once
every runner has finished. One CI job running `ytif ci` does the work of a job
per runner without repeating the checkout and setup.

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

`ytif list` shows verifications and findings; `ytif stats` shows recorded time and
hits, where a hit is a run of consecutive fails that a pass ends.
`ytif ci --report github` emits annotations.

## Board

`ytif board` writes the ledger and its records as one static HTML file,
`<git-common-dir>/ytif/board.html`, and opens it; `--out PATH` only writes
it, and `--records PATH` reads other record files, such as a CI artifact.
Its pages list and filter verifications by tag and by all four gate memberships,
draw a verification's relations and nearest verifications, show each
verification's cost and hits from history rather than present-day membership,
show run selection context with requested and effective mode, fallback, revision
mismatch, and omissions, and lay out the vocabulary, in English or Korean.
An omission is neither a pass nor a zero-duration run; an empty ledger reads
differently from an affected selection that chose nothing, and records before
selection tracking show mode unknown. The board fetches no remote artifacts and
compares no retrospective full and affected runs. Exit codes: `0` pass,
`1` fail, `2` usage or configuration error.

To work on the page itself, run from this repository's root:

```sh
go run ./internal/board/devserve <repo>
```

It serves the board of `<repo>`, a repository with an inventory and records,
at `http://127.0.0.1:8765`, rebuilding the page from
`internal/board/assets` on each request, so an edit shows on reload.

## Agent guard

Agents run `check: true` verifications through `ytif check`; other verifications
are left to the Git hooks and CI that start the gates.
`ytif guard` is a PreToolUse hook that refuses agent commands that run
checks. It refuses nothing until an agent's configuration runs it, which
`ytif init` writes for the hosts you select:

| Host | Project registration |
|---|---|
| Claude Code | `.claude/settings.json`, `PreToolUse` |
| Codex | `.codex/hooks.json`, `PreToolUse` |
| OpenCode | `.opencode/plugins/ytif-guard.ts`, v1 `server` and v2 `setup` |
| Kilo Code | `.kilo/plugin/ytif-guard.ts`, current OpenCode-based Kilo |
| Cursor | `.cursor/hooks.json`, `beforeShellExecution` |
| Pi | `.pi/extensions/ytif-guard.ts`, `tool_call` |

Orca launches other agents: select the hosts it launches, rather than an
Orca plugin. Commit the project configuration so new worktrees receive it.
The Kilo plugin targets the current OpenCode-based CLI and extension.
The OpenCode plugin uses the default-object entrypoint supported by v1
from 1.18.29 and targets the v2 2.0.32 `setup` API.

For Claude Code and Codex, the hook is:

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": "go tool ytif guard" }] }
    ]
  }
}
```

For Cursor, init merges this native shell hook:

```json
{
  "version": 1,
  "hooks": {
    "beforeShellExecution": [
      { "command": "go tool ytif guard --host cursor" }
    ]
  }
}
```

`--host cursor` accepts Cursor's `command` and `cwd` payload and returns
its `permission` and `user_message` response. The refusal rules and records
are shared with the other hosts.

Init installs the embedded TypeScript plugins with `YTIF` set to
`["go", "tool", "ytif"]`. Open a new agent session after installation,
and ensure Go is on that host's PATH, including GUI hosts. Codex hooks
need approval in `/hooks`, Pi extensions need a trusted project, and
Cursor project hooks need a trusted workspace. OpenCode and Kilo resolve
the session directory separately from a shell tool's working-directory
override, so guard evaluates the command where it will run.

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
