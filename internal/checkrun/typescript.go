package checkrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/undervoke/ytif/internal/shellcmd"
)

// A type check is admissible; a known emitting compiler invocation is a build.
// Strict-JSON configs and relative extends are read without loading TypeScript.
// Package-resolved extends and JSONC remain unresolved unless the CLI decides.
func typescriptBuild(c shellcmd.Command, dir string) string {
	if c.NameBase() != "tsc" && c.NameBase() != "vue-tsc" {
		return ""
	}
	var noEmit *bool
	project := ""
	files := false
	ambiguousOperands := false
	for i := 1; i < len(c.Args); i++ {
		if !c.Literal[i] {
			return ""
		}
		a, value, inline := strings.Cut(c.Args[i], "=")
		switch a {
		case "--help", "-h", "--version", "-v", "--showConfig", "--init":
			return ""
		case "--noEmit":
			v := true
			if inline {
				if value != "true" && value != "false" {
					return ""
				}
				v = value == "true"
			} else if i+1 < len(c.Args) && c.Literal[i+1] && (c.Args[i+1] == "true" || c.Args[i+1] == "false") {
				i++
				v = c.Args[i] == "true"
			}
			noEmit = &v
		case "-p", "--project":
			if !inline {
				if i+1 >= len(c.Args) || !c.Literal[i+1] {
					return ""
				}
				i++
				value = c.Args[i]
			}
			project = value
		case "--types", "--typeRoots", "--lib", "--target", "-t", "--module", "-m", "--moduleResolution", "--jsx", "--jsxFactory", "--jsxFragmentFactory", "--jsxImportSource", "--outDir", "--outFile", "--rootDir", "--rootDirs", "--baseUrl", "--declarationDir", "--tsBuildInfoFile", "--mapRoot", "--sourceRoot", "--locale", "--moduleSuffixes", "--customConditions":
			if !inline {
				if i+1 >= len(c.Args) || !c.Literal[i+1] {
					return ""
				}
				i++
			}
		default:
			if strings.HasPrefix(a, "-") {
				// Unknown option arity must not turn its value into a source file.
				ambiguousOperands = true
				continue
			}
			if !ambiguousOperands && (strings.HasSuffix(a, ".ts") || strings.HasSuffix(a, ".tsx") || strings.HasSuffix(a, ".js") || strings.HasSuffix(a, ".jsx")) {
				files = true
			}
		}
	}
	if noEmit != nil && *noEmit {
		return ""
	}
	if files && project == "" {
		return c.NameBase() + " emits compiled files"
	}
	if dir == "" || c.Chdir {
		return ""
	}
	file := ""
	if project != "" {
		file = workingDir(dir, project)
		if info, err := os.Stat(file); err == nil && info.IsDir() {
			file = filepath.Join(file, "tsconfig.json")
		}
	} else {
		for d := dir; ; d = filepath.Dir(d) {
			candidate := filepath.Join(d, "tsconfig.json")
			if _, err := os.Stat(candidate); err == nil {
				file = candidate
				break
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	if file == "" {
		return ""
	}
	value, known := configNoEmit(file, 0)
	if noEmit != nil {
		value, known = *noEmit, true
	}
	if known && !value {
		return c.NameBase() + " emits compiled files"
	}
	return ""
}

func configNoEmit(file string, depth int) (bool, bool) {
	if depth >= 8 {
		return false, false
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return false, false
	}
	var cfg struct {
		Extends         json.RawMessage `json:"extends"`
		CompilerOptions struct {
			NoEmit *bool `json:"noEmit"`
		} `json:"compilerOptions"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return false, false
	}
	if cfg.CompilerOptions.NoEmit != nil {
		return *cfg.CompilerOptions.NoEmit, true
	}
	if len(cfg.Extends) == 0 {
		return false, true
	}
	var parent string
	if json.Unmarshal(cfg.Extends, &parent) != nil || (!strings.HasPrefix(parent, ".") && !filepath.IsAbs(parent)) {
		return false, false
	}
	parent = workingDir(filepath.Dir(file), parent)
	if filepath.Ext(parent) == "" {
		parent += ".json"
	}
	return configNoEmit(parent, depth+1)
}
