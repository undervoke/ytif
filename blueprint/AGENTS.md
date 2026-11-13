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

Recording follows commit/push hooks and CI workflows with an explicit record path.
The host workflow owns CI artifact publication and retention; the board syncs those artifacts into a cache beside the local records.
The board combines the current registry with the local records and the cached CI records into a static snapshot.

@import "records.mermaid"

## gate scope

The `gate:` section is required; a ledger without it fails to load, and `ytif init` adds the missing gate with init defaults.
A version 3 ledger is converted by migration, never by init.
Push membership defaults to include and can default to exclude instead, with each entry overriding the default by an explicit choice.
An omitted entry choice inherits the push default, so an explicit choice survives a later default change.
Commit membership stays an independent opt-in, and every verification stays a CI member.
A gate runs each member at one of three scopes: file runs it on changed files only, affected skips it when the build tool's affected graph leaves its project out, and full runs its registered invocation unchanged.
The most specific scope wins: `ci` and every push prefer entry `ci/<profile>`, then entry `ci`, then the mapped profile, then gate `ci`; commit prefers the entry scope, then the gate scope.
Membership still decides which verifications a gate runs; scope decides only how much of each one runs.
A push previews one CI scope and target: the common CI policy, or the named profile when `ci-profile` selects one.
The push default and the CI profile are independent: the default decides which entries belong to push, the profile decides which CI scope and target the included entries use.
File scope applies only where ytif can name the files a verification reads; elsewhere it falls back to affected, and affected falls back to full where the build tool's graph does not map the verification.
Narrowing that cannot be established widens to full and records why; a push without a usable comparison target widens the same way.
Invalid configuration or context fails instead.
A verification that selects its own work from the comparison receives the resolved scope, but its actual scope stays unknown unless it reports one.

@import "gate-scope.mermaid"

The changes a gate compares choose targets; every verification still reads the checked-out working tree.

| Gate | Changes compared | Code that runs |
|---|---|---|
| commit | HEAD → staged paths | Working tree |
| push | Merge base of the selected CI profile target and each pushed tip → that tip | Working tree |
| ci, pull request | Merge base of the PR base and head → PR head | The host's checkout, usually the PR merge |

When comparison is needed, push fetches its profile's target once into a ytif-owned ref, so the comparison does not depend on when the user last fetched.
When pushed tips differ from the working tree, narrowed verifications run full, because the result describes the working tree rather than those tips. <!-- dinyd: internal/gate/plans.go#resolvePlans s1:bd6bfcc1862b26c7 -->

## nx targets

Nx owns project actions and the graph; ytif owns membership, selection, and records.
A target becomes a verification only through an explicit marker, because a target can be any action, including build and serve.
The key's unit is the project root, so a path stays the only ownership evidence and `cwd` stays an execution detail.
Enrollment sits at the root of the ledger rather than following the entry list, because discovery must not depend on the entries it audits.
Enrollment is explicit because discovery fails the gate when the graph cannot be read, and a repository that never adopted the runner must not take on that dependency.
One target invocation is one verdict; ytif infers nothing about the tasks Nx runs behind it.
Affected scope narrows an entry by its own task rather than its project, because the entry runs exactly that task; a target the graph lacks is unknown rather than unaffected, so the entry runs and discovery can report it stale.
