// Package initcmd adopts ytif in a repository: it pins this ytif as a Go
// tool, writes the inventory and exemptions, wires the gates into lefthook
// and a workflow, and registers the agent guard with Claude Code, Codex,
// OpenCode, and Pi. What is already there is kept; what is missing is
// added. It reads and checks every file before it writes any, so a file it
// cannot edit stops it with nothing changed.
package initcmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/undervoke/ytif/integrations"
	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gate"
	"github.com/undervoke/ytif/internal/gateconf"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/launch"
)

const (
	guardCommand = "go tool ytif guard"
	workflowFile = ".github/workflows/ytif.yml"
)

// file is one file init leaves as want.
type file struct {
	path   string // relative to the root, slash-separated
	want   []byte
	status string // created, updated, or unchanged
}

// Run adopts ytif in the repository at root with self, the running ytif's
// version, and returns the exit code.
func Run(root, self string, stdout, stderr io.Writer) int {
	pin, err := pinStep(root, self)
	if err != nil {
		fmt.Fprintf(stderr, "ytif init: %v\n", err)
		return gate.ExitUsage
	}
	files, errs := plan(root)
	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintf(stderr, "ytif init: %v\n", err)
		}
		fmt.Fprintln(stderr, "ytif init: nothing was changed")
		return gate.ExitUsage
	}
	if pin.run {
		get := exec.Command("go", "get", "-tool", launch.Command+"@"+self)
		get.Dir, get.Stdout, get.Stderr = root, stderr, stderr
		if err := get.Run(); err != nil {
			fmt.Fprintf(stderr, "ytif init: go get -tool %s@%s: %v\nytif init: nothing was changed\n", launch.Command, self, err)
			return gate.ExitUsage
		}
	}
	fmt.Fprintf(stdout, "%-9s %s\n", pin.status, pin.detail)
	for _, f := range files {
		if f.status != "unchanged" {
			p := filepath.Join(root, filepath.FromSlash(f.path))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				fmt.Fprintf(stderr, "ytif init: %v\n", err)
				return gate.ExitUsage
			}
			if err := os.WriteFile(p, f.want, 0o644); err != nil {
				fmt.Fprintf(stderr, "ytif init: %v\n", err)
				return gate.ExitUsage
			}
		}
		fmt.Fprintf(stdout, "%-9s %s\n", f.status, f.path)
	}
	if !lefthookInstalled(root) {
		fmt.Fprintln(stdout, "\nnote: the git hooks do not run lefthook; run `lefthook install` so commit and push start the gates")
	}
	fmt.Fprintln(stdout, "note: Codex runs .codex/hooks.json once you trust its hooks in /hooks, and Pi loads .pi/extensions only in a trusted project")
	fmt.Fprintln(stdout, "next: stage the changes and run `ytif list` for the checks that still need contracts")
	return gate.ExitPass
}

type pinPlan struct {
	run    bool // go get -tool is needed
	status string
	detail string
}

// pinStep decides how go.mod comes to pin self, refusing a development
// build, a repository without a root go.mod, and a newer pin.
func pinStep(root, self string) (pinPlan, error) {
	if _, ok := parseVersion(self); !ok || strings.Contains(self, "+") {
		return pinPlan{}, fmt.Errorf("this ytif (%s) is not a published version, so go.mod cannot pin it; install a release", self)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return pinPlan{}, errors.New("no go.mod at the repository root; ytif is pinned as a Go tool, which needs a Go module")
	}
	if _, err := exec.LookPath("go"); err != nil {
		return pinPlan{}, errors.New("go is not on PATH; pinning ytif needs Go 1.24 or later")
	}
	pin, ok := launch.Pinned(root)
	if !ok {
		return pinPlan{run: true, status: "updated", detail: "go.mod (ytif " + self + " pinned as a tool)"}, nil
	}
	switch c, ok := compareVersions(pin, self); {
	case !ok:
		return pinPlan{}, fmt.Errorf("go.mod pins ytif %s, which cannot be compared with %s", pin, self)
	case c > 0:
		return pinPlan{}, fmt.Errorf("go.mod pins ytif %s, newer than this ytif %s; run ytif update", pin, self)
	case c < 0:
		return pinPlan{run: true, status: "updated", detail: "go.mod (ytif " + pin + " -> " + self + ")"}, nil
	}
	return pinPlan{status: "unchanged", detail: "go.mod (ytif " + self + ")"}, nil
}

// plan works out every file and collects every reason one cannot be
// edited.
func plan(root string) ([]file, []error) {
	tracked, err := gitx.Tracked(root)
	if err != nil {
		return nil, []error{err}
	}
	wired := gateconf.Wired(check.Repo{Root: root, Tracked: withWorkflows(root, tracked)})
	var files []file
	var errs []error
	add := func(f file, err error) {
		if err != nil {
			errs = append(errs, err)
			return
		}
		files = append(files, f)
	}
	add(skeleton(root, inventory.InventoryFile, "version: 2\nchecks: []\n"))
	add(lefthook(root, wired))
	var actions []string
	if !wired.CI {
		f, err := workflow(root)
		add(f, err)
		actions = workflowActions
	}
	add(exemptions(root, actions))
	for _, p := range []string{".claude/settings.json", ".codex/hooks.json"} {
		add(hookConfig(root, p))
	}
	add(plugin(root, ".opencode/plugins/ytif-guard.ts", integrations.OpenCode))
	add(plugin(root, ".pi/extensions/ytif-guard.ts", integrations.Pi))
	return files, errs
}

// withWorkflows adds the workflows on disk to tracked, so a rerun before the
// new workflow is staged finds it.
func withWorkflows(root string, tracked []string) []string {
	seen := map[string]bool{}
	for _, t := range tracked {
		seen[t] = true
	}
	entries, _ := os.ReadDir(filepath.Join(root, ".github", "workflows"))
	for _, e := range entries {
		p := ".github/workflows/" + e.Name()
		if ext := path.Ext(p); !e.IsDir() && (ext == ".yml" || ext == ".yaml") && !seen[p] {
			tracked = append(tracked, p)
		}
	}
	return tracked
}

// compare reports how want relates to the file at rel.
func compare(root, rel string, want []byte) (file, error) {
	have, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return file{path: rel, want: want, status: "created"}, nil
	case err != nil:
		return file{}, err
	case bytes.Equal(have, want):
		return file{path: rel, want: want, status: "unchanged"}, nil
	}
	return file{path: rel, want: want, status: "updated"}, nil
}

func read(root, rel string) ([]byte, bool, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

// skeleton keeps an existing file and creates a missing one.
func skeleton(root, rel, text string) (file, error) {
	have, ok, err := read(root, rel)
	if err != nil {
		return file{}, err
	}
	if ok {
		return file{path: rel, want: have, status: "unchanged"}, nil
	}
	return file{path: rel, want: []byte(text), status: "created"}, nil
}

// lefthook adds ytif commit to pre-commit and ytif push to pre-push where
// the configuration does not run them.
func lefthook(root string, w gateconf.Wiring) (file, error) {
	rel := w.Lefthook
	if rel == "" {
		rel = "lefthook.yml"
	}
	if w.Commit && w.Push {
		return file{path: rel, status: "unchanged"}, nil
	}
	if ext := path.Ext(rel); ext != ".yml" && ext != ".yaml" {
		return file{}, fmt.Errorf("%s: lefthook reads this file, which is not YAML; ytif can neither edit nor reconcile it", rel)
	}
	data, _, err := read(root, rel)
	if err != nil {
		return file{}, err
	}
	doc, top, err := yamlMapping(data)
	if err != nil {
		return file{}, fmt.Errorf("%s: %w", rel, err)
	}
	if !w.Commit {
		if err := addHook(top, "pre-commit", "ytif-commit", "go tool ytif commit", false); err != nil {
			return file{}, fmt.Errorf("%s: %w", rel, err)
		}
	}
	if !w.Push {
		if err := addHook(top, "pre-push", "ytif-push", "go tool ytif push", true); err != nil {
			return file{}, fmt.Errorf("%s: %w", rel, err)
		}
	}
	out, err := encodeYAML(doc)
	if err != nil {
		return file{}, fmt.Errorf("%s: %w", rel, err)
	}
	return compare(root, rel, out)
}

// addHook adds a job running run to the hook, in its jobs when it has
// them, else in its commands when it has them, else in new jobs. stdin
// passes the hook's standard input, which ytif push reads the refs from.
func addHook(top *yaml.Node, hook, name, run string, stdin bool) error {
	if err := editable(top, "the top level"); err != nil {
		return err
	}
	fields := []*yaml.Node{str("run"), str(run)}
	if stdin {
		fields = append(fields, str("use_stdin"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
	}
	h := lookup(top, hook)
	if err := editable(h, hook); err != nil {
		return err
	}
	switch {
	case h == nil:
		h = &yaml.Node{Kind: yaml.MappingNode}
		top.Content = append(top.Content, str(hook), h)
	case h.Kind == yaml.ScalarNode && h.Tag == "!!null":
		h.Kind, h.Tag, h.Value = yaml.MappingNode, "", ""
	case h.Kind != yaml.MappingNode:
		return fmt.Errorf("%s is not a mapping", hook)
	}
	if jobs := lookup(h, "jobs"); jobs != nil || lookup(h, "commands") == nil {
		if jobs == nil {
			jobs = &yaml.Node{Kind: yaml.SequenceNode}
			h.Content = append(h.Content, str("jobs"), jobs)
		}
		if jobs.Kind != yaml.SequenceNode {
			return fmt.Errorf("%s.jobs is not a list", hook)
		}
		if err := editable(jobs, hook+".jobs"); err != nil {
			return err
		}
		jobs.Style = 0
		jobs.Content = append(jobs.Content, &yaml.Node{Kind: yaml.MappingNode, Content: append([]*yaml.Node{str("name"), str(name)}, fields...)})
		return nil
	}
	cmds := lookup(h, "commands")
	if cmds.Kind != yaml.MappingNode {
		return fmt.Errorf("%s.commands is not a mapping", hook)
	}
	if err := editable(cmds, hook+".commands"); err != nil {
		return err
	}
	if lookup(cmds, name) != nil {
		return fmt.Errorf("%s.commands.%s exists and does not run `%s`", hook, name, run)
	}
	cmds.Content = append(cmds.Content, str(name), &yaml.Node{Kind: yaml.MappingNode, Content: fields})
	return nil
}

const workflowText = `name: ytif

on:
  push:
    branches: [main]
  pull_request:

permissions:
  contents: read

jobs:
  ytif:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
      - name: checks
        run: go tool ytif ci --report github --records ytif-records.jsonl
      - name: records
        if: always()
        uses: actions/upload-artifact@v7
        with:
          name: ytif-records
          path: ytif-records.jsonl
`

// workflowActions are the actions workflowText uses, with the reasons the
// exemptions give them.
var workflowActions = []string{
	"actions/checkout", "Fetches the repository.",
	"actions/setup-go", "Installs the Go toolchain.",
	"actions/upload-artifact", "Uploads the gate records.",
}

// workflow writes a workflow that runs ytif ci, since none does.
func workflow(root string) (file, error) {
	f, err := compare(root, workflowFile, []byte(workflowText))
	if err == nil && f.status == "updated" {
		return file{}, fmt.Errorf("%s exists and does not run ytif ci; add a step running `go tool ytif ci` to a workflow", workflowFile)
	}
	return f, err
}

// exemptions keeps the exemptions and adds the actions, given as pairs of
// action and reason, that it does not list yet.
func exemptions(root string, actions []string) (file, error) {
	rel := inventory.ExemptionsFile
	data, ok, err := read(root, rel)
	if err != nil {
		return file{}, err
	}
	if !ok {
		data = []byte("version: 2\nentries: []\n")
	}
	if len(actions) == 0 {
		return compare(root, rel, data)
	}
	doc, top, err := yamlMapping(data)
	if err != nil {
		return file{}, fmt.Errorf("%s: %w", rel, err)
	}
	if err := editable(top, "the top level"); err != nil {
		return file{}, fmt.Errorf("%s: %w", rel, err)
	}
	entries := lookup(top, "entries")
	if entries == nil {
		entries = &yaml.Node{Kind: yaml.SequenceNode}
		top.Content = append(top.Content, str("entries"), entries)
	}
	if entries.Kind != yaml.SequenceNode {
		return file{}, fmt.Errorf("%s: entries is not a list", rel)
	}
	if err := editable(entries, "entries"); err != nil {
		return file{}, fmt.Errorf("%s: %w", rel, err)
	}
	listed := map[string]bool{}
	for _, e := range entries.Content {
		if merges(e) {
			return file{}, fmt.Errorf("%s: an entry uses a merge key (<<), which ytif cannot read", rel)
		}
		if s, u := lookup(e, "surface"), lookup(e, "uses"); s != nil && u != nil && s.Value == inventory.SurfaceGitHubActions {
			listed[u.Value] = true
		}
	}
	for i := 0; i < len(actions); i += 2 {
		if listed[actions[i]] {
			continue
		}
		entries.Style = 0
		entries.Content = append(entries.Content, &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
			str("surface"), str(inventory.SurfaceGitHubActions), str("uses"), str(actions[i]), str("reason"), str(actions[i+1]),
		}})
	}
	out, err := encodeYAML(doc)
	if err != nil {
		return file{}, fmt.Errorf("%s: %w", rel, err)
	}
	if ok {
		if have, _, _ := read(root, rel); bytes.Equal(normalizeYAML(have), out) {
			return file{path: rel, want: have, status: "unchanged"}, nil
		}
	}
	return compare(root, rel, out)
}

// normalizeYAML re-encodes data as init writes YAML, so a file whose
// content init would not change compares equal.
func normalizeYAML(data []byte) []byte {
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil {
		return nil
	}
	out, _ := encodeYAML(&doc)
	return out
}

// hookConfig registers the guard as a PreToolUse hook in a Claude Code or
// Codex hooks file, whose schemas match, unless a hook already runs it.
func hookConfig(root, rel string) (file, error) {
	data, ok, err := read(root, rel)
	if err != nil {
		return file{}, err
	}
	if !ok {
		data = []byte("{}")
	}
	top, err := parseObject(data)
	if err != nil {
		return file{}, fmt.Errorf("%s: %v; ytif edits plain JSON only", rel, err)
	}
	hooks := &object{vals: map[string]json.RawMessage{}}
	if raw, ok := top.get("hooks"); ok {
		if hooks, err = parseObject(raw); err != nil {
			return file{}, fmt.Errorf("%s: hooks: %v", rel, err)
		}
	}
	var pre []json.RawMessage
	if raw, ok := hooks.get("PreToolUse"); ok {
		if err := json.Unmarshal(raw, &pre); err != nil {
			return file{}, fmt.Errorf("%s: hooks.PreToolUse is not a list", rel)
		}
	}
	for _, m := range pre {
		var group struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Async   bool   `json:"async"`
				If      string `json:"if"`
			} `json:"hooks"`
		}
		if json.Unmarshal(m, &group) != nil || !coversBash(group.Matcher) {
			continue
		}
		for _, h := range group.Hooks {
			if h.Type == "command" && !h.Async && h.If == "" && runsGuard(h.Command) {
				return file{path: rel, want: data, status: "unchanged"}, nil
			}
		}
	}
	type command struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	entry, err := marshal(struct {
		Matcher string    `json:"matcher"`
		Hooks   []command `json:"hooks"`
	}{"Bash", []command{{"command", guardCommand}}})
	if err != nil {
		return file{}, err
	}
	list, err := marshal(append(pre, entry))
	if err != nil {
		return file{}, err
	}
	hooks.set("PreToolUse", list)
	h, err := hooks.MarshalJSON()
	if err != nil {
		return file{}, err
	}
	top.set("hooks", h)
	out, err := top.indent()
	if err != nil {
		return file{}, fmt.Errorf("%s: %w", rel, err)
	}
	return compare(root, rel, out)
}

// coversBash reports whether a PreToolUse matcher selects the Bash tool:
// empty, *, or a pattern matching the whole name.
func coversBash(matcher string) bool {
	if matcher == "" || matcher == "*" {
		return true
	}
	ok, err := regexp.MatchString(`^(?:`+matcher+`)$`, "Bash")
	return err == nil && ok
}

// runsGuard reports whether a hook command is a ytif guard invocation:
// ytif guard, with ytif as a path or not, or go tool ytif guard.
func runsGuard(command string) bool {
	f := strings.Fields(command)
	n := len(f)
	if n < 2 || f[n-1] != "guard" || path.Base(strings.Trim(f[n-2], `"'`)) != "ytif" {
		return false
	}
	return n == 2 || n == 4 && f[0] == "go" && f[1] == "tool"
}

// plugin writes a guard plugin that starts the pinned ytif.
func plugin(root, rel, src string) (file, error) {
	const from, to = `const YTIF = ["ytif"]`, `const YTIF = ["go", "tool", "ytif"]`
	if strings.Count(src, from) != 1 {
		return file{}, fmt.Errorf("%s: the embedded plugin does not declare %s", rel, from)
	}
	return compare(root, rel, []byte(strings.Replace(src, from, to, 1)))
}

// lefthookInstalled reports whether the pre-commit and pre-push git hooks,
// wherever core.hooksPath puts them, run lefthook.
func lefthookInstalled(root string) bool {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--path-format=absolute", "--git-path", "hooks").Output()
	if err != nil {
		return false
	}
	dir := strings.TrimSpace(string(out))
	for _, h := range []string{"pre-commit", "pre-push"} {
		data, err := os.ReadFile(filepath.Join(dir, h))
		if err != nil || !bytes.Contains(data, []byte("lefthook")) {
			return false
		}
	}
	return true
}

func yamlMapping(data []byte) (*yaml.Node, *yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("cannot parse: %w", err)
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, nil, errors.New("the top level is not a mapping")
	}
	return &doc, top, nil
}

func encodeYAML(doc *yaml.Node) ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func lookup(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// editable refuses a node that ytif cannot add to without changing other
// configuration: one that inherits keys through <<, or one an anchor lets
// other places reuse.
func editable(n *yaml.Node, where string) error {
	switch {
	case n == nil:
		return nil
	case merges(n):
		return fmt.Errorf("%s uses a merge key (<<), which ytif cannot edit without changing what it inherits", where)
	case n.Anchor != "":
		return fmt.Errorf("%s has the anchor &%s, which ytif cannot edit without changing what reuses it", where, n.Anchor)
	}
	return nil
}

// merges reports whether the mapping m inherits keys through <<.
func merges(m *yaml.Node) bool {
	if m == nil || m.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i < len(m.Content); i += 2 {
		if m.Content[i].Value == "<<" {
			return true
		}
	}
	return false
}

func str(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

// parseVersion reads vMAJOR.MINOR.PATCH with an optional pre-release.
func parseVersion(v string) ([4]string, bool) {
	rest, ok := strings.CutPrefix(v, "v")
	if !ok {
		return [4]string{}, false
	}
	rest, pre, _ := strings.Cut(rest, "-")
	rest, _, _ = strings.Cut(rest, "+")
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return [4]string{}, false
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return [4]string{}, false
		}
	}
	return [4]string{parts[0], parts[1], parts[2], pre}, true
}

// compareVersions orders two versions as semver does; ok is false when
// either cannot be read.
func compareVersions(a, b string) (int, bool) {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	if !okA || !okB {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(va[i])
		y, _ := strconv.Atoi(vb[i])
		if x != y {
			if x < y {
				return -1, true
			}
			return 1, true
		}
	}
	switch {
	case va[3] == vb[3]:
		return 0, true
	case va[3] == "":
		return 1, true
	case vb[3] == "":
		return -1, true
	}
	return comparePre(strings.Split(va[3], "."), strings.Split(vb[3], ".")), true
}

// comparePre orders pre-release identifiers as semver does: numbers
// numerically and below words, words as text, and a shorter list first
// when it is a prefix of the other.
func comparePre(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		x, errX := strconv.ParseUint(a[i], 10, 64)
		y, errY := strconv.ParseUint(b[i], 10, 64)
		switch {
		case errX == nil && errY == nil:
			if x != y {
				if x < y {
					return -1
				}
				return 1
			}
		case errX == nil:
			return -1
		case errY == nil:
			return 1
		default:
			if c := strings.Compare(a[i], b[i]); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}
