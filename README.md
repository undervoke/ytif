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
(Go 1.25 or later):

```sh
go get -tool github.com/undervoke/ytif/cmd/ytif@v0.17.0
```

Read every `ytif` below as `go tool ytif`. Outside a Go module, download a
release archive and verify it against `SHA256SUMS`.

Existing projects add `--hook` to their lefthook commit and push commands to
keep recording.

To upgrade a project whose ledger is a version 2 `ytif-inventory.yaml`, raise
the pin and run `ytif migrate`. It lists the verifications that now also run
at push; add `skip-push: true` where push must not run them.

When upgrading from v0.15.0, run `ytif init` after raising the pin, using the
same host selection as before. It removes the retired `check` settings from
the root ledger and its includes and adds the default two-minute guard limit.
Existing `check` settings must be migrated before the new gates can load them.

## Adopt

```sh
ytif init --hosts claude-code,codex
```

Init needs a root `go.mod`. It pins ytif as a tool, creates the ledger and the
exemptions, wires `ytif commit` and `ytif push` into lefthook and `ytif ci`
into a GitHub workflow, registers the agent guard for the selected hosts, and
stores `--jobs N`, or the count it prompts for, as this machine's gate process
limit. <!-- dinyd: internal/initcmd/initcmd.go#Run s1:0310ed3bed4b7f49 -->
In ytif's own repository, init registers the checkout's command for gates. <!-- dinyd: internal/initcmd/initcmd.go#pinStep s1:cc8001f009c097c4 -->
Agent guards in this repository use the installed ytif release on PATH, keeping their executable independent of source edits. <!-- dinyd: internal/initcmd/initcmd.go#plan s1:8fbb7f788c518487 -->
Run `lefthook install`, then follow the approval steps init prints for each
host.

Then stage new verification files and run `ytif list`. Every discovered
verification without a contract and every gate step that bypasses the rail is
a finding; the gates fail until none remain.

## Verifications

Tests and verify functions are discovered from Git-tracked files, marked Nx
targets from the project graph; direct commands are registered in the ledger.
A key is `runner:unit:name`:

| Runner | Discovered from | Unit | Name |
|---|---|---|---|
| `go-verify` | `VerifyXxx` functions in `//go:build verification` files | package directory | function |
| `node-verify` | exported `verifyXxx` functions in `*.verify.mjs` files | file | function |
| `go-test` | `TestXxx`, `FuzzXxx`, examples with output | package directory | function |
| `bun-test`, `node-test` | `*.test.*` files importing `bun:test` or `node:test` | file | `suite > test` |
| `dotnet-test` | `.csproj` referencing `Microsoft.NET.Test.Sdk` | `.csproj` path | method |
| `vitest-test` | `vitest list` of each `vitest.config.*`, or a `vite.config.*` that mentions vitest | test file | `suite > test` |
| `playwright-test` | `playwright test --list` of each `playwright.config.*` | spec file | `describe > test` |
| `nx-target` | Nx targets marked `metadata.ytif` in the resolved project graph | project root | target |
| `command` | ledger `run` argv | `cwd` or `.` | entry `name` |

Verify functions write findings as `path:line: message` and fail by returning
an error (Go) or throwing (Node):

```go
func VerifyXxx(ctx context.Context, [files []string], [provided...], w io.Writer) error
```

```js
export async function verifyXxx({ files, signal, gate, profile, selection, report }, out) // out.write(text)
```

`selection` carries the plan's requested and resolved scope with its decision, comparison, and changed paths; `report({scope, targets})` optionally records the scope actually run, and an unreported scope stays unknown.
Comparisons identify their `kind`: `commits` has a base and head, while `worktree` and `index` have a baseline and offer the changed paths separately.

A Go verify function returning `(T, error)` provides `T` to verify functions of
the same package that take it. Test names must be static. Anything discovery
needs built first is declared as `prepare` in the ledger:

```yaml
prepare:
  - runners: [vitest-test]
    run: [pnpm, exec, nx, run-many, -t, prepare]
```

A project's judgment that is an Nx target rather than a test suite joins through
`nx-target`. Set `nx-targets: true` at the root of the ledger, mark the target,
and declare one entry per project and target with the usual contract:

```json
"logging:scan": { "metadata": { "ytif": true }, "command": "semgrep scan {projectRoot}" }
```

```yaml
nx-targets: true
verifications:
  - runner: nx-target
    unit: apps/api
    name: logging:scan
    tags: [...]
    accident: ...
    detection: ...
    impact: ...
    delete_when: ...
```

The marker declares a verification; membership and the contract stay in the
ledger. Plugin-inferred targets can carry it too. The setting is read from the
root file only. With it, an entry without a marked target is stale, a marked
target without an entry is unregistered, and a project graph that cannot be read
is a discovery failure. Without it, discovery never reads the project graph for
this runner, and an `nx-target` entry is an unknown runner.

Affected scope trusts the entry's project, so a target should read what that
project owns. ytif runs the exact project and target with `nx run` from the
repository root and records one verdict for it; the dependencies and cache
behind that invocation are Nx's. A dependency that is itself a ledger entry runs
regardless of that entry's gate membership and gets no verdict of its own.
Selected targets run one at a time, while other runners run alongside them.
The agent guard uses a target's own recorded verdict timing first; without it,
only an uncached target's statically resolved single-command body can be priced.

## The ledger

`ytif-verifications.yaml` holds the verification list and may include
fragments under its own directory:

```yaml
version: 3
gate:
  commit: {scope: file}
  push: {scope: full}          # or {ci-profile: NAME} to preview that profile's CI selection
  ci:
    scope: full
    profiles:
      integration:
        scope: affected
        target: {remote: origin, branch: staging}
guard:
  limit: 2m                   # agent commands estimated above this are refused
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
```

`commit` and `skip-push` set the gates an entry belongs to; see
[Gates](#gates).

A discovered verification's contract has four fields: `accident`, an ordinary
unintended change it catches; `detection`, what it runs so that the accident
fails it; `impact`, what users see when that change ships; and `delete_when`,
an observable condition for removing it. `command` entries carry no contract.

YAML merge keys can share a contract while each unit remains explicitly registered:

```yaml
verifications:
  - <<: &logging-scan
      runner: nx-target
      name: logging:scan
      tags: [user, mistake, misdirection, silent, manual]
      accident: A new logging call bypasses the project's structured logger.
      detection: Scans for logging calls outside the approved logger.
      impact: Diagnostic events lose the context needed to investigate failures.
      delete_when: The project no longer requires structured logging.
    unit: apps/api
    commit: true
    scope: {ci: affected}
  - <<: *logging-scan
    unit: apps/api.seed
```

Anchors are local to one YAML file. Setting `tags` or `scope` on a member replaces
that whole inherited value; it does not extend it. Keep per-member `unit`,
`commit`, and `scope` outside the shared mapping. `ytif init` leaves these
merge-keyed verification entries intact when they have no retired `check` fields
to remove.

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

| Command | Runs | Scope written by init |
|---|---|---|
| `ytif commit` | `commit: true` | `file` |
| `ytif push` | every verification unless `skip-push: true` | `full`, or one CI profile when mapped |
| `ytif ci [--profile NAME]` | every verification | `full` |

Every gate runs on the current working tree. The ledger's `gate:` section is required and sets how much of each member runs: `file` requests changed-file targeting, `affected` skips it when Nx places it in an unaffected project, and `full` runs its registered invocation unchanged. An `nx-target` entry is instead skipped when Nx reports its own target's task unaffected: from task inputs when Nx selects affected tasks (Nx 24 unless `affected.legacy` is set), otherwise from its project. An entry whose target the graph no longer has runs full. A ledger without `gate:` fails to load; `ytif init` writes the values above and fills missing gate settings while preserving existing ledger settings. `ytif init` also writes a missing `guard: {limit: 2m}` and fills a missing guard limit the same way. <!-- dinyd: internal/initcmd/initcmd.go#fillGate s1:d998944357315ed4 --> <!-- dinyd: internal/initcmd/initcmd.go#ledgerText s1:9afbf1fd266bb5e7 -->

A gate runs at most `--jobs N` processes at once, else `git config ytif.jobs`,
else 4. The limit counts processes, not the workers a tool starts inside one.
Git config keeps it per machine and out of the repository: `ytif init` writes
the repository's config, and `git config --global ytif.jobs N` covers every
repository. CI checkouts have no such key and run 4 unless the workflow passes
`--jobs`.

An entry overrides the policy per gate or per CI profile:

```yaml
verifications:
  - runner: command
    name: eslint
    run: [pnpm, exec, eslint, ., --max-warnings, "0"]
    commit: true
    scope: {ci: full}
```

The most specific scope wins: for `ci` and a mapped push, entry `ci/<profile>`, then entry `ci`, then the profile, then gate `ci`; for commit and an unmapped push, the entry scope, then the gate scope. Membership still decides which verifications a gate runs; scope decides only how much of each one runs. <!-- dinyd: internal/inventory/gate.go#GateContext.Resolve s1:0476e67848eb9527 -->

`--base`, `--head`, and `--target-branch` only choose the comparison; scope comes from the ledger. Commit compares HEAD to staged paths, push compares each pushed tip to its merge base with the mapped profile's target, and pull-request CI compares the merge base of the event's base and head to the PR head while recording the execution HEAD separately. A push needing comparison fetches its target once into a ytif-owned ref; when pushed tips differ from the working tree, narrowed verifications run full because the result describes the working tree. Without a usable comparison — no target, no history, or a failed fetch — narrowed plans run full and record why, as does any narrowing that cannot be established. CI outside a PR uses explicit comparison inputs or the profile's locally available target ref; a post-merge run whose target equals HEAD compares nothing, so keep that profile full or pass an explicit comparison that names the intended range.

Native file targeting supports ESLint 10, direct or via `pnpm exec eslint`, with literal file/directory operands and simple JavaScript configs. Local dependencies must be static relative imports or literal `require` of regular, exact-case `.js`, `.mjs`, or `.cjs` files inside the repository. Node builtins and packages whose real paths remain under `node_modules` are treated as installed dependencies, including their exported subpaths. Workspace or unresolved package imports, directory imports, TS configs, URL references, symlinked or differently cased config/helper/domain paths, and other unmodeled forms run the original full invocation with a reason. <!-- dinyd: internal/targeting/targeting.go#plan s1:71b5d6aeaf8cd540 -->
Config, ignore, manifest, and lockfile changes anywhere in the repository widen to full, as do changes to tracked relative helpers; this deliberately trades some extra runs for avoiding Node module-resolution rules. Otherwise, changed existing files inside the operands are passed after `--` with `--no-warn-ignored`, and a valid empty set skips. Explicit file operands need an existing `--no-warn-ignored` to preserve their original warning behavior. File scope provides selected-file feedback, not proof of rule locality: arbitrary cross-file dependencies and runtime file reads remain outside the static model.

Child processes receive `YTIF_GATE`, `YTIF_PROFILE`, and `YTIF_COMPARE_KIND`/`YTIF_COMPARE_BASE`/`YTIF_COMPARE_HEAD`. Commands and Node dispatchers also receive `YTIF_SCOPE`: a narrowed commit-range comparison projects the target tip as `NX_BASE`; full runs and local worktree/index comparisons clear it, and both remove `NX_HEAD`. Go verifier processes apply that projection only for a homogeneous batch; mixed batches keep the host Nx environment and omit the scalar scope. `BASE_SHA` remains host-owned. <!-- dinyd: internal/check/plan.go#Input.ProcessEnvironment s1:d044300cad3ecdc2 -->
Node checks run in key order, one dispatcher per maximal contiguous process context; module state is shared within each dispatcher. A wrapper chooses its own targets, so projected environment values alone establish no actual execution scope.

A narrowed run prepares and discovers only the runners its selection needs, so `ytif list` and an effective-full `ytif ci` are what find every new test.

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
    with:
      fetch-depth: 0
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

`ytif init` writes that workflow only when none runs `ytif ci`; an existing workflow is left unchanged. Narrowing compares against history, so keep `fetch-depth: 0`.

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

Lefthook's `ytif commit --hook` / `ytif push --hook`
append results, timings, and selection to `<git-common-dir>/ytif/records/`; <!-- dinyd: internal/record/record.go#DefaultPath s1:ca79b9c2bc10b015 -->
`--records PATH` selects another destination. Remote CI workflows record with
`ytif ci --records PATH` and publish that file as an artifact.
`ytif list` shows verifications and findings.

`ytif board` writes the ledger and its records as one HTML file and opens it;
`--out PATH` only writes it, and `--records PATH` reads those files instead of
the default ones.

Recording and `ytif board` delete the oldest closed record files (rotated at about 8 MiB) and dated CI artifacts once they pass 400 days or 1 GiB, counted separately for local and CI records; the open file and undated artifacts stay, so storage can exceed these limits. <!-- dinyd: internal/storage/storage.go#SegmentBytes s1:1d70f44b784722de --> <!-- dinyd: internal/storage/storage.go#Defaults s1:757196d8b34afc71 --> <!-- dinyd: internal/cirecords/retention.go#syncer.runGC s1:76c793dbc299453a -->
`<git-common-dir>/ytif/storage.json` overrides the limits for every worktree; an invalid file stops deletion until it is fixed: <!-- dinyd: internal/storage/storage.go#Load s1:0ba85313cec0c9d2 -->

```json
{"days": 400, "local_max_bytes": 1073741824, "ci_max_bytes": 1073741824}
```

Deleted history stays gone when a statistics window is raised; the board's source line shows how far back history reaches.
`<git-common-dir>/ytif/records.jsonl` from earlier releases still feeds statistics but is excluded from the limits and never deleted; remove it by hand once no older ytif writes it and its history is no longer needed. <!-- dinyd: internal/record/managed.go#ReadManaged s1:9a5b80a1f826ad01 -->
Files named with `--records` are never deleted.

On a repository whose origin is GitHub, the board also syncs the `ytif-records`
artifacts of that repository into `<git-common-dir>/ytif/ci-cache/`. It lists
the catalog at most every five minutes, downloads at most twenty unseen
artifacts per build (newest first), and keeps them within those limits, so the next
build resumes where the last one stopped. The token comes from `GH_TOKEN`, then
`GITHUB_TOKEN`, then `gh auth token --hostname github.com`. Without
credentials, a network, or any artifact, the page renders the cached records
and names on the source line why the sync stopped.

A gate's time is the sum of its current members' means. <!-- dinyd: internal/board/board.go#summarize s1:824cf197241d735e -->
Each mean weights the last N timed results of that verification at that gate toward the newest: a result's weight halves every `half_life_percent` percent of N, so one value gives every gate the same shape. <!-- dinyd: internal/board/board.go#aggregate s1:3325f4ee969cfe77 --> <!-- dinyd: internal/board/board.go#meansOf s1:34b753e0c61efa15 --> <!-- dinyd: internal/board/board.go#build s1:4fee2e9f071b433f -->
A fail counts as one hit; later fails of that verification at that gate count again only after a pass. <!-- dinyd: internal/record/hits.go#Hits s1:d5b7e4a02af8b181 -->
Hits are counted over the last `days` days, with an optional latest-attempt limit per gate. <!-- dinyd: internal/board/board.go#window s1:374dbfd9604443da -->
Only the root ledger sets the windows:

```yaml
board:
  average: {commit: 100, push: 100, ci: 10}
  half_life_percent: 50
  hits: {days: 365, last: {commit: 50}}
  recent_fail_days: 30
  scales:
    verification: [0, 100, 1000, 10000, 100000, 1000000]
    unit: [0, 10000, 100000, 1000000, 10000000]
```

An omitted value keeps the default: 100 timed results per gate, 10 for ci, a half-life of 50 percent, 365 hit days, and 30 fail days. <!-- dinyd: internal/inventory/inventory.go#BoardConfig.Settings s1:14a4dccbff859b0f --> <!-- dinyd: internal/inventory/inventory.go#defaultAverage s1:47f8d6f4c819e8af --> <!-- dinyd: internal/inventory/inventory.go#defaultCIAverage s1:7175079900b572ce --> <!-- dinyd: internal/inventory/inventory.go#defaultHalfLife s1:9b7933633575070d --> <!-- dinyd: internal/inventory/inventory.go#defaultHitDays s1:afc36fd3bd525bdc --> <!-- dinyd: internal/inventory/inventory.go#defaultFailDays s1:7b525bc396a5ec4f -->
`hits.last` counts a gate's hits only in its latest N attempts; leave it out to count every attempt inside the days.
`recent_fail_days` is how far back a fail marks a verification as recently failed. <!-- dinyd: internal/board/board.go#flags s1:0e0a093f81108c3b -->
`scales` lists the time bars' ticks in milliseconds, each interval taking an equal width: `verification` draws single verifications, `unit` draws unit, file, and process totals, and the verification page's execution-unit rows extend `verification` tenfold until their largest total fits. <!-- dinyd: internal/inventory/inventory.go#Scales s1:8e1feca08d139295 -->
A ledger `ytif init` creates lists these keys and their defaults as a comment. <!-- dinyd: internal/initcmd/initcmd.go#ledgerText s1:9afbf1fd266bb5e7 -->

## Agent guard

`ytif guard` is a PreToolUse hook that refuses agent commands whose recorded
duration predicts they exceed the ledger's `guard.limit` (default 2m),
registered by `ytif init` for the selected hosts:

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
The Claude Code and Codex hooks run it from the project root
(`$CLAUDE_PROJECT_DIR`, the Git top level), because those hosts start hooks in
the session's directory, where a nested module without the pin cannot resolve
the tool.
When a guard cannot start, every host allows the command with a warning. The
hooks end in `|| exit 1` because these hosts read exit 2 as a refusal, and Go
exits 2 when it cannot run the tool. Rerunning `ytif init` rewrites a guard
hook an earlier version registered.

It prices, directly or through a package manager, `ytif commit`, `ytif push`,
and `ytif ci`; supported test runners; package scripts and Nx
targets (single-target bodies only, `nx affected` stays unknown); and
ledger-registered tool modes such as `prettier --check`, `tsc --noEmit`, and
`eslint`. Name filters and unresolvable selections stay unknown and allowed.

Whole-command elapsed evidence applies first, undivided. Otherwise the estimate
is `max(longest, sum/workers)` over the identified verifications: shell lists
add, gates divide by the process limit they would run with, serial sources (dotnet, node/go
verify, nx-target) sum first, and unresolved worker counts fall back
to the longest component. Evidence is local records only (last 10 samples in 30
days per gate, largest gate mean; dotnet samples require one run invocation per
attempt and project, which does not prove an unfiltered run). Unknown
stays allowed and is never shown as cheap.

The guard reads command text and the package scripts it names; work started
from another file or program passes. The fixed refusal asks the agent to narrow
its verification and leave the rest to the gates. Estimates and their evidence
are stored in the guard record for the owner, rather than in that message.

`ytif allow 8h` lifts the duration rule for unattended work, and
`ytif allow --off` ends it. Only the user can grant it; an agent's own
`ytif allow` is always refused.

## License

[MIT](LICENSE)
