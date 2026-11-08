// Nx static resolution for the agent guard.
//
// The inspector reads the explicit project metadata the repository already
// maintains and resolves what an Nx command runs without starting Nx: the
// selected projects and targets, each target's single literal body or its
// cacheable own elapsed, and the configuration and forwarded arguments the
// command carries. Plugins, import-inferred edges, computed options, and
// dependsOn work stay the project owner's knowledge and are never priced.
// `nx affected` stays unknown.
package guard

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/undervoke/ytif/internal/shellcmd"
	"mvdan.cc/sh/v3/syntax"
)

type nxObject map[string]json.RawMessage

var nxArgTemplate = regexp.MustCompile(`\{args\.[^}]+\}`)

type nxProject struct {
	name, dir, manager string
	targets            map[string]nxObject
}

// Only explicit metadata is followed. Plugins, import-inferred edges and
// computed options remain the project owner's knowledge.
type nxInspector struct {
	root           string
	projects       map[string]*nxProject
	defaults       map[string]nxObject
	defaultProject string
}

func newNxInspector(root string) *nxInspector {
	return &nxInspector{root: root}
}

// NxSelection is one priced project:target from an Nx command.
//
// The records key an Nx verification the way the nxtarget source does: by the
// project root and the target name. A selection carries that key so the guard
// reads the target's own elapsed before it reads the body the target runs.
type NxSelection struct {
	Project   string
	Target    string
	Root      string // project directory, repository-relative, slash-separated
	Cacheable bool   // the target may stand cached, so its body may not run
	Body      string // single literal body command text ("" when not resolvable)
	BodyDir   string // cwd for the body ("" when unknown)
}

// Key returns the verification key the records carry for this target.
func (s NxSelection) Key() string { return "nx-target:" + s.Root + ":" + s.Target }

// Label names the selection in a refusal record.
func (s NxSelection) Label() string { return s.Project + ":" + s.Target }

type nxParsed struct {
	positional       []string
	targets          []string
	projects         []string
	excluded         []string
	forwarded        []string
	config           string
	unknownOverrides bool
	execForm         bool
	execCommand      string
}

// Select resolves an Nx command to its priced selections: every selected
// project:target with an exact target, or nothing when the command cannot
// be resolved statically. `nx affected` is always unknown.
func (n *nxInspector) Select(c shellcmd.Command, dir string) ([]NxSelection, bool) {
	p, ok := parseNxCommand(c)
	if !ok {
		return nil, false
	}
	if p.execForm {
		if p.execCommand == "" {
			return nil, false
		}
		return []NxSelection{{Body: p.execCommand}}, true
	}
	targets, projects, config := p.targets, p.projects, p.config
	switch {
	case len(p.positional) > 0 && p.positional[0] == "run":
		if len(p.positional) < 2 {
			return nil, false
		}
		projName, rest, found := strings.Cut(p.positional[1], ":")
		if !found || projName == "" || rest == "" {
			return nil, false
		}
		n.load()
		known, present := n.projects[projName]
		if !present || known == nil {
			return nil, false
		}
		target, inline := rest, ""
		if strings.Contains(rest, ":") {
			if _, exact := known.targets[rest]; exact {
				// Exact colon target; no configuration split.
			} else if i := strings.LastIndex(rest, ":"); i > 0 && i+1 < len(rest) {
				cand, cfg := rest[:i], rest[i+1:]
				if _, ok := known.targets[cand]; !ok {
					return nil, false
				}
				target, inline = cand, cfg
			} else {
				return nil, false
			}
		}
		projects, targets = []string{projName}, []string{target}
		if config == "" && inline != "" {
			config = inline
		}
	case len(p.positional) > 0 && (p.positional[0] == "run-many" || p.positional[0] == "affected"):
		if p.positional[0] == "affected" {
			return nil, false
		}
	case len(p.positional) > 0:
		targets = []string{p.positional[0]}
		if len(p.positional) > 1 {
			projects = []string{p.positional[1]}
		}
	}
	if len(targets) == 0 {
		return nil, false
	}
	if p.unknownOverrides {
		return nil, true
	}
	n.load()
	var selected []*nxProject
	for _, pr := range n.uniqueProjects() {
		include := len(projects) == 0 && len(p.positional) > 0 && p.positional[0] == "run-many"
		for _, pattern := range projects {
			include = include || n.matchesProject(pr, pattern)
		}
		if len(projects) == 0 && !(len(p.positional) > 0 && p.positional[0] == "run-many") {
			include = pr == n.projectAt(dir)
		}
		for _, pattern := range p.excluded {
			if n.matchesProject(pr, pattern) {
				include = false
			}
		}
		if include {
			selected = append(selected, pr)
		}
	}
	var out []NxSelection
	for _, pr := range selected {
		for _, t := range targets {
			sel, ok := n.resolveTarget(pr, t, config, nxCommandText(p.forwarded))
			if !ok {
				continue
			}
			out = append(out, sel)
		}
	}
	return out, true
}

// parseNxCommand reads the selection an Nx command names: positional words,
// targets, projects, exclusions, forwarded words after `--`, and the
// configuration. A non-literal word fails the parse. Unknown flags set
// unknownOverrides.
func parseNxCommand(c shellcmd.Command) (nxParsed, bool) {
	var p nxParsed
	if len(c.Args) > 2 && c.Literal[1] && c.Args[1] == "exec" {
		for j := 2; j < len(c.Args); j++ {
			if c.Literal[j] && c.Args[j] == "--" {
				for k := j + 1; k < len(c.Args); k++ {
					if !c.Literal[k] {
						return nxParsed{}, false
					}
				}
				// Nx exec may change to a selected project's cwd; only
				// directory-independent facts apply, so the body keeps an
				// unknown directory.
				p.execForm = true
				p.execCommand = nxCommandText(c.Args[j+1:])
				return p, true
			}
		}
		return nxParsed{}, false
	}
	for i := 1; i < len(c.Args); i++ {
		if !c.Literal[i] {
			return nxParsed{}, false
		}
		a := c.Args[i]
		if a == "--" {
			for j := i + 1; j < len(c.Args); j++ {
				if !c.Literal[j] {
					return nxParsed{}, false
				}
			}
			p.forwarded = append(p.forwarded, c.Args[i+1:]...)
			break
		}
		if a == "--help" || a == "-h" || a == "--version" {
			return nxParsed{}, false
		}
		if !strings.HasPrefix(a, "-") {
			p.positional = append(p.positional, a)
			continue
		}
		flag, value, inline := strings.Cut(a, "=")
		switch flag {
		case "-t", "--target", "--targets", "-p", "--project", "--projects", "--exclude":
			var words []string
			if inline {
				words = strings.Split(value, ",")
			} else {
				for i+1 < len(c.Args) && c.Literal[i+1] && !strings.HasPrefix(c.Args[i+1], "-") {
					i++
					words = append(words, strings.Split(c.Args[i], ",")...)
					if len(p.positional) > 0 && p.positional[0] != "run-many" && p.positional[0] != "affected" {
						break
					}
				}
			}
			switch flag {
			case "-t", "--target", "--targets":
				p.targets = append(p.targets, words...)
			case "--exclude":
				p.excluded = append(p.excluded, words...)
			default:
				p.projects = append(p.projects, words...)
			}
		case "--parallel":
			if !inline && i+1 < len(c.Args) && c.Literal[i+1] {
				next := c.Args[i+1]
				if next == "true" || next == "false" || isDigits(next) || strings.HasSuffix(next, "%") {
					i++
				}
			}
		case "-c", "--configuration", "--maxParallel", "--max-parallel", "--runner", "--outputStyle", "--output-style", "--graph", "--tuiAutoExit", "--tui-auto-exit", "--base", "--head", "--files":
			if !inline {
				if i+1 >= len(c.Args) || !c.Literal[i+1] {
					return nxParsed{}, false
				}
				i++
				value = c.Args[i]
			}
			if flag == "-c" || flag == "--configuration" {
				p.config = value
			}
		case "--noEmit":
			p.forwarded = append(p.forwarded, a)
			if !inline && i+1 < len(c.Args) && c.Literal[i+1] && (c.Args[i+1] == "true" || c.Args[i+1] == "false") {
				i++
				p.forwarded = append(p.forwarded, c.Args[i])
			}
		case "--skipNxCache", "--skip-nx-cache", "--skipRemoteCache", "--skip-remote-cache", "--verbose", "--nxBail", "--nx-bail", "--nxIgnoreCycles", "--nx-ignore-cycles", "--all", "--uncommitted", "--untracked":
		default:
			p.unknownOverrides = true
		}
	}
	if len(p.positional) == 0 {
		return nxParsed{}, false
	}
	switch p.positional[0] {
	case "show", "graph", "list", "report", "help", "exec", "watch", "sync", "sync:check":
		return nxParsed{}, false
	}
	return p, true
}

// resolveTarget resolves one exact target in one project to a priced
// selection. dependsOn work is never priced. A cacheable target prices its
// own elapsed; otherwise only a single literal body prices.
func (n *nxInspector) resolveTarget(pr *nxProject, name, config, forwarded string) (NxSelection, bool) {
	t, exists := pr.targets[name]
	if !exists {
		return NxSelection{}, false
	}
	sel := NxSelection{Project: pr.name, Target: name, Root: relDir(pr.dir, n.root)}
	executor := nxString(t["executor"])
	defaults := n.defaults[name]
	if d, ok := n.defaults[executor]; executor != "" && ok {
		defaults = d
	}
	t = mergeNx(defaults, t)
	executor = nxString(t["executor"])
	if nxBool(t["cache"]) {
		sel.Cacheable = true
		return sel, true
	}
	if config == "" {
		config = nxString(t["defaultConfiguration"])
	}
	options := nxMap(t["options"])
	if config != "" {
		configs := nxMap(t["configurations"])
		if branch, ok := configs[config]; ok {
			options = mergeNx(options, nxMap(branch))
		}
	}
	if executor == "nx:run-script" {
		script := nxString(options["script"])
		if script == "" {
			script = name
		}
		body := nxCommandText([]string{pr.manager, "run", script})
		if forwarded != "" {
			body += " -- " + forwarded
		}
		sel.Body, sel.BodyDir = body, pr.dir
		return sel, true
	}
	if executor != "" && executor != "nx:run-commands" {
		return NxSelection{}, false
	}
	cwd := n.root
	if dir := nxString(options["cwd"]); dir != "" {
		dir = strings.ReplaceAll(dir, "{workspaceRoot}", n.root)
		dir = strings.ReplaceAll(dir, "{projectRoot}", pr.dir)
		if strings.Contains(dir, "{") {
			cwd = ""
		} else {
			cwd = nxWorkingDir(n.root, dir)
		}
	}
	type commandSpec struct {
		text    string
		forward bool
	}
	forward := string(options["forwardAllArgs"]) != "false"
	var commands []commandSpec
	if command := nxString(t["command"]); command != "" {
		commands = append(commands, commandSpec{command, forward})
	}
	if command := nxString(options["command"]); command != "" {
		commands = []commandSpec{{command, forward}}
	}
	if raw, ok := options["commands"]; ok {
		commands = nil
		var list []json.RawMessage
		_ = json.Unmarshal(raw, &list)
		for _, raw := range list {
			command, pass := nxString(raw), forward
			if command == "" {
				item := nxMap(raw)
				command = nxString(item["command"])
				if v, ok := item["forwardAllArgs"]; ok {
					pass = string(v) != "false"
				}
			}
			if command != "" {
				commands = append(commands, commandSpec{command, pass})
			}
		}
	}
	if len(commands) != 1 {
		return NxSelection{}, false
	}
	spec := commands[0]
	body := spec.text
	templated := strings.Contains(body, "{args.")
	body = regexpReplaceAllLiteralTemplate(body)
	body = strings.ReplaceAll(body, "{workspaceRoot}", n.root)
	body = strings.ReplaceAll(body, "{projectRoot}", pr.dir)
	args := nxString(options["args"])
	if args == "" {
		var words []string
		if json.Unmarshal(options["args"], &words) == nil {
			args = strings.Join(words, " ")
		}
	}
	if strings.Contains(body, "${YTIF_UNRESOLVED_ARG}") {
		return NxSelection{}, false
	}
	if args != "" && spec.forward && !templated {
		body += " " + args
	}
	if forwarded != "" && spec.forward && !templated {
		body += " " + forwarded
	}
	if strings.Contains(body, "{") {
		return NxSelection{}, false
	}
	sel.Body, sel.BodyDir = body, cwd
	return sel, true
}

func (n *nxInspector) load() {
	if n.projects != nil {
		return
	}
	n.projects = map[string]*nxProject{}
	n.defaults = map[string]nxObject{}
	nx := nxFile(filepath.Join(n.root, "nx.json"))
	if nx == nil {
		return
	}
	_ = json.Unmarshal(nx["targetDefaults"], &n.defaults)
	n.defaultProject = nxString(nxMap(nx["cli"])["defaultProjectName"])
	if n.defaultProject == "" {
		n.defaultProject = nxString(nx["defaultProject"])
	}
	manager := strings.SplitN(nxString(nxFile(filepath.Join(n.root, "package.json"))["packageManager"]), "@", 2)[0]
	if manager == "" {
		manager = "npm"
	}
	_ = filepath.WalkDir(n.root, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if file != n.root && (strings.HasPrefix(d.Name(), ".") || slices.Contains([]string{"node_modules", "vendor", "dist", "bin", "obj", "build", "coverage"}, d.Name())) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "project.json" && d.Name() != "package.json" {
			return nil
		}
		dir := filepath.Dir(file)
		pkg := nxFile(filepath.Join(dir, "package.json"))
		project := nxFile(filepath.Join(dir, "project.json"))
		meta := nxMap(pkg["nx"])
		if project == nil && meta == nil {
			return nil
		}
		name := nxString(project["name"])
		if name == "" {
			name = nxString(meta["name"])
		}
		if name == "" {
			name = nxString(pkg["name"])
		}
		if name == "" {
			name = filepath.Base(dir)
		}
		if _, ok := n.projects[name]; ok {
			return nil
		}
		p := &nxProject{name: name, dir: dir, manager: manager, targets: map[string]nxObject{}}
		var scripts map[string]string
		_ = json.Unmarshal(pkg["scripts"], &scripts)
		var included []string
		_ = json.Unmarshal(meta["includedScripts"], &included)
		for name := range scripts {
			if _, set := meta["includedScripts"]; set && !slices.Contains(included, name) {
				continue
			}
			options, _ := json.Marshal(map[string]string{"script": name})
			p.targets[name] = nxObject{"executor": json.RawMessage(`"nx:run-script"`), "options": options}
		}
		for _, object := range []nxObject{meta, project} {
			for name, raw := range nxMap(object["targets"]) {
				p.targets[name] = mergeNx(p.targets[name], nxMap(raw))
			}
		}
		n.projects[p.name] = p
		if pkgName := nxString(pkg["name"]); pkgName != "" {
			n.projects[pkgName] = p
		}
		return nil
	})
}

func (n *nxInspector) projectAt(dir string) *nxProject {
	var best *nxProject
	for _, p := range n.uniqueProjects() {
		rel, err := filepath.Rel(p.dir, dir)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && (best == nil || len(p.dir) > len(best.dir)) {
			best = p
		}
	}
	if best != nil && filepath.Clean(best.dir) != filepath.Clean(n.root) {
		return best
	}
	if name := os.Getenv("NX_DEFAULT_PROJECT"); name != "" {
		return n.projects[name]
	}
	if best != nil {
		return best
	}
	return n.projects[n.defaultProject]
}

func (n *nxInspector) uniqueProjects() []*nxProject {
	var out []*nxProject
	for _, name := range nxKeys(n.projects) {
		p := n.projects[name]
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

func (n *nxInspector) matchesProject(p *nxProject, pattern string) bool {
	for name, q := range n.projects {
		if q == p && nxMatch(pattern, name) {
			return true
		}
	}
	return false
}

func nxFile(path string) nxObject {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return nxMap(data)
}

func nxMap(raw json.RawMessage) nxObject {
	var m nxObject
	_ = json.Unmarshal(raw, &m)
	return m
}

func nxString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func nxBool(raw json.RawMessage) bool {
	var b bool
	_ = json.Unmarshal(raw, &b)
	return b
}

func nxKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func nxMatch(pattern, name string) bool {
	if pattern == name || pattern == "*" {
		return true
	}
	ok, _ := filepath.Match(pattern, name)
	return ok
}

func mergeNx(base, override nxObject) nxObject {
	out := nxObject{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		if (k == "options" || k == "configurations") && nxMap(v) != nil {
			v, _ = json.Marshal(mergeNx(nxMap(out[k]), nxMap(v)))
		}
		out[k] = v
	}
	return out
}

func nxWorkingDir(root, dir string) string {
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Join(root, dir)
}

func nxCommandText(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i], _ = syntax.Quote(a, syntax.LangBash)
	}
	return strings.Join(quoted, " ")
}

func regexpReplaceAllLiteralTemplate(s string) string {
	return nxArgTemplate.ReplaceAllLiteralString(s, "${YTIF_UNRESOLVED_ARG}")
}
