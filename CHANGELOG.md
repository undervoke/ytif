# Changelog

Each release tag `vX.Y.Z` publishes its `## [X.Y.Z]` section below as the
GitHub release notes, so the section must be committed before the tag. The
release workflow fails without it; a pushed tag stays where it is, since the Go
module proxy may already have cached that version.

## [Unreleased]

### Added

- `board.half_life_percent` sets how fast a result's weight fades in a board mean: it halves every that percent of the gate's `average` window (default 50).

### Changed

- Board means weight each gate's last N timed results toward the newest instead of weighing them equally.
- Time bars draw the ticks under their fill on top of it. The gate summary card's bars share a time scale with ticks instead of filling the width.

### Fixed

- The verification table's "more" button sits centered below the table.
- Choosing a verification in the diagram rail keeps the rail's scroll position.

## [0.13.1] - 2026-10-26

### Fixed

- ytif builds on Windows again, and record retention there renames and deletes files other ytif processes hold open, as on unix.

## [0.13.0] - 2026-10-26

### Added

- `board.scales` sets the time bars' ticks in milliseconds, `verification` for single verifications and `unit` for unit, file, and process totals; each interval takes an equal width. The verification page's execution-unit rows extend the verification ticks tenfold until they cover the largest total.
- Recording and `ytif board` delete closed local record files and dated CI artifacts older than 400 days or beyond 1 GiB, counted separately for local and CI records; the open file and undated artifacts can exceed these limits. `<git-common-dir>/ytif/storage.json` overrides the limits.

### Changed

- Local records move to `<git-common-dir>/ytif/records/`. The previous `records.jsonl` still feeds statistics but is excluded from the limits and never deleted; remove it by hand once no older ytif writes it and its history is no longer needed.
- The verification table and the diagram's cost bar use the diagram rail's time scales, and the table shows each verification's impact, runner, and short name above its bar; the full key moves to the expanded row.
- The diagram rail labels execution units by file name and verifications by their short name.

## [0.12.0] - 2026-10-25

### Added

- `board:` takes `recent_fail_days`, how far back a fail marks a verification as recently failed (default 30).
- A ledger `ytif init` creates lists the `board:` keys and their defaults as a comment.

## [0.11.0] - 2026-10-23

### Added

- `ytif board` syncs the `ytif-records` CI artifacts of a GitHub origin into `<git-common-dir>/ytif/ci-cache/`, so CI records appear without downloading them manually. It authenticates with `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token`, and renders from the cache when credentials, the network, or artifacts are missing.
- A root-only `board:` section in `ytif-verifications.yaml` sets the statistics windows: the timed results per mean for each gate and the hit window.
- Gate records carry the current branch, and the board shows each gate's latest run on that branch.
- Run inside ytif's own module, `ytif init` registers the checkout's `cmd/ytif` as a tool instead of pinning a release, and its agent guards run the installed ytif on PATH.

### Changed

- Board statistics use the mean of each verification's last N timed results per gate (100, or 10 for ci) instead of totals over every record. A gate's time is the sum of its members' means, and hits count within the last 365 days by default.
- The cost and hit page merges into the verification page, with a ledger file tree, gate totals, a time-per-gate table on a log scale, a process-time view, and one filter card.
- The diagram page groups verifications in a collapsible rail by file or runner, with search and a tag filter.
- **Breaking:** gates record only from their designed triggers: `ytif check` runs, `ytif commit` and `ytif push` with `--hook`, and `ytif ci` with an explicit `--records` path. Other invocations run and report as before without writing records.
- The board takes ci records only from the synced artifact cache; local records and `--records` files supply check, commit, and push. A CI record file passed with `--records` no longer shows.

### Removed

- **Breaking:** `ytif stats`. The board computes costs and hits from the records.

### Upgrade

```sh
go get -tool github.com/undervoke/ytif/cmd/ytif@v0.11.0
```

Hooks wired by an earlier `ytif init` run `go tool ytif commit` and `go tool ytif push` without `--hook`, and rerunning init leaves them unchanged. Append `--hook` to both lefthook commands, or those gates keep passing without writing records. Keep the CI step's explicit `--records` path.

## [0.10.0] - 2026-10-21

### Changed

- **Breaking:** the ledger moves from `ytif-inventory.yaml` (version 2) to `ytif-verifications.yaml` (version 3). Entries declare gate membership with `check`, `commit`, and `skip-push` instead of a cumulative `placement`: every entry runs at ci, and every entry not marked `skip-push` runs at push. `commands:` entries become `runner: command` entries, and `ytif check` runs the entries marked `check: true` that its admission accepts. The root ledger can include fragments.
- `--base` selects Nx-affected verifications at every gate, falling back to every member without a usable Nx graph.
- Local gates and affected CI prepare and discover only the runners their selection needs; `ytif list` and full CI still discover every runner.
- Each gate run, including a rejected admission, records its eligible and selected verifications and the omission reasons; the board shows them with gate filters and membership badges.
- Child processes receive `YTIF_MODE`, `YTIF_BASE`, and `YTIF_HEAD`, and node verify functions receive mode, base, and head.

### Added

- `ytif migrate` converts a version-2 ledger.
- `ytif init` formats generated files with the project's Prettier when `node_modules/.bin/prettier` exists and Biome does not.

### Upgrade

```sh
go get -tool github.com/undervoke/ytif/cmd/ytif@v0.10.0
go tool ytif migrate
```

`placement` and `commands` are no longer accepted, and a legacy `ytif-inventory.yaml` alone or beside the new root fails to load. Migrated entries that ran only at ci now also run at push; mark them `skip-push` where that is not wanted.

## [0.9.0] - 2026-10-19

### Added

- Select agent integrations with `ytif init --hosts claude-code,codex,opencode,kilo,cursor,pi`, or choose hosts interactively.
- Use `ytif init --hosts none` to install only the gates. Unselected host files remain untouched.
- Support OpenCode v1 (1.18.29+) and the v2 2.0.32 plugin API in one plugin, with per-session directory resolution.
- Add the current OpenCode-based Kilo integration at `.kilo/plugin/ytif-guard.ts`.
- Add Cursor's native `beforeShellExecution` hook and `ytif guard --host cursor`, sharing the existing refusal policy, allowances, and records.
- Document Orca adoption through the agent hosts it launches.

### Upgrade

```sh
go install github.com/undervoke/ytif/cmd/ytif@v0.9.0
ytif init --hosts claude-code,codex
```

Run init at the project root and replace the host list with the integrations you use. It pins this release and refreshes only the selected registrations.

Noninteractive `ytif init` requires an explicit `--hosts` list or `--hosts none`; it no longer installs every host. Existing registrations are not removed when omitted from the selection.

## [0.8.0] - 2026-10-19

### Added

- `ytif check` runs every entry registered under `commands:` in `ytif-inventory.yaml`.
- Two concurrent entries by default; `--jobs N` controls concurrency and shared `exclusive` tokens prevent overlap.
- Static package-script, TypeScript and Nx inspection rejects identifiable builds and tests before any registered command starts. `nx affected` is checked against all non-excluded declared projects.
- Separate execution timing and outcomes are available through `ytif stats --gate check`.

### Fixed

- `ytif init` validates existing gate and guard wiring more carefully, detects active vendoring before changing the pin, and handles parallel pre-push stdin consumers.

### Upgrade

```sh
go get -tool github.com/undervoke/ytif/cmd/ytif@v0.8.0
```

Existing `version: 2` inventories remain compatible. `commands:` is optional and separate from gate checks; a successful `ytif check` does not replace a gate run.

## [0.7.0] - 2026-10-16

### Added

- `ytif init` adopts ytif in one rerunnable command: it pins the running ytif as a Go tool, creates the inventory and exemptions, wires `ytif commit` and `ytif push` into lefthook and `ytif ci` into a GitHub workflow when missing, and registers the agent guard for Claude Code, Codex, OpenCode, and Pi. It checks every file before writing any.

### Changed

- The agent guard follows `npx`, `pnpx`, `bunx`, and package-manager exec, dlx, and binary shorthands, and also refuses `*.verify.mjs` modules, package scripts, Nx targets, and Make goals named test, check, or verify, check-only formatters and linters, and package scripts whose body runs any of these.

### Fixed

- Clock times such as `Time Elapsed 00:00:14.55` are no longer read as `path:line` findings.
- A passing Playwright test's stdout and stderr are printed under its PASS line, so its workflow commands reach GitHub.

## [0.6.0] - 2026-10-15

### Added

- `ytif update` replaces the PATH install with the latest release.

### Changed

- A ytif installed on PATH hands every command except `update` to `go tool ytif` when the working directory's module pins a different ytif version.

## [0.5.0] - 2026-10-15

### Changed

- **Breaking:** `ytif-routing.yaml` becomes `ytif-gate-exemptions.yaml`. An entry names a step by its exact run text or action `owner/repo` and replaces `kind` with a required `reason`, and any gate step that runs code outside ytif is a finding unless listed.
- A step that runs a test runner directly stays a finding even when listed, and the agent guard refuses it through the same detector.

## [0.4.0] - 2026-10-14

### Changed

- A gate discovers and runs its runners concurrently under one process limit, set with `--jobs N` (default: the CPU count). Log lines carry a `[runner]` prefix.

### Fixed

- .NET test filters longer than one command line are split into batches instead of failing with "argument list too long".

## [0.3.0] - 2026-10-08

### Added

- Vitest and Playwright runners.
- The node-verify runner for exported `verifyXxx` functions in tracked `*.verify.mjs` files.
- The inventory's `prepare` list runs before discovery.
- `ytif ci --base REF` narrows test checks to Nx-affected projects.

## [0.2.0] - 2026-10-07

### Added

- **Breaking:** contracts require `detection`, how the check catches its accident.
- The board has a theme toggle and a language menu.
- `go run ./internal/board/devserve <repo>` serves a repository's board from the asset files on disk.

## [0.1.0] - 2026-10-07

### Added

- ytif keeps every project-owned check on one rail: each check carries a contract in the inventory, and the commit, push, and ci gates run checks by placement and record their outcomes and costs.
- Runners for Go `VerifyXxx` functions, `go test`, bun and node tests, and `dotnet test`.
- `ytif guard` refuses agent-invoked test runners and ytif gates; `ytif allow` lets the user permit them for a limited time.
- Inventory version 2 with tags, hits counting, `go tool ytif` as the gate command, and `ytif board`, a static HTML page of the inventory and records.
- A passing check's output is printed as its log.

### Fixed

- Checks run without the hook repository's git environment variables.

[Unreleased]: https://github.com/undervoke/ytif/compare/v0.13.1...HEAD
[0.13.1]: https://github.com/undervoke/ytif/compare/v0.13.0...v0.13.1
[0.13.0]: https://github.com/undervoke/ytif/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/undervoke/ytif/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/undervoke/ytif/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/undervoke/ytif/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/undervoke/ytif/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/undervoke/ytif/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/undervoke/ytif/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/undervoke/ytif/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/undervoke/ytif/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/undervoke/ytif/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/undervoke/ytif/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/undervoke/ytif/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/undervoke/ytif/releases/tag/v0.1.0
