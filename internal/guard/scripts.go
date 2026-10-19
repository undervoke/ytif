package guard

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
	"mvdan.cc/sh/v3/syntax"

	"github.com/undervoke/ytif/internal/gateconf"
	"github.com/undervoke/ytif/internal/shellcmd"
)

// scripts reads the package scripts an agent command runs, so a check
// behind a script name is refused as if the agent had typed the script.
// It reads only the scripts the command names, in the packages it selects,
// with the pre and post scripts the package manager runs. What it cannot
// resolve — a changed directory, an unsupported selection, a script past
// its limits — it passes over: the gates, not the guard, are the authority.
type scripts struct {
	// openChecks reports whether to read a script whose name is already a
	// check, which matters only to find a ytif allow behind it.
	openChecks func() bool
	visits     int
	seen       map[string]bool
	manifests  map[string]*manifest
	allow      bool   // a script runs ytif allow
	hit        string // the first check a script runs
	judge      func(shellcmd.Command, string) string
}

const (
	maxScriptDepth = 8
	maxScripts     = 64
)

type manifest struct {
	Name           string            `json:"name"`
	Scripts        map[string]string `json:"scripts"`
	Workspaces     json.RawMessage   `json:"workspaces"`
	PackageManager string            `json:"packageManager"`
}

func newScripts(openChecks func() bool) *scripts {
	return &scripts{openChecks: openChecks, seen: map[string]bool{}, manifests: map[string]*manifest{},
		judge: func(c shellcmd.Command, _ string) string { return checkRun(c) }}
}

// Inspect follows the same literal wrappers and package scripts as the guard,
// using judge instead of the guard's refusal policy. Unknown directories and
// unresolved script branches retain the guard's bounded, best-effort behavior.
func Inspect(command, dir string, judge func(shellcmd.Command, string) string) string {
	s := newScripts(func() bool { return true })
	s.judge = judge
	cmds, _ := shellcmd.Parse(command)
	s.walk(s.expand(cmds, dir), dir, 0)
	return s.hit
}

// walk judges cmds, run in dir ("" when unknown), and follows the package
// scripts they run.
func (s *scripts) walk(cmds []shellcmd.Command, dir string, depth int) {
	if changesDir(cmds) {
		dir = ""
	}
	for _, c := range cmds {
		if gateconf.RailCommand(c) == "allow" {
			s.allow = true
		}
		if h := s.judge(c, dir); h != "" && s.hit == "" {
			s.hit = h
		}
		run, ok := gateconf.PackageScript(c)
		if !ok || dir == "" || c.Chdir || (checkName.MatchString(run.Name) && !s.openChecks()) {
			continue
		}
		for _, pkg := range s.packages(run, runBase(run, dir)) {
			s.run(run, pkg, depth)
		}
	}
}

// hasScript reports whether a package c runs in, from dir, has the script c
// names; false when the guard cannot tell.
func (s *scripts) hasScript(c shellcmd.Command, dir string) bool {
	run, ok := gateconf.PackageScript(c)
	if !ok || dir == "" || c.Chdir {
		return false
	}
	for _, pkg := range s.packages(run, runBase(run, dir)) {
		if m := s.manifest(pkg); m != nil {
			if _, ok := m.Scripts[run.Name]; ok {
				return true
			}
		}
	}
	return false
}

// runBase returns the directory r starts from when run in dir.
func runBase(r gateconf.ScriptRun, dir string) string {
	switch {
	case r.Dir == "":
		return dir
	case filepath.IsAbs(r.Dir):
		return r.Dir
	}
	return filepath.Join(dir, r.Dir)
}

// changesDir reports whether a script changes its directory, after which
// the guard no longer knows where later commands run.
func changesDir(cmds []shellcmd.Command) bool {
	for _, c := range cmds {
		switch c.Name() {
		case "cd", "pushd", "popd":
			return true
		}
	}
	return false
}

// run reads script r.Name of the package in dir, with the words passed on
// to it, and its pre and post scripts when the package manager runs them,
// and walks their commands in that directory.
func (s *scripts) run(r gateconf.ScriptRun, dir string, depth int) {
	m := s.manifest(dir)
	if m == nil || depth >= maxScriptDepth {
		return
	}
	names := []string{r.Name}
	if s.lifecycle(r, dir) {
		names = []string{"pre" + r.Name, r.Name, "post" + r.Name}
	}
	for _, name := range names {
		body, ok := m.Scripts[name]
		key := dir + "\x00" + name
		if ok && name == r.Name && len(r.Args) > 0 {
			body, ok = withArgs(body, r.Args)
			key += "\x00" + strings.Join(r.Args, "\x00")
		}
		if !ok || s.seen[key] || s.visits >= maxScripts {
			continue
		}
		s.seen[key] = true
		s.visits++
		cmds, _ := shellcmd.Parse(body)
		s.walk(s.expand(cmds, dir), dir, depth+1)
	}
}

// withArgs returns the script the package manager runs for body with args
// passed on: the words appended, quoted.
func withArgs(body string, args []string) (string, bool) {
	words := []string{body}
	for _, a := range args {
		q, err := syntax.Quote(a, syntax.LangBash)
		if err != nil {
			return "", false
		}
		words = append(words, q)
	}
	return strings.Join(words, " "), true
}

// lifecycle reports whether the package manager runs pre and post scripts
// around r: npm does unless ignore-scripts is set, Bun does, Yarn 1 does
// and later Yarn does not, pnpm does unless enablePrePostScripts is off,
// and node --run does not.
func (s *scripts) lifecycle(r gateconf.ScriptRun, dir string) bool {
	switch r.Manager {
	case "npm":
		if r.IgnoreScripts != nil {
			return !*r.IgnoreScripts
		}
		ignore, _ := npmrcSetting(dir, "ignore-scripts")
		return !ignore
	case "bun":
		return true
	case "yarn":
		return s.yarnMajor(dir) < 2
	case "pnpm":
		return pnpmSetting(s.pnpmRoot(dir), dir, "enablePrePostScripts", "enable-pre-post-scripts", true)
	}
	return false
}

// packages returns the package directories r runs its script in, from
// base: the nearest package, or the workspace packages it selects.
func (s *scripts) packages(r gateconf.ScriptRun, base string) []string {
	if r.Opaque {
		return nil
	}
	if !r.Recursive && len(r.Filters) == 0 {
		if r.WorkspaceRoot && r.Manager == "pnpm" {
			if root := s.pnpmRoot(base); root != "" {
				return []string{root}
			}
			return nil
		}
		if dir := s.nearest(base); dir != "" {
			return []string{dir}
		}
		return nil
	}
	root := s.workspaceRoot(r.Manager, base)
	if root == "" {
		return nil
	}
	pkgs := s.workspacePackages(r.Manager, root)
	withRoot := func(dirs []string) []string {
		if !slices.Contains(dirs, root) {
			dirs = append([]string{root}, dirs...)
		}
		return dirs
	}
	if len(r.Filters) == 0 {
		switch {
		case r.Manager == "pnpm" && pnpmSetting(root, root, "includeWorkspaceRoot", "include-workspace-root", false),
			r.Manager == "npm" && r.WorkspaceRoot,
			r.Manager == "yarn":
			return withRoot(pkgs)
		}
		return pkgs
	}
	candidates := withRoot(pkgs)
	var out []string
	for _, f := range r.Filters {
		if strings.HasPrefix(f, "!") {
			return nil // an exclusion the guard does not resolve
		}
		for _, dir := range s.match(f, base, candidates) {
			if !slices.Contains(out, dir) {
				out = append(out, dir)
			}
		}
	}
	if r.Manager == "npm" && r.WorkspaceRoot && !slices.Contains(out, root) {
		out = append(out, root)
	}
	return out
}

// match returns the candidates a selector names: a package name or name
// pattern, or a package directory. Selectors with dependency or
// change-based forms are not resolved.
func (s *scripts) match(f, base string, candidates []string) []string {
	if strings.HasPrefix(f, "^") || strings.Contains(f, "...") || strings.Contains(f, "[") {
		return nil
	}
	if strings.HasPrefix(f, "{") && strings.HasSuffix(f, "}") {
		f = "./" + strings.Trim(f, "{}")
	}
	var out []string
	if !strings.HasPrefix(f, ".") {
		for _, dir := range candidates {
			if m := s.manifest(dir); m != nil && m.Name != "" {
				if ok, _ := path.Match(f, m.Name); ok {
					out = append(out, dir)
				}
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	dir := filepath.Join(base, filepath.FromSlash(f))
	if slices.Contains(candidates, dir) {
		return []string{dir}
	}
	return nil
}

// nearest returns the closest directory from dir up that holds a
// package.json, where package managers run a script.
func (s *scripts) nearest(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if s.manifest(d) != nil {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// workspaceRoot returns the workspace root above base: the directory of
// pnpm-workspace.yaml for pnpm, or of a package.json that declares
// workspaces for the others.
func (s *scripts) workspaceRoot(manager, base string) string {
	if manager == "pnpm" {
		return s.pnpmRoot(base)
	}
	for d := base; ; d = filepath.Dir(d) {
		if m := s.manifest(d); m != nil && len(workspacePatterns(m)) > 0 {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

func (s *scripts) pnpmRoot(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "pnpm-workspace.yaml")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// workspacePackages returns the package directories the workspace at root
// declares.
func (s *scripts) workspacePackages(manager, root string) []string {
	var patterns []string
	if manager == "pnpm" {
		var w struct {
			Packages []string `yaml:"packages"`
		}
		if data, err := os.ReadFile(filepath.Join(root, "pnpm-workspace.yaml")); err == nil {
			_ = yaml.Unmarshal(data, &w)
		}
		patterns = w.Packages
	} else if m := s.manifest(root); m != nil {
		patterns = workspacePatterns(m)
	}
	var dirs, excluded []string
	for _, p := range patterns {
		if rest, ok := strings.CutPrefix(p, "!"); ok {
			excluded = append(excluded, globDirs(root, rest)...)
			continue
		}
		for _, d := range globDirs(root, p) {
			if !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
	}
	return slices.DeleteFunc(dirs, func(d string) bool { return slices.Contains(excluded, d) })
}

// workspacePatterns reads the workspaces field: a list, or an object with
// a packages list.
func workspacePatterns(m *manifest) []string {
	var list []string
	if json.Unmarshal(m.Workspaces, &list) == nil {
		return list
	}
	var obj struct {
		Packages []string `json:"packages"`
	}
	_ = json.Unmarshal(m.Workspaces, &obj)
	return obj.Packages
}

// globDirs returns the directories under root that match a workspace
// pattern, where * matches within one path element and ** any number of
// them, and that hold a package.json. node_modules and hidden directories
// are not searched.
func globDirs(root, pattern string) []string {
	pattern = strings.Trim(path.Clean(filepath.ToSlash(pattern)), "/")
	if pattern == "." || pattern == "" {
		return nil
	}
	segs := strings.Split(pattern, "/")
	var out []string
	var walk func(dir string, i int)
	walk = func(dir string, i int) {
		if i == len(segs) {
			if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil && !slices.Contains(out, dir) {
				out = append(out, dir)
			}
			return
		}
		seg := segs[i]
		if !strings.ContainsAny(seg, "*?[") {
			walk(filepath.Join(dir, seg), i+1)
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		if seg == "**" {
			walk(dir, i+1)
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || name == "node_modules" || strings.HasPrefix(name, ".") {
				continue
			}
			if seg == "**" {
				walk(filepath.Join(dir, name), i)
			} else if ok, _ := path.Match(seg, name); ok {
				walk(filepath.Join(dir, name), i+1)
			}
		}
	}
	walk(root, 0)
	return out
}

// manifest reads the package.json in dir, or nil when there is none.
func (s *scripts) manifest(dir string) *manifest {
	if m, ok := s.manifests[dir]; ok {
		return m
	}
	var m *manifest
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		m = &manifest{}
		if json.Unmarshal(data, m) != nil {
			m = &manifest{}
		}
	}
	s.manifests[dir] = m
	return m
}

// yarnMajor returns the major version of the Yarn the project pins in
// packageManager, or, without one, 2 when .yarnrc.yml configures a later
// Yarn and 1 otherwise.
func (s *scripts) yarnMajor(dir string) int {
	for d := dir; ; d = filepath.Dir(d) {
		if m := s.manifest(d); m != nil {
			if v, ok := strings.CutPrefix(m.PackageManager, "yarn@"); ok {
				major, _, _ := strings.Cut(v, ".")
				if n, err := strconv.Atoi(major); err == nil {
					return n
				}
			}
		}
		if _, err := os.Stat(filepath.Join(d, ".yarnrc.yml")); err == nil {
			return 2
		}
		if filepath.Dir(d) == d {
			return 1
		}
	}
}

// pnpmSetting reads a boolean pnpm setting from the pnpm-workspace.yaml at
// root ("" for none), or else as npmrcSetting finds it from dir, with its
// default.
func pnpmSetting(root, dir, yamlKey, npmrcKey string, def bool) bool {
	if root != "" {
		if data, err := os.ReadFile(filepath.Join(root, "pnpm-workspace.yaml")); err == nil {
			var settings map[string]any
			if yaml.Unmarshal(data, &settings) == nil {
				if v, ok := settings[yamlKey].(bool); ok {
					return v
				}
			}
		}
	}
	if v, ok := npmrcSetting(dir, npmrcKey); ok {
		return v
	}
	return def
}

// npmrcSetting reads a boolean npm-style setting: from its npm_config_
// environment variable, or else from the first .npmrc that sets it, from
// dir up and then in the home directory.
func npmrcSetting(dir, key string) (value, ok bool) {
	if v, set := os.LookupEnv("npm_config_" + strings.ReplaceAll(key, "-", "_")); set {
		b, err := strconv.ParseBool(v)
		return b, err == nil
	}
	var files []string
	for d := dir; d != ""; d = filepath.Dir(d) {
		files = append(files, filepath.Join(d, ".npmrc"))
		if filepath.Dir(d) == d {
			break
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".npmrc"))
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			k, v, cut := strings.Cut(line, "=")
			if cut && strings.TrimSpace(k) == key {
				b, err := strconv.ParseBool(strings.TrimSpace(v))
				return b, err == nil
			}
		}
	}
	return false, false
}
