package gateconf

import "github.com/undervoke/ytif/internal/shellcmd"

// BuildCommand recognizes explicit build invocations for registered commands'
// admission. It does not change the gate classifier or the agent guard.
func BuildCommand(c shellcmd.Command) string {
	i, ok := operand(c, 1, nil)
	switch c.NameBase() {
	case "go":
		i, ok = goSubcommand(c)
	case "bun":
		i, ok = bunSubcommand(c)
	case "tsc", "vue-tsc":
		for j := 1; j < len(c.Args); j++ {
			if c.Literal[j] && (c.Args[j] == "--build" || c.Args[j] == "-b") {
				return c.NameBase() + " --build"
			}
		}
		return ""
	case "msbuild":
		return "msbuild"
	}
	if !ok || i >= len(c.Args) {
		return ""
	}
	sub := c.Args[i]
	switch c.NameBase() {
	case "go", "bun", "vite", "nuxt", "nuxi", "cargo":
		if sub == "build" {
			return c.NameBase() + " build"
		}
	case "dotnet":
		if sub == "build" || sub == "publish" || sub == "msbuild" {
			return "dotnet " + sub
		}
	}
	return ""
}
