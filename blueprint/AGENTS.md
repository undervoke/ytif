# Blueprint

ytif's current architecture. Each diagram owns one view; its leading `%%` comments identify the behavior authority in `README.md` and `internal/`. Project-specific configuration belongs in that project's documentation.

## Diagrams

- `.mermaid` only; MPE `@import` treats `.mmd` as plain code. Palette targets the VS Code dark background `#1f1f1f`.
- Leading `%%` names the behavior authority.
- English prose and diagram labels; identifiers verbatim. Frontmatter `title`; group titles instead of legends.
- Node: `heading<br/><small>detail</small>`. Two or more distinct statements: one `• ` line each; one `A → B → C` chain stays one line.
- Achromatic by default. Color only for `em` (focus), `issue` (observed defect), `legacy` (superseded), and infrastructure kinds `db` · `cache` · `queue` · `file` · `ext`. No per-node `style`, so palette edits stay in `classDef` and frontmatter.
- Copy frontmatter and `classDef`s from an existing diagram of the same type.

## gates

Gate execution: resolve `--base`, survey (prepare, discovery, inventory reconciliation, gate-config), cumulative placement selection, narrowing, dispatch, verdict, records.

@import "gates.mermaid"

## check

The separate execution path from registered `commands` through `ytif check`.

@import "check.mermaid"

## runners

Discovery sources and check keys for the eight runners: six test runners and two verify runners.

@import "runners.mermaid"
