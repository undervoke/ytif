package checkrun

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/undervoke/ytif/internal/shellcmd"
)

type nxObject map[string]json.RawMessage

var nxArgTemplate = regexp.MustCompile(`\{args\.[^}]+\}`)

type nxProject struct {
	name, dir, manager string
	targets            map[string]nxObject
	dependencies       []string
}

// Only explicit metadata is followed. Plugins, import-inferred edges and
// computed options remain the project owner's knowledge.
type nxInspector struct {
	root           string
	callback       func(string, string) string
	projects       map[string]*nxProject
	defaults       map[string]nxObject
	defaultProject string
}

func newNxInspector(root string, inspect func(string, string) string) *nxInspector {
	return &nxInspector{root: root, callback: inspect}
}
func (n *nxInspector) inspect(c shellcmd.Command, dir string) string {
	if len(c.Args) > 2 && c.Literal[1] && c.Args[1] == "exec" {
		for j := 2; j < len(c.Args); j++ {
			if c.Literal[j] && c.Args[j] == "--" {
				for k := j + 1; k < len(c.Args); k++ {
					if !c.Literal[k] {
						return ""
					}
				}
				// Nx exec preserves argument boundaries but may change to a
				// selected project's cwd. Only directory-independent facts apply.
				return n.callback(commandText(c.Args[j+1:]), "")
			}
		}
		return ""
	}
	var positional, targets, projects, excluded, forwarded []string
	unknownOverrides := false
	config := ""
	for i := 1; i < len(c.Args); i++ {
		if !c.Literal[i] {
			return ""
		}
		a := c.Args[i]
		if a == "--" {
			for j := i + 1; j < len(c.Args); j++ {
				if !c.Literal[j] {
					return ""
				}
			}
			forwarded = append(forwarded, c.Args[i+1:]...)
			break
		}
		if a == "--help" || a == "-h" || a == "--version" {
			return ""
		}
		if !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
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
					if len(positional) > 0 && positional[0] != "run-many" && positional[0] != "affected" {
						break
					}
				}
			}
			switch flag {
			case "-t", "--target", "--targets":
				targets = append(targets, words...)
			case "--exclude":
				excluded = append(excluded, words...)
			default:
				projects = append(projects, words...)
			}
		case "-c", "--configuration", "--parallel", "--maxParallel", "--max-parallel", "--runner", "--outputStyle", "--output-style", "--graph", "--tuiAutoExit", "--tui-auto-exit", "--base", "--head", "--files":
			if !inline {
				if i+1 >= len(c.Args) || !c.Literal[i+1] {
					return ""
				}
				i++
				value = c.Args[i]
			}
			if flag == "-c" || flag == "--configuration" {
				config = value
			}
		case "--noEmit":
			forwarded = append(forwarded, a)
			if !inline && i+1 < len(c.Args) && c.Literal[i+1] && (c.Args[i+1] == "true" || c.Args[i+1] == "false") {
				i++
				forwarded = append(forwarded, c.Args[i])
			}
		case "--skipNxCache", "--skip-nx-cache", "--skipRemoteCache", "--skip-remote-cache", "--verbose", "--nxBail", "--nx-bail", "--nxIgnoreCycles", "--nx-ignore-cycles", "--all", "--uncommitted", "--untracked":
		default:
			unknownOverrides = true
		}
	}
	if len(positional) == 0 {
		return ""
	}
	switch positional[0] {
	case "show", "graph", "list", "report", "help", "exec", "watch", "sync", "sync:check":
		return ""
	case "run":
		if len(positional) > 1 {
			parts := strings.Split(positional[1], ":")
			if len(parts) < 2 || len(parts) > 3 {
				return ""
			}
			projects, targets = []string{parts[0]}, []string{parts[1]}
			if len(parts) == 3 && config == "" {
				config = parts[2]
			}
		}
	case "run-many", "affected":
	default:
		targets = []string{positional[0]}
		if len(positional) > 1 {
			projects = []string{positional[1]}
		}
	}
	if len(targets) == 0 {
		return ""
	}
	n.load()
	defaultProject := n.projectAt(dir)
	var selected []*nxProject
	for _, p := range n.uniqueProjects() {
		include := len(projects) == 0 && (positional[0] == "run-many" || positional[0] == "affected")
		for _, pattern := range projects {
			include = include || n.matchesProject(p, pattern)
		}
		if len(projects) == 0 && positional[0] != "run-many" && positional[0] != "affected" {
			include = p == defaultProject
		}
		for _, pattern := range excluded {
			if n.matchesProject(p, pattern) {
				include = false
			}
		}
		if include {
			selected = append(selected, p)
		}
	}
	args := commandText(forwarded)
	if unknownOverrides {
		args += " ${YTIF_UNRESOLVED_ARG}"
	}
	seen := map[string]bool{}
	for _, p := range selected {
		for _, t := range targets {
			if hit := n.target(p, t, config, args, seen, 0); hit != "" {
				return hit
			}
		}
	}
	return ""
}
func (n *nxInspector) target(p *nxProject, name, config, forwarded string, seen map[string]bool, depth int) string {
	key := p.name + "\x00" + name + "\x00" + config
	if depth >= 16 || len(seen) >= 128 || seen[key] {
		return ""
	}
	seen[key] = true
	if taskWork.MatchString(name) {
		return "nx " + p.name + ":" + name
	}
	t, exists := p.targets[name]
	if !exists {
		return ""
	}
	executor := nxString(t["executor"])
	defaults := n.defaults[name]
	if d, ok := n.defaults[executor]; executor != "" && ok {
		defaults = d
	}
	t = mergeNx(defaults, t)
	executor = nxString(t["executor"])
	executorTask := executor[strings.LastIndex(executor, ":")+1:]
	if taskWork.MatchString(executorTask) || slices.Contains([]string{"@nx/jest:jest", "@nx/vite:vitest", "@nx/playwright:playwright", "@nx/js:tsc", "@nx/webpack:webpack", "@nx/vite:build"}, executor) {
		return "nx " + p.name + ":" + name + " uses " + executor
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
	var deps []json.RawMessage
	_ = json.Unmarshal(t["dependsOn"], &deps)
	for _, raw := range deps {
		dep := nxString(raw)
		owners := []*nxProject{p}
		if dep != "" {
			if strings.HasPrefix(dep, "^") {
				dep = strings.TrimPrefix(dep, "^")
				owners = n.dependencies(p)
			}
		} else {
			d := nxMap(raw)
			dep = nxString(d["target"])
			var dependencies bool
			_ = json.Unmarshal(d["dependencies"], &dependencies)
			if dependencies {
				owners = n.dependencies(p)
			}
			if selection, ok := d["projects"]; ok {
				var names []string
				if one := nxString(selection); one != "" {
					names = []string{one}
				} else {
					_ = json.Unmarshal(selection, &names)
				}
				owners = nil
				for _, s := range names {
					switch s {
					case "self":
						owners = append(owners, p)
					case "{dependencies}":
						owners = append(owners, n.dependencies(p)...)
					default:
						for _, q := range n.uniqueProjects() {
							if n.matchesProject(q, s) {
								owners = append(owners, q)
							}
						}
					}
				}
			}
		}
		if taskWork.MatchString(dep) {
			return fmt.Sprintf("nx %s:%s depends on %s", p.name, name, dep)
		}
		for _, q := range owners {
			for _, target := range nxKeys(q.targets) {
				if nxMatch(dep, target) {
					dependencyConfig := config
					if _, ok := nxMap(q.targets[target]["configurations"])[config]; !ok {
						dependencyConfig = ""
					}
					if hit := n.target(q, target, dependencyConfig, "", seen, depth+1); hit != "" {
						return hit
					}
				}
			}
		}
	}
	if executor == "nx:run-script" {
		script := nxString(options["script"])
		if script == "" {
			script = name
		}
		command := commandText([]string{p.manager, "run", script})
		if forwarded != "" {
			command += " -- " + forwarded
		}
		return n.callback(command, p.dir)
	}
	if executor != "" && executor != "nx:run-commands" {
		return ""
	}
	cwd := n.root
	if dir := nxString(options["cwd"]); dir != "" {
		dir = strings.ReplaceAll(dir, "{workspaceRoot}", n.root)
		dir = strings.ReplaceAll(dir, "{projectRoot}", p.dir)
		if strings.Contains(dir, "{") {
			cwd = ""
		} else {
			cwd = workingDir(n.root, dir)
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
	for _, spec := range commands {
		command := spec.text
		templated := strings.Contains(command, "{args.")
		// Keep unknown operands in position for the static recognizers.
		command = nxArgTemplate.ReplaceAllLiteralString(command, "${YTIF_UNRESOLVED_ARG}")
		command = strings.ReplaceAll(command, "{workspaceRoot}", n.root)
		command = strings.ReplaceAll(command, "{projectRoot}", p.dir)
		args := nxString(options["args"])
		if args == "" {
			var words []string
			if json.Unmarshal(options["args"], &words) == nil {
				args = strings.Join(words, " ")
			}
		}
		if args != "" && spec.forward && !templated {
			command += " " + args
		}
		if forwarded != "" && spec.forward && !templated {
			command += " " + forwarded
		}
		if hit := n.callback(command, cwd); hit != "" {
			return hit
		}
	}
	return ""
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
		_ = json.Unmarshal(mergeNx(meta, project)["implicitDependencies"], &p.dependencies)
		for _, field := range []string{"dependencies", "devDependencies", "peerDependencies"} {
			for name := range nxMap(pkg[field]) {
				p.dependencies = append(p.dependencies, name)
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
func (n *nxInspector) dependencies(p *nxProject) []*nxProject {
	var out []*nxProject
	for _, q := range n.uniqueProjects() {
		include := false
		for _, pattern := range p.dependencies {
			if strings.HasPrefix(pattern, "!") {
				continue
			}
			include = include || n.matchesProject(q, pattern)
		}
		for _, pattern := range p.dependencies {
			if rest, ok := strings.CutPrefix(pattern, "!"); ok && n.matchesProject(q, rest) {
				include = false
			}
		}
		if include {
			out = append(out, q)
		}
	}
	return out
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
