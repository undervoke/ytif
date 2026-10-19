// Package integrations holds the guard plugins for agents whose hooks are
// code rather than configuration, so ytif init can write them.
package integrations

import _ "embed"

var (
	//go:embed opencode/ytif-guard.ts
	OpenCode string
	//go:embed kilo/ytif-guard.ts
	Kilo string
	//go:embed pi/ytif-guard.ts
	Pi string
)
