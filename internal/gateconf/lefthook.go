package gateconf

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/reconcile"
)

// Lefthook reads the first main configuration it finds, extension by
// extension and then name by name; a local configuration overlays it.
var (
	lefthookNames      = []string{"lefthook", ".lefthook", ".config/lefthook"}
	lefthookLocalNames = []string{"lefthook-local", ".lefthook-local", ".config/lefthook-local"}
	lefthookExtensions = []string{".yml", ".yaml", ".json", ".jsonc", ".toml"}
)

// lefthookSettings are top-level keys that configure lefthook and run
// nothing; rc, lefthook, remotes, and extends are read separately, and any
// other key is a hook.
var lefthookSettings = set("min_version", "source_dir", "source_dir_local", "output", "colors", "no_tty",
	"assert_lefthook_installed", "skip_lfs", "no_auto_install", "install_non_git_hooks", "glob_matcher", "templates")

// lefthookInert lists, per kind of mapping, the options that run nothing.
// An option outside these lists and the ones read for execution fails,
// since ytif cannot tell whether it runs code.
var lefthookInert = map[string]map[string]bool{
	"hook":    set("parallel", "piped", "follow", "fail_on_changes", "fail_on_changes_diff", "exclude_tags", "exclude"),
	"command": set("root", "fail_text", "timeout", "tags", "file_types", "glob", "exclude", "env", "priority", "interactive", "use_stdin", "stage_fixed"),
	"script":  set("runner", "args", "tags", "env", "priority", "fail_text", "timeout", "interactive", "use_stdin", "stage_fixed"),
	"job":     set("name", "root", "runner", "args", "fail_text", "timeout", "glob", "exclude", "tags", "file_types", "env", "interactive", "use_stdin", "stage_fixed"),
	"group":   set("root", "parallel", "piped"),
}

func lefthookEntries(repo check.Repo) ([]entry, []reconcile.Finding) {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(repo.Root, filepath.FromSlash(name)))
		return err == nil
	}
	r := &lefthookReader{}
	for _, ext := range lefthookExtensions {
		for _, name := range lefthookLocalNames {
			if exists(name + ext) {
				r.fail(name+ext, "a local lefthook configuration changes hooks outside the tracked configuration")
			}
		}
	}
	for _, ext := range lefthookExtensions {
		for _, name := range lefthookNames {
			if r.file == "" && exists(name+ext) {
				r.file = name + ext
			}
		}
	}
	if r.file == "" {
		return nil, r.findings
	}
	if !isYAML(r.file) {
		r.fail(r.file, "lefthook reads this configuration, whose format ytif cannot reconcile; use YAML")
		return nil, r.findings
	}
	data, err := os.ReadFile(filepath.Join(repo.Root, filepath.FromSlash(r.file)))
	if err != nil {
		r.fail(r.file, err.Error())
		return nil, r.findings
	}
	// Decoding into plain values resolves anchors and merge keys as lefthook does.
	var top map[string]any
	if err := yaml.Unmarshal(data, &top); err != nil {
		r.fail(r.file, "cannot parse: "+err.Error())
		return nil, r.findings
	}
	r.templates = map[string]string{}
	if t, ok := top["templates"].(map[string]any); ok {
		for k, v := range t {
			r.templates[k] = text(v)
		}
	}
	for _, key := range sortedKeys(top) {
		v := top[key]
		switch {
		case key == "remotes" || key == "extends":
			r.fail(r.file+": "+key, "configuration loaded from outside this file cannot be reconciled")
		case key == "rc":
			r.run(r.file+": rc", ". "+shellQuote(text(v)), rootScope, false)
		case key == "lefthook":
			r.run(r.file+": lefthook", text(v), rootScope, false)
		case lefthookSettings[key]:
		default:
			r.hook(key, v)
		}
	}
	return r.entries, r.findings
}

type lefthookReader struct {
	file      string
	templates map[string]string
	entries   []entry
	findings  []reconcile.Finding
}

func (r *lefthookReader) fail(loc, detail string) {
	r.findings = append(r.findings, reconcile.Finding{Kind: reconcile.GateConfig, Location: loc, Detail: detail})
}

// run adds a command line lefthook runs with sh. Templates expand in run,
// args, and setup commands; routing matches the text as written.
func (r *lefthookReader) run(loc, line string, sc scope, expand bool) {
	if strings.TrimSpace(line) == "" {
		return
	}
	script := line
	if expand {
		script = r.expand(line)
	}
	r.entries = append(r.entries, entry{surface: inventory.SurfaceLefthook, location: loc, run: script, route: line, scope: sc})
}

// builtinTemplate matches lefthook's own replacements: file lists, the job
// name, and the hook's arguments.
var builtinTemplate = regexp.MustCompile(`\{(files|staged_files|push_files|all_files|lefthook_job_name|[0-9]+)\}`)

func (r *lefthookReader) expand(line string) string {
	keys := make([]string, 0, len(r.templates))
	for k := range r.templates {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		line = strings.ReplaceAll(line, "{"+k+"}", r.templates[k])
	}
	return builtinTemplate.ReplaceAllString(line, "ytif-template-value")
}

func (r *lefthookReader) mapping(loc string, v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		r.fail(loc, "expected a mapping")
	}
	return m, ok
}

func (r *lefthookReader) hook(name string, v any) {
	m, ok := r.mapping(r.file+": "+name, v)
	if !ok {
		return
	}
	for _, k := range sortedKeys(m) {
		loc := fmt.Sprintf("%s: %s.%s", r.file, name, k)
		switch k {
		case "files":
			r.run(loc, text(m[k]), rootScope, false)
		case "skip", "only":
			r.conditions(loc, m[k], rootScope)
		case "setup":
			for i, s := range list(m[k]) {
				if sm, ok := r.mapping(fmt.Sprintf("%s[%d]", loc, i), s); ok {
					r.run(fmt.Sprintf("%s[%d]", loc, i), text(sm["run"]), rootScope, true)
				}
			}
		case "jobs":
			r.jobs(name+".jobs", m[k], "")
		case "commands":
			if cm, ok := r.mapping(loc, m[k]); ok {
				for _, cmd := range sortedKeys(cm) {
					r.command(loc+"."+cmd, cm[cmd])
				}
			}
		case "scripts":
			if sm, ok := r.mapping(loc, m[k]); ok {
				for _, script := range sortedKeys(sm) {
					r.script(loc+"."+script, script, sm[script])
				}
			}
		default:
			r.option(loc, "hook", k)
		}
	}
}

func (r *lefthookReader) option(loc, kind, key string) {
	if !lefthookInert[kind][key] {
		r.fail(loc, "an option ytif does not know; it cannot tell whether the option runs code")
	}
}

// conditions reads skip and only: a run condition is a command lefthook
// executes.
func (r *lefthookReader) conditions(loc string, v any, sc scope) {
	for i, c := range list(v) {
		if m, ok := c.(map[string]any); ok {
			if run, ok := m["run"]; ok {
				r.run(fmt.Sprintf("%s[%d].run", loc, i), text(run), sc, false)
			}
		}
	}
}

func (r *lefthookReader) command(loc string, v any) {
	m, ok := r.mapping(loc, v)
	if !ok {
		return
	}
	sc := dirScope(text(m["root"]))
	for _, k := range sortedKeys(m) {
		switch k {
		case "run":
			r.run(loc+".run", text(m[k]), sc, true)
		case "files":
			r.run(loc+".files", text(m[k]), sc, false)
		case "skip", "only":
			r.conditions(loc+"."+k, m[k], sc)
		default:
			r.option(loc+"."+k, "command", k)
		}
	}
}

func (r *lefthookReader) script(loc, name string, v any) {
	r.entries = append(r.entries, entry{surface: inventory.SurfaceLefthook, location: loc, repoCode: "runs the repository script " + name})
	m, ok := r.mapping(loc, v)
	if !ok {
		return
	}
	for _, k := range sortedKeys(m) {
		switch k {
		case "skip", "only":
			r.conditions(loc+"."+k, m[k], rootScope)
		default:
			r.option(loc+"."+k, "script", k)
		}
	}
}

func (r *lefthookReader) jobs(prefix string, v any, root string) {
	for i, j := range list(v) {
		loc := fmt.Sprintf("%s: %s[%d]", r.file, prefix, i)
		m, ok := r.mapping(loc, j)
		if !ok {
			continue
		}
		if name := text(m["name"]); name != "" {
			loc = fmt.Sprintf("%s: %s[%s]", r.file, prefix, name)
		}
		dir := firstNonEmpty(text(m["root"]), root)
		sc := dirScope(dir)
		for _, k := range sortedKeys(m) {
			switch k {
			case "run":
				// lefthook runs the job's run and args as one command line
				r.run(loc, strings.TrimSpace(text(m["run"])+" "+text(m["args"])), sc, true)
			case "script":
				r.entries = append(r.entries, entry{surface: inventory.SurfaceLefthook, location: loc, repoCode: "runs the repository script " + text(m[k])})
			case "files":
				r.run(loc+".files", text(m[k]), sc, false)
			case "skip", "only":
				r.conditions(loc+"."+k, m[k], sc)
			case "group":
				g, ok := r.mapping(loc+".group", m[k])
				if !ok {
					continue
				}
				for _, gk := range sortedKeys(g) {
					if gk == "jobs" {
						r.jobs(fmt.Sprintf("%s[%d].group.jobs", prefix, i), g[gk], firstNonEmpty(text(g["root"]), dir))
					} else {
						r.option(loc+".group."+gk, "group", gk)
					}
				}
			default:
				r.option(loc+"."+k, "job", k)
			}
		}
	}
}

// text renders a scalar as lefthook reads it; other values render empty.
func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool, int, int64, uint64, float64:
		return fmt.Sprint(x)
	}
	return ""
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
