// Package initcmd adopts ytif in a repository: it pins this ytif as a Go
// tool, writes the inventory and exemptions, wires the gates into lefthook
// and a workflow, and registers the agent guard with the selected hosts.
// What is already there is kept; what is missing is added. It reads and
// checks every file before it writes any, so a file it cannot edit stops it
// with nothing changed. Host selection only installs or refreshes the
// selected hosts; unselected host files are left alone without being read
// for validation.
package initcmd

import (
	"bufio"
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
	guardCommand                = "go tool ytif guard"
	installedGuardCommand       = "ytif guard"
	cursorGuardCommand          = "go tool ytif guard --host cursor"
	installedCursorGuardCommand = "ytif guard --host cursor"
	workflowFile                = ".github/workflows/ytif.yml"

	claudeRel   = ".claude/settings.json"
	codexRel    = ".codex/hooks.json"
	cursorRel   = ".cursor/hooks.json"
	opencodeRel = ".opencode/plugins/ytif-guard.ts"
	kiloRel     = ".kilo/plugin/ytif-guard.ts"
	piRel       = ".pi/extensions/ytif-guard.ts"
)

// hostSlugs are the accepted --hosts values, in display order.
var hostSlugs = []string{"claude-code", "codex", "opencode", "kilo", "cursor", "pi"}

// file is one file init leaves as want.
type file struct {
	path   string // relative to the root, slash-separated
	want   []byte
	status string // created, updated, or unchanged
}

// Run adopts ytif in the repository at root with self, the running ytif's
// version, for the hosts from SelectHosts, and returns the exit code. An
// empty hosts slice installs the gates only.
func Run(root, self string, hosts []string, stdout, stderr io.Writer) int {
	selected := hosts
	if selected == nil {
		selected = []string{}
	}
	pin, err := pinStep(root, self)
	if err != nil {
		fmt.Fprintf(stderr, "ytif init: %v\n", err)
		return gate.ExitUsage
	}
	files, errs := plan(root, selected, pin.local)
	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintf(stderr, "ytif init: %v\n", err)
		}
		fmt.Fprintln(stderr, "ytif init: nothing was changed")
		return gate.ExitUsage
	}
	if err := formatFiles(root, files, stdout); err != nil {
		fmt.Fprintf(stderr, "ytif init: %v\nytif init: nothing was changed\n", err)
		return gate.ExitUsage
	}
	if pin.run {
		args := []string{"mod", "edit", "-tool=" + launch.Command}
		if !pin.local {
			if err := pinVendor(root, self); err != nil {
				fmt.Fprintf(stderr, "ytif init: %v\nytif init: nothing was changed\n", err)
				return gate.ExitUsage
			}
			args = []string{"get", "-tool", launch.Command + "@" + self}
		}
		get := exec.Command("go", args...)
		get.Dir, get.Stdout, get.Stderr = root, stderr, stderr
		if err := get.Run(); err != nil {
			fmt.Fprintf(stderr, "ytif init: go %s: %v\nytif init: go.mod/go.sum may have changed; integration files were not written\n", strings.Join(args, " "), err)
			return gate.ExitUsage
		}
	}
	// Check the same Go module mode the generated commands use. In particular,
	// go get leaves vendor/ untouched, so an outdated vendor tree cannot be
	// mistaken for a working installation. -n builds but does not run ytif.
	probe := exec.Command("go", "tool", "-n", "ytif")
	probe.Dir, probe.Stdout, probe.Stderr = root, io.Discard, stderr
	if err := probe.Run(); err != nil {
		fmt.Fprintf(stderr, "ytif init: the project's `go tool ytif` is not ready: %v; resolve the Go error above and rerun init\n", err)
		if pin.run {
			fmt.Fprintln(stderr, "ytif init: go.mod/go.sum may have changed; integration files were not written")
		} else {
			fmt.Fprintln(stderr, "ytif init: integration files were not written")
		}
		return gate.ExitUsage
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
	if containsHost(selected, "codex") {
		fmt.Fprintln(stdout, "note: Codex runs .codex/hooks.json once you trust its hooks in /hooks")
	}
	if containsHost(selected, "cursor") {
		if pin.local {
			fmt.Fprintln(stdout, "note: Cursor runs .cursor/hooks.json in a trusted workspace with the installed ytif on PATH")
		} else {
			fmt.Fprintln(stdout, "note: Cursor runs .cursor/hooks.json in a trusted workspace with Go and the pinned tool on PATH")
		}
	}
	if containsHost(selected, "pi") {
		fmt.Fprintln(stdout, "note: Pi loads .pi/extensions only in a trusted project")
	}
	if len(selected) > 0 {
		fmt.Fprintln(stdout, "note: open a new session so the selected hosts load the guard")
	}
	fmt.Fprintln(stdout, "next: stage the changes and run `ytif list` for the checks that still need contracts")
	return gate.ExitPass
}

// SelectHosts resolves the --hosts spec into the hosts init installs. An
// empty spec prompts on a real terminal with existing ytif registrations
// preselected; pressing Enter keeps them. It never writes files.
func SelectHosts(root, spec string, stdin io.Reader, stderr io.Writer) ([]string, error) {
	if strings.TrimSpace(spec) != "" {
		return parseHostSpec(spec)
	}
	if !isTerminal(stdin) || !isTerminalWriter(stderr) {
		return nil, errors.New("no --hosts given and no terminal; rerun with --hosts \"claude-code,codex,opencode,kilo,cursor,pi\" or \"none\" (orca has no binding; choose the agent it launches)")
	}
	defaults := existingHosts(root)
	fmt.Fprintln(stderr, "Configure agent hosts (existing ytif registrations are preselected):")
	for i, slug := range hostSlugs {
		mark := " "
		if containsHost(defaults, slug) {
			mark = "x"
		}
		fmt.Fprintf(stderr, "  %d) [%s] %s\n", i+1, mark, slug)
	}
	fmt.Fprintln(stderr, "Enter numbers or slugs separated by commas, `none`, or press Enter to keep.")
	fmt.Fprint(stderr, "hosts> ")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && len(line) == 0 {
		return nil, errors.New("no input; rerun with --hosts \"claude-code,codex,opencode,kilo,cursor,pi\" or \"none\"")
	}
	if strings.TrimSpace(line) == "" {
		return defaults, nil
	}
	return parseHostSpec(line)
}

// parseHostSpec reads comma or space separated numbers or slugs into the
// canonical host order. It reports unknown names and orca before any write.
func parseHostSpec(spec string) ([]string, error) {
	set := map[string]bool{}
	none := false
	for _, token := range strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		token = strings.TrimSpace(strings.ToLower(token))
		if token == "" {
			continue
		}
		if token == "none" {
			none = true
			continue
		}
		if token == "orca" {
			return nil, errors.New("orca has no binding; choose the host of the agent it launches (for example claude-code, codex, or opencode)")
		}
		if n, err := strconv.Atoi(token); err == nil {
			if n < 1 || n > len(hostSlugs) {
				return nil, fmt.Errorf("unknown host `%s`; choose from %s or `none`", token, strings.Join(hostSlugs, ", "))
			}
			set[hostSlugs[n-1]] = true
			continue
		}
		known := false
		for _, slug := range hostSlugs {
			if token == slug {
				set[slug] = true
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("unknown host `%s`; choose from %s or `none`", token, strings.Join(hostSlugs, ", "))
		}
	}
	if none && len(set) > 0 {
		return nil, errors.New("`none` cannot be combined with host names")
	}
	if none {
		return []string{}, nil
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("no hosts given; choose from %s or `none`", strings.Join(hostSlugs, ", "))
	}
	var out []string
	for _, slug := range hostSlugs {
		if set[slug] {
			out = append(out, slug)
		}
	}
	return out, nil
}

// existingHosts reports the hosts with a detectable ytif registration. It
// is conservative and read-only: missing or unreadable files only mean the
// host is not preselected. Detection reuses the planners in canonical order,
// so preselection matches what plan considers done.
func existingHosts(root string) []string {
	out := []string{}
	if hasJSONGuard(root, claudeRel) {
		out = append(out, "claude-code")
	}
	if hasJSONGuard(root, codexRel) {
		out = append(out, "codex")
	}
	if hasPluginGuard(root, opencodeRel) {
		out = append(out, "opencode")
	}
	if hasPluginGuard(root, kiloRel) {
		out = append(out, "kilo")
	}
	if hasCursorGuard(root) {
		out = append(out, "cursor")
	}
	if hasPluginGuard(root, piRel) {
		out = append(out, "pi")
	}
	return out
}

// hasJSONGuard reports whether the Claude-style hooks file already runs the
// ytif guard, reusing the planner's own recognition.
func hasJSONGuard(root, rel string) bool {
	f, err := hookConfig(root, rel, false)
	return err == nil && f.status == "unchanged"
}

// hasCursorGuard reports whether the Cursor hooks file already carries the
// unrestricted ytif guard, reusing the planner's own recognition.
func hasCursorGuard(root string) bool {
	f, err := cursorHookConfig(root, false)
	return err == nil && f.status == "unchanged"
}

// hasPluginGuard reports whether a plugin file looks like a ytif guard.
// The path itself names the ytif guard, so content containing ytif is
// enough for a conservative preselection.
func hasPluginGuard(root, rel string) bool {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "ytif")
}

func containsHost(hosts []string, slug string) bool {
	for _, h := range hosts {
		if h == slug {
			return true
		}
	}
	return false
}

// isTerminal reports whether r is a character device, the existing
// project's terminal test.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// isTerminalWriter reports whether w is a character device.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

type pinPlan struct {
	run    bool // go.mod needs an update
	local  bool // the tool belongs to the main module
	status string
	detail string
}

// pinStep registers the checkout's command in ytif's own module; elsewhere
// it pins the running release.
func pinStep(root, self string) (pinPlan, error) {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return pinPlan{}, errors.New("no go.mod at the repository root; ytif is pinned as a Go tool, which needs a Go module")
	}
	if _, err := exec.LookPath("go"); err != nil {
		return pinPlan{}, errors.New("go is not on PATH; pinning ytif needs Go 1.24 or later")
	}
	cmd := exec.Command("go", "mod", "edit", "-json")
	cmd.Dir = root
	data, err := cmd.Output()
	if err != nil {
		return pinPlan{}, fmt.Errorf("read go.mod: %w", err)
	}
	var mod struct {
		Module struct{ Path string }
		Tool   []struct{ Path string }
	}
	if err := json.Unmarshal(data, &mod); err != nil {
		return pinPlan{}, fmt.Errorf("read go.mod: %w", err)
	}
	if mod.Module.Path+"/cmd/ytif" == launch.Command {
		p := pinPlan{local: true, status: "unchanged", detail: "go.mod (ytif from this checkout)"}
		for _, tool := range mod.Tool {
			if tool.Path == launch.Command {
				return p, nil
			}
		}
		p.run, p.status = true, "updated"
		return p, nil
	}
	if _, ok := parseVersion(self); !ok || strings.Contains(self, "+") {
		return pinPlan{}, fmt.Errorf("this ytif (%s) is not a published version, so go.mod cannot pin it; install a release", self)
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

// pinVendor prevents a pin update from leaving an active vendor tree stale.
// Go tool selects the vendor tree belonging to its module or workspace;
// a modules.txt marked for the other mode does not activate vendoring.
func pinVendor(root, self string) error {
	cmd := exec.Command("go", "env", "GOWORK")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("read Go workspace: %w", err)
	}
	work := strings.TrimSpace(string(out))
	workspace := work != "" && work != "off"
	dir, sync := root, "go mod vendor"
	if workspace {
		dir, sync = filepath.Dir(work), "go work vendor"
	}
	vendor := filepath.Join(dir, "vendor")
	info, err := os.Stat(vendor)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(vendor, "modules.txt"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	first, _, _ := strings.Cut(string(data), "\n")
	vendorWorkspace := false
	if annotations, ok := strings.CutPrefix(first, "## "); ok {
		for _, a := range strings.Split(annotations, ";") {
			vendorWorkspace = vendorWorkspace || strings.TrimSpace(a) == "workspace"
		}
	}
	if workspace != vendorWorkspace {
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s activates Go vendoring but has no modules.txt; preserve or relocate unrelated files and prepare the Go vendor tree before pinning ytif", vendor)
	}
	return fmt.Errorf("pinning ytif %s would leave %s stale; run `go get -tool %s@%s` from %s, then `%s` from %s, and rerun init", self, vendor, launch.Command, self, root, sync, dir)
}

// plan works out every file and collects every reason one cannot be
// edited. Gate files are always planned; host files only for selected
// hosts, so unselected host files are never read or validated.
func plan(root string, hosts []string, local bool) ([]file, []error) {
	tracked, err := gitx.Tracked(root)
	if err != nil {
		return nil, []error{err}
	}
	tracked, err = withWorkflows(root, tracked)
	if err != nil {
		return nil, []error{err}
	}
	wired := gateconf.Wired(check.Repo{Root: root, Tracked: tracked})
	var files []file
	var errs []error
	add := func(f file, err error) {
		if err != nil {
			errs = append(errs, err)
			return
		}
		files = append(files, f)
	}
	if _, err := inventory.LoadInventory(root); err != nil {
		errs = append(errs, err)
	} else {
		add(skeleton(root, inventory.InventoryFile, "version: 3\nverifications: []\n"))
	}
	add(lefthook(root, wired))
	var actions []string
	if !wired.CI {
		f, err := workflow(root)
		add(f, err)
		actions = workflowActions
	}
	add(exemptions(root, actions))
	if containsHost(hosts, "claude-code") {
		add(hookConfig(root, claudeRel, local))
	}
	if containsHost(hosts, "codex") {
		add(hookConfig(root, codexRel, local))
	}
	if containsHost(hosts, "opencode") {
		add(plugin(root, opencodeRel, integrations.OpenCode, local))
	}
	if containsHost(hosts, "kilo") {
		add(plugin(root, kiloRel, integrations.Kilo, local))
	}
	if containsHost(hosts, "cursor") {
		add(cursorHookConfig(root, local))
	}
	if containsHost(hosts, "pi") {
		add(plugin(root, piRel, integrations.Pi, local))
	}
	return files, errs
}

// withWorkflows includes unstaged workflows that Git can add, so reruns
// find newly created workflows without counting ignored local drafts.
func withWorkflows(root string, tracked []string) ([]string, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "--others", "--exclude-standard", "-z", "--", ".github/workflows/").Output()
	if err != nil {
		return nil, fmt.Errorf("list unstaged workflows: %w", err)
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if ext := path.Ext(p); path.Dir(p) == ".github/workflows" && (ext == ".yml" || ext == ".yaml") {
			tracked = append(tracked, p)
		}
	}
	return tracked, nil
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
		if err := addHook(top, "pre-commit", "ytif-commit", "go tool ytif commit --hook", false); err != nil {
			return file{}, fmt.Errorf("%s: %w", rel, err)
		}
	}
	if !w.Push {
		if err := addHook(top, "pre-push", "ytif-push", "go tool ytif push --hook", true); err != nil {
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
	if stdin {
		// Lefthook shares a cached stdin reader between jobs. Run this hook
		// sequentially so each consumer receives the complete push refs.
		for i := 0; i+1 < len(h.Content); i += 2 {
			if h.Content[i].Value == "parallel" {
				old := h.Content[i+1]
				var parallel bool
				if old.Decode(&parallel) != nil || !parallel {
					continue
				}
				uses, err := hookStdin(h)
				if err != nil {
					return fmt.Errorf("%s: %w", hook, err)
				}
				if !uses {
					continue
				}
				if err := editable(old, hook+".parallel"); err != nil {
					return err
				}
				h.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false", HeadComment: old.HeadComment, LineComment: old.LineComment, FootComment: old.FootComment}
			}
		}
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

type stdinJob struct {
	UseStdin bool `yaml:"use_stdin"`
	Group    struct {
		Jobs []stdinJob `yaml:"jobs"`
	} `yaml:"group"`
}

func (j stdinJob) readsStdin() bool {
	if j.UseStdin {
		return true
	}
	for _, child := range j.Group.Jobs {
		if child.readsStdin() {
			return true
		}
	}
	return false
}

// hookStdin reads the effective job options, including aliases and merges,
// to tell whether adding ytif push creates a second stdin consumer.
func hookStdin(h *yaml.Node) (bool, error) {
	var hook struct {
		Jobs     []stdinJob          `yaml:"jobs"`
		Commands map[string]stdinJob `yaml:"commands"`
		Scripts  map[string]stdinJob `yaml:"scripts"`
	}
	if err := h.Decode(&hook); err != nil {
		return false, err
	}
	for _, jobs := range []map[string]stdinJob{hook.Commands, hook.Scripts} {
		for _, j := range jobs {
			hook.Jobs = append(hook.Jobs, j)
		}
	}
	for _, j := range hook.Jobs {
		if j.readsStdin() {
			return true, nil
		}
	}
	return false, nil
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
// Codex hooks file, unless a foreground hook already runs it. Exec form is
// recognized for Claude Code only. In ytif's own module the guard runs as
// the installed ytif, elsewhere as the pinned tool; a go-tool guard there
// is rewritten in place instead of appended.
func hookConfig(root, rel string, local bool) (file, error) {
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
	want := guardCommand
	if local {
		want = installedGuardCommand
	}
	found, sites := scanHooks(pre, rel)
	if found && (!local || len(sites) == 0) {
		return file{path: rel, want: data, status: "unchanged"}, nil
	}
	if len(sites) > 0 { // local with go-tool guards to migrate
		rewritten, err := migrateHooks(pre, sites)
		if err != nil {
			return file{}, fmt.Errorf("%s: %v", rel, err)
		}
		pre = rewritten
	} else {
		type command struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		}
		entry, err := marshal(struct {
			Matcher string    `json:"matcher"`
			Hooks   []command `json:"hooks"`
		}{"Bash", []command{{"command", want}}})
		if err != nil {
			return file{}, err
		}
		pre = append(pre, entry)
	}
	list, err := marshal(pre)
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

// cursorHookConfig registers the guard as an unrestricted
// beforeShellExecution entry in the Cursor hooks file. An entry counts only
// when it runs the guard for Cursor with no type or type command and an
// unrestricted matcher. Only version 1 is edited. In ytif's own module the
// guard runs as the installed ytif, elsewhere as the pinned tool; a
// go-tool guard there is rewritten in place instead of appended.
func cursorHookConfig(root string, local bool) (file, error) {
	data, ok, err := read(root, cursorRel)
	if err != nil {
		return file{}, err
	}
	if !ok {
		data = []byte("{}")
	}
	top, err := parseObject(data)
	if err != nil {
		return file{}, fmt.Errorf("%s: %v; ytif edits plain JSON only", cursorRel, err)
	}
	if raw, ok := top.get("version"); ok {
		var num float64
		if err := json.Unmarshal(raw, &num); err != nil || num != 1 {
			return file{}, fmt.Errorf("%s: version %s is not supported; ytif edits version 1 only", cursorRel, string(raw))
		}
	}
	hooks := &object{vals: map[string]json.RawMessage{}}
	if raw, ok := top.get("hooks"); ok {
		if hooks, err = parseObject(raw); err != nil {
			return file{}, fmt.Errorf("%s: hooks: %v", cursorRel, err)
		}
	}
	var list []json.RawMessage
	if raw, ok := hooks.get("beforeShellExecution"); ok {
		if err := json.Unmarshal(raw, &list); err != nil {
			return file{}, fmt.Errorf("%s: hooks.beforeShellExecution is not a list", cursorRel)
		}
	}
	wantCursor := cursorGuardCommand
	if local {
		wantCursor = installedCursorGuardCommand
	}
	found, sites := scanCursor(list)
	if found && (!local || len(sites) == 0) {
		if _, hasVersion := top.get("version"); hasVersion {
			return file{path: cursorRel, want: data, status: "unchanged"}, nil
		}
		top.set("version", json.RawMessage("1"))
		out, err := top.indent()
		if err != nil {
			return file{}, fmt.Errorf("%s: %w", cursorRel, err)
		}
		return compare(root, cursorRel, out)
	}
	if len(sites) > 0 { // local with go-tool guards to migrate
		rewritten, err := migrateCursor(list, sites)
		if err != nil {
			return file{}, fmt.Errorf("%s: %v", cursorRel, err)
		}
		list = rewritten
	} else {
		entry, err := marshal(struct {
			Command string `json:"command"`
		}{wantCursor})
		if err != nil {
			return file{}, err
		}
		list = append(list, entry)
	}
	rawList, err := marshal(list)
	if err != nil {
		return file{}, err
	}
	hooks.set("beforeShellExecution", rawList)
	rawHooks, err := hooks.MarshalJSON()
	if err != nil {
		return file{}, err
	}
	top.set("hooks", rawHooks)
	if _, hasVersion := top.get("version"); !hasVersion {
		top.set("version", json.RawMessage("1"))
	}
	out, err := top.indent()
	if err != nil {
		return file{}, fmt.Errorf("%s: %w", cursorRel, err)
	}
	return compare(root, cursorRel, out)
}

// guardForm is how a hook starts the ytif guard.
type guardForm int

const (
	noGuard guardForm = iota
	directGuard
	goToolGuard
)

// guardFields splits command with args into words. Shell form trims quotes
// around the executable; exec form is already split.
func guardFields(command string, args json.RawMessage) []string {
	if args == nil {
		f := strings.Fields(command)
		if len(f) >= 2 {
			f[len(f)-2] = strings.Trim(f[len(f)-2], `"'`)
		}
		return f
	}
	var argv []string
	if json.Unmarshal(args, &argv) != nil || argv == nil {
		return nil
	}
	return append([]string{command}, argv...)
}

// cursorFields strips the Cursor host flag from command and trims quotes,
// or returns nil when the flag is absent.
func cursorFields(command string) []string {
	f := strings.Fields(command)
	if len(f) == 0 {
		return nil
	}
	trim := func(s string) string { return strings.Trim(s, `"'`) }
	last := trim(f[len(f)-1])
	switch {
	case last == "cursor" && len(f) >= 2 && (trim(f[len(f)-2]) == "--host" || trim(f[len(f)-2]) == "-host"):
		f = f[:len(f)-2]
	case last == "--host=cursor" || last == "-host=cursor":
		f = f[:len(f)-1]
	default:
		return nil
	}
	for i := range f {
		f[i] = trim(f[i])
	}
	return f
}

// guardFormOf reports how fields start the ytif guard: directly as ytif, or
// through go tool.
func guardFormOf(f []string) guardForm {
	if len(f) < 2 || f[len(f)-1] != "guard" || path.Base(f[len(f)-2]) != "ytif" {
		return noGuard
	}
	if len(f) == 2 {
		return directGuard
	}
	if len(f) == 4 && path.Base(f[0]) == "go" && f[1] == "tool" {
		return goToolGuard
	}
	return noGuard
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

// hookEntry is one PreToolUse hook candidate.
type hookEntry struct {
	Type        string          `json:"type"`
	Command     string          `json:"command"`
	Args        json.RawMessage `json:"args"`
	Async       bool            `json:"async"`
	AsyncRewake bool            `json:"asyncRewake"`
	Once        bool            `json:"once"`
	If          string          `json:"if"`
}

// hookSite locates one go-tool guard hook inside pre for migration.
type hookSite struct {
	group, hook int
	exec        bool
}

// foreground reports whether h is a foreground command hook, the only kind
// init counts. Exec form is recognized for Claude Code only.
func foreground(h hookEntry, rel string) bool {
	return h.Type == "command" && !h.Async && !h.AsyncRewake && !h.Once && h.If == "" &&
		(h.Args == nil || rel == claudeRel)
}

// scanHooks decodes each PreToolUse group once and reports whether any
// foreground Bash hook runs the guard, and where the go-tool guards are. A
// missing matcher selects every tool, including Bash.
func scanHooks(pre []json.RawMessage, rel string) (bool, []hookSite) {
	var found bool
	var sites []hookSite
	for i, m := range pre {
		var group struct {
			Matcher string      `json:"matcher"`
			Hooks   []hookEntry `json:"hooks"`
		}
		if json.Unmarshal(m, &group) != nil || !coversBash(group.Matcher) {
			continue
		}
		for j, h := range group.Hooks {
			if !foreground(h, rel) {
				continue
			}
			switch guardFormOf(guardFields(h.Command, h.Args)) {
			case directGuard:
				found = true
			case goToolGuard:
				found = true
				sites = append(sites, hookSite{i, j, h.Args != nil})
			}
		}
	}
	return found, sites
}

// migrateHooks rewrites the flagged go-tool guards to the installed guard,
// keeping the shell or exec form and every other key. Only flagged groups
// are re-decoded, as ordered objects.
func migrateHooks(pre []json.RawMessage, sites []hookSite) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, len(pre))
	copy(out, pre)
	byGroup := map[int][]hookSite{}
	for _, s := range sites {
		byGroup[s.group] = append(byGroup[s.group], s)
	}
	shell, err := marshal(installedGuardCommand)
	if err != nil {
		return nil, err
	}
	execCommand, err := marshal("ytif")
	if err != nil {
		return nil, err
	}
	execArgs, err := marshal([]string{"guard"})
	if err != nil {
		return nil, err
	}
	for i, groupSites := range byGroup {
		group, err := parseObject(out[i])
		if err != nil {
			return nil, err
		}
		raw, ok := group.get("hooks")
		if !ok {
			continue
		}
		var hooks []json.RawMessage
		if json.Unmarshal(raw, &hooks) != nil {
			continue
		}
		for _, s := range groupSites {
			obj, err := parseObject(hooks[s.hook])
			if err != nil {
				return nil, err
			}
			if s.exec {
				obj.set("command", execCommand)
				obj.set("args", execArgs)
			} else {
				obj.set("command", shell)
			}
			raw, err := obj.MarshalJSON()
			if err != nil {
				return nil, err
			}
			hooks[s.hook] = raw
		}
		encoded, err := marshal(hooks)
		if err != nil {
			return nil, err
		}
		group.set("hooks", encoded)
		rawGroup, err := group.MarshalJSON()
		if err != nil {
			return nil, err
		}
		out[i] = rawGroup
	}
	return out, nil
}

// scanCursor decodes each beforeShellExecution entry once and reports
// whether any unrestricted entry runs the guard, and where the go-tool
// guards are.
func scanCursor(list []json.RawMessage) (bool, []int) {
	var found bool
	var sites []int
	for i, entry := range list {
		var item struct {
			Command string `json:"command"`
			Matcher string `json:"matcher"`
			Type    string `json:"type"`
		}
		if json.Unmarshal(entry, &item) != nil {
			continue
		}
		if (item.Type != "" && item.Type != "command") || (item.Matcher != "" && item.Matcher != "*") {
			continue
		}
		switch guardFormOf(cursorFields(item.Command)) {
		case directGuard:
			found = true
		case goToolGuard:
			found = true
			sites = append(sites, i)
		}
	}
	return found, sites
}

// migrateCursor rewrites the flagged go-tool guards to the installed guard,
// keeping every other key.
func migrateCursor(list []json.RawMessage, sites []int) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, len(list))
	copy(out, list)
	command, err := marshal(installedCursorGuardCommand)
	if err != nil {
		return nil, err
	}
	for _, i := range sites {
		obj, err := parseObject(out[i])
		if err != nil {
			return nil, err
		}
		obj.set("command", command)
		raw, err := obj.MarshalJSON()
		if err != nil {
			return nil, err
		}
		out[i] = raw
	}
	return out, nil
}

// plugin writes a guard plugin that starts ytif: the installed executable
// in ytif's own module, the pinned tool elsewhere.
func plugin(root, rel, src string, local bool) (file, error) {
	const from, to = `const YTIF = ["ytif"]`, `const YTIF = ["go", "tool", "ytif"]`
	if strings.Count(src, from) != 1 {
		return file{}, fmt.Errorf("%s: the embedded plugin does not declare %s", rel, from)
	}
	if local {
		return compare(root, rel, []byte(src))
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
