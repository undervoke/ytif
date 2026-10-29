# Blueprint

<!-- dinyd-lock -->
Project documents are not diaries.
Before writing a Mermaid diagram, read [diagram-examples/AGENTS.md](diagram-examples/AGENTS.md); it holds the diagram rules and one example per type.

## Document · Diagram · Code ownership

- Documents own decisions, rationale, and intended boundaries that cannot be inferred from code.
- Diagrams explain cross-file and process relationships at an abstraction level that avoids copying code literals.
- Code owns implementation facts; link to their source instead of maintaining parallel inventories or configuration values.
- Topic documents import their diagrams.
- Keep each independently reviewable claim on its own physical line.
<!-- /dinyd-lock -->

## gates

Gate execution plans each member from the mandatory gate policy and resolves file and affected scopes against the comparison before preparing and discovering active runners.
List and effective-full CI perform complete reconciliation; every other gate run reconciles only the sources it activated.
Dispatch closes required co-runs within gate membership, then produces verdicts and shared selection/result records.

@import "gates.mermaid"

## records

Recording follows check invocations, commit/push hooks, and CI workflows with an explicit record path.
The host workflow owns CI artifact publication and retention; the board syncs those artifacts into a cache beside the local records.
The board combines the current registry with the local records and the cached CI records into a static snapshot.

@import "records.mermaid"

## gate scope

The `gate:` section is required; a ledger without it fails to load, and `ytif init` adds the missing gate with init defaults.
A gate runs each member at one of three scopes: file runs it on changed files only, affected skips it when the build tool's affected graph leaves its project out, and full runs its registered invocation unchanged.
The most specific scope wins: `ci` and a mapped push prefer entry `ci/<profile>`, then entry `ci`, then the profile, then gate `ci`; check, commit, and an unmapped push prefer the entry scope, then the gate scope.
Membership still decides which verifications a gate runs; scope decides only how much of each one runs.
Push maps to one CI profile's scope and target, so a push previews that profile's CI selection against the same target branch.
File scope applies only where ytif can name the files a verification reads; elsewhere it falls back to affected, and affected falls back to full where the build tool's graph does not map the verification.
Narrowing that cannot be established widens to full and records why; invalid configuration or context fails instead.
A verification that selects its own work from the comparison receives the resolved scope, but its actual scope stays unknown unless it reports one.

@import "gate-scope.mermaid"

The changes a gate compares choose targets; every verification still reads the checked-out working tree.

| Gate | Changes compared | Code that runs |
|---|---|---|
| check | HEAD → working tree, untracked files included | Working tree |
| commit | HEAD → staged paths | Working tree |
| push | Merge base of the profile target and each pushed tip → that tip | Working tree |
| ci, pull request | Merge base of the PR base and head → PR head | The host's checkout, usually the PR merge |

When comparison is needed, push fetches its profile's target once into a ytif-owned ref, so the comparison does not depend on when the user last fetched.
When pushed tips differ from the working tree, narrowed verifications run full, because the result describes the working tree rather than those tips. <!-- dinyd: internal/gate/plans.go#resolvePlans s1:bf96ec39f703fdb6 -->
