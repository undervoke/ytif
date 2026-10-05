// Package gomod maps tracked Go files to the modules that build them.
package gomod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/build"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Modules lists the directories of tracked go.mod files, deepest first, so
// the first match in Owner is a directory's own module.
func Modules(tracked []string) []string {
	var mods []string
	for _, f := range tracked {
		if path.Base(f) == "go.mod" && !Excluded(f) {
			mods = append(mods, path.Dir(f))
		}
	}
	sort.Slice(mods, func(i, j int) bool {
		if di, dj := depth(mods[i]), depth(mods[j]); di != dj {
			return di > dj
		}
		return mods[i] < mods[j]
	})
	return mods
}

func depth(dir string) int {
	if dir == "." {
		return 0
	}
	return strings.Count(dir, "/") + 1
}

// Owner returns the module that contains dir.
func Owner(mods []string, dir string) (string, bool) {
	for _, m := range mods {
		if m == "." || dir == m || strings.HasPrefix(dir, m+"/") {
			return m, true
		}
	}
	return "", false
}

// Rel returns dir relative to its module root, or "" for the root itself.
func Rel(mod, dir string) string {
	rel := dir
	if mod != "." {
		rel = strings.TrimPrefix(strings.TrimPrefix(dir, mod), "/")
	}
	if rel == "." {
		return ""
	}
	return rel
}

// Excluded reports files the go command never builds as part of a module
// package: those under vendor, testdata, or a directory starting with . or _.
func Excluded(file string) bool {
	for _, el := range strings.Split(path.Dir(file), "/") {
		if el == "vendor" || el == "testdata" || (el != "." && (strings.HasPrefix(el, ".") || strings.HasPrefix(el, "_"))) {
			return true
		}
	}
	return false
}

// BuildContext returns the build context the go command uses in dir: the
// default context with the go command's effective GOOS, GOARCH, CGO_ENABLED,
// Go version, and GOFLAGS build tags, including settings saved with
// go env -w. Discovery matches files with it so it agrees with the go
// command that later builds them.
func BuildContext(ctx context.Context, dir string) (build.Context, error) {
	cmd := exec.CommandContext(ctx, "go", "env", "-json", "GOOS", "GOARCH", "CGO_ENABLED", "GOFLAGS", "GOVERSION")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return build.Context{}, fmt.Errorf("go env: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var env struct{ GOOS, GOARCH, CGO_ENABLED, GOFLAGS, GOVERSION string }
	if err := json.Unmarshal(out, &env); err != nil {
		return build.Context{}, fmt.Errorf("go env: %w", err)
	}
	c := build.Default
	c.GOOS, c.GOARCH = env.GOOS, env.GOARCH
	c.CgoEnabled = env.CGO_ENABLED == "1"
	tags, err := flagTags(env.GOFLAGS)
	if err != nil {
		return build.Context{}, fmt.Errorf("parsing GOFLAGS: %w", err)
	}
	c.BuildTags = tags
	if m := goMinor.FindStringSubmatch(env.GOVERSION); m != nil {
		n, _ := strconv.Atoi(m[1])
		c.ReleaseTags = nil
		for i := 1; i <= n; i++ {
			c.ReleaseTags = append(c.ReleaseTags, "go1."+strconv.Itoa(i))
		}
	}
	return c, nil
}

var goMinor = regexp.MustCompile(`go1\.(\d+)`)

// flagTags returns the build tags a GOFLAGS value sets, read as the go
// command reads them: GOFLAGS splits into fields that may be quoted, the
// last -tags wins, and its value is a comma-separated list unless it holds a
// space or quote, when it splits like GOFLAGS itself.
func flagTags(goflags string) ([]string, error) {
	fields, err := splitQuoted(goflags)
	if err != nil {
		return nil, err
	}
	var last string
	for _, f := range fields {
		for _, prefix := range []string{"-tags=", "--tags="} {
			if v, ok := strings.CutPrefix(f, prefix); ok {
				last = v
			}
		}
	}
	if strings.ContainsAny(last, " '") {
		return splitQuoted(last)
	}
	var tags []string
	for _, t := range strings.Split(last, ",") {
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags, nil
}

// splitQuoted splits s into space-separated fields, a field may be wrapped
// in single or double quotes, and nothing inside quotes is unescaped — the
// go command's rule for GOFLAGS and list-valued flags.
func splitQuoted(s string) ([]string, error) {
	var fields []string
	for {
		s = strings.TrimLeft(s, " \t\n\r")
		if s == "" {
			return fields, nil
		}
		if q := s[0]; q == '"' || q == '\'' {
			end := strings.IndexByte(s[1:], q)
			if end < 0 {
				return nil, fmt.Errorf("unterminated %c string", q)
			}
			fields = append(fields, s[1:1+end])
			s = s[2+end:]
			continue
		}
		end := strings.IndexAny(s, " \t\n\r")
		if end < 0 {
			end = len(s)
		}
		fields = append(fields, s[:end])
		s = s[end:]
	}
}
