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

Gate execution selects membership and affected scope before preparing and discovering active runners.
List and effective-full CI perform complete reconciliation; every other gate run reconciles only the sources it activated.
Dispatch closes required co-runs within gate membership, then produces verdicts and shared selection/result records.

@import "gates.mermaid"

## records

Recording follows agent-invoked check, commit/push hooks, and remote CI workflows with an explicit record path.
The host workflow owns CI artifact publication and retention; the developer supplies downloaded records to the board.
The board combines the current registry with the chosen record files into a static snapshot.

@import "records.mermaid"
