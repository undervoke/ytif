# Blueprint

ytif의 현재 구조. 각 다이어그램이 하나의 축을 소유하고, 동작 정본은 각 `%%` 주석이 담는다. 정본은 `README.md`와 `internal/`이다. 프로젝트가 고르는 설정은 각 프로젝트의 문서가 소유한다.

## Diagrams

- `.mermaid` only; MPE `@import` treats `.mmd` as plain code. Palette targets the VS Code dark background `#1f1f1f`.
- Leading `%%` names the behavior authority.
- Korean text, identifiers verbatim. Frontmatter `title`; group titles instead of legends.
- Node: `heading<br/><small>detail</small>`. Two or more distinct statements: one `• ` line each; one `A → B → C` chain stays one line.
- Achromatic by default. Color only for `em` (focus), `issue` (observed defect), `legacy` (superseded), and infrastructure kinds `db` · `cache` · `queue` · `file` · `ext`. No per-node `style`, so palette edits stay in `classDef` and frontmatter.
- Copy frontmatter and `classDef`s from an existing diagram of the same type.

## gates

gate 하나가 실행되는 순서: `--base` 해석, survey(prepare · 탐색 · inventory 대조 · gate-config), placement 누적 선택, `--base` 축소, 실행, 판정, records.

@import "gates.mermaid"

## check

`commands`에 등록한 명령을 `ytif check`로 실행하는 별도 경로.

@import "check.mermaid"

## runners

runner 8개(test 6 · verify 2)가 검사를 발견하는 출처와 검사 key의 구성.

@import "runners.mermaid"
