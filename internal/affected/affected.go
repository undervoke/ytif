// Package affected reads the dependency graph the repository already
// maintains and reports which checks a change cannot affect. Nx is the only
// graph source. A unit the graph does not place in a project is never
// reported unaffected.
package affected

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/proc"
	"github.com/undervoke/ytif/internal/runner/nodetool"
)

// Graph places units in projects and knows which projects, and which tasks of
// the asked targets, a change affects.
type Graph struct {
	roots    map[string]string // project → slash root, "" for the repository root
	targets  map[string]map[string]json.RawMessage
	affected map[string]bool
	tasks    map[string]map[string]bool // asked target → projects owning an affected task
}

// Unaffected reports whether unit belongs to a project the change does not
// affect. A unit outside every project is affected.
func (g *Graph) Unaffected(unit string) bool {
	owner, found := g.Project(unit)
	return found && !g.affected[owner]
}

// Project returns the deepest project owning unit, when the graph maps it.
func (g *Graph) Project(unit string) (string, bool) {
	owner, best := "", -1
	for name, root := range g.roots {
		if (root == "" || unit == root || strings.HasPrefix(unit, root+"/")) && len(root) > best {
			owner, best = name, len(root)
		}
	}
	return owner, best >= 0
}

// Task reports whether the change affects target's task in the project rooted
// exactly at unit. known is false when no project has that root, the project
// lacks the target, or the target was not asked, so the caller cannot narrow:
// an empty answer for a target the graph no longer has means "missing", not
// "unaffected".
func (g *Graph) Task(unit, target string) (affected, known bool) {
	root := unit
	if root == "." {
		root = ""
	}
	for name, r := range g.roots {
		if r != root {
			continue
		}
		owners, asked := g.tasks[target]
		if _, has := g.targets[name][target]; !has || !asked {
			return false, false
		}
		return owners[name], true
	}
	return false, false
}

// NxFiles reports which projects the changed files affect, and for each of
// targets which projects own an affected task of it. It loads the Nx project
// graph once, then asks Nx for exactly those files: once for projects, then
// once per target, because Nx merges the answer for several targets. With
// task-granular affected (Nx 24 unless affected.legacy is set), the target
// answer comes from task inputs and is not bounded by the project answer;
// legacy Nx returns the affected projects that have the target. changed holds
// slash-separated paths relative to the repository root. An empty list
// returns the graph with nothing affected, without invoking an affected
// query. Verification units outside the graph's projects remain selected.
// Target names Nx cannot receive verbatim stay unasked. Unrepresentable
// paths and Nx failures return an error so the caller falls back to the
// eligible set.
//
// Contract: --files and --withTarget are each one argv that Nx splits on
// commas only, stripping one pair of surrounding double quotes per item, so
// commas and quote-wrapped names cannot be passed while other names pass
// through verbatim. Deleted paths are valid affected input. Overlong lists
// fall back through the invocation error, with no length pre-check here.
func NxFiles(ctx context.Context, repo check.Repo, changed, targets []string) (*Graph, string, []check.Invocation, error) {
	if !slices.Contains(repo.Tracked, "nx.json") {
		return nil, "no nx.json at the repository root", nil, nil
	}
	nx, err := newNxSession(repo, "affected")
	if err != nil {
		return nil, "", nil, err
	}
	projects, err := nx.projects(ctx, "nx-graph.json")
	if err != nil {
		return nil, "", nx.invs, err
	}
	g := &Graph{roots: map[string]string{}, targets: map[string]map[string]json.RawMessage{},
		affected: map[string]bool{}, tasks: map[string]map[string]bool{}}
	for _, p := range projects {
		g.roots[p.Name] = p.Root
		g.targets[p.Name] = p.Targets
	}
	seen := map[string]bool{}
	var files []string
	for _, f := range changed {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		if !verbatim(f) {
			return nil, "", nx.invs, fmt.Errorf("nx --files: path %q cannot be represented unambiguously", f)
		}
		files = append(files, f)
	}
	var asked []string
	for _, t := range targets {
		// Without changed files nothing is affected, whatever the name.
		if t != "" && (len(files) == 0 || verbatim(t)) && !slices.Contains(asked, t) {
			asked = append(asked, t)
		}
	}
	sort.Strings(asked)
	for _, t := range asked {
		g.tasks[t] = map[string]bool{}
	}
	if len(files) == 0 {
		return g, "", nx.invs, nil
	}
	sort.Strings(files)
	joined := strings.Join(files, ",")
	names, err := nx.affected(ctx, "show projects", "--files="+joined)
	if err != nil {
		return nil, "", nx.invs, err
	}
	for _, n := range names {
		g.affected[n] = true
	}
	for _, t := range asked {
		owners, err := nx.affected(ctx, "show projects -t "+t, "--files="+joined, "--withTarget="+t)
		if err != nil {
			return nil, "", nx.invs, err
		}
		for _, n := range owners {
			g.tasks[t][n] = true
		}
	}
	return g, "", nx.invs, nil
}

// verbatim reports whether Nx receives name unchanged as one item of a
// comma-split option.
func verbatim(name string) bool {
	return !strings.Contains(name, ",") && !(strings.HasPrefix(name, `"`) && strings.HasSuffix(name, `"`))
}

// Project is one node of the resolved Nx project graph.
type Project struct {
	Name    string
	Root    string                     // slash path, "" for the repository root
	Targets map[string]json.RawMessage // by exact target name
}

// Projects reads the project graph as Nx resolves it, plugin-inferred targets
// included. A repository without nx.json has no graph, which is an error here
// because only a repository that asked for the graph calls this.
func Projects(ctx context.Context, repo check.Repo) ([]Project, []check.Invocation, error) {
	if !slices.Contains(repo.Tracked, "nx.json") {
		return nil, nil, errors.New("no nx.json at the repository root")
	}
	nx, err := newNxSession(repo, "list")
	if err != nil {
		return nil, nil, err
	}
	projects, err := nx.projects(ctx, "nx-targets-graph.json")
	return projects, nx.invs, err
}

// nxSession runs nx at the repository root and keeps each call's cost, which
// it records under what.
type nxSession struct {
	repo check.Repo
	bin  string
	what string
	invs []check.Invocation
}

func newNxSession(repo check.Repo, what string) (*nxSession, error) {
	bin, err := nodetool.Bin(repo.Root, ".", "nx")
	if err != nil {
		return nil, err
	}
	return &nxSession{repo: repo, bin: bin, what: what}, nil
}

func (s *nxSession) run(ctx context.Context, label string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, s.bin, args...)
	cmd.Dir = s.repo.Root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	elapsed, err := proc.Run(ctx, cmd)
	if err != nil {
		err = fmt.Errorf("nx %s: %w\n%s", label, err, strings.TrimSpace(stderr.String()))
	}
	// The caller reports a failure, so this invocation records only cost.
	s.invs = append(s.invs, check.Invocation{Runner: "nx", Unit: label, What: s.what,
		Elapsed: elapsed, Interrupted: errors.Is(err, proc.ErrInterrupted)})
	return stdout.Bytes(), err
}

// affected lists the projects `nx show projects --affected` selects with the
// given options.
func (s *nxSession) affected(ctx context.Context, label string, options ...string) ([]string, error) {
	args := append([]string{"show", "projects", "--affected"}, options...)
	out, err := s.run(ctx, label, append(args, "--json")...)
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal(lastLine(out), &names); err != nil {
		return nil, fmt.Errorf("nx %s: %w", label, err)
	}
	return names, nil
}

// projects writes the graph to name in the scratch directory and reads it.
func (s *nxSession) projects(ctx context.Context, name string) ([]Project, error) {
	file := filepath.Join(s.repo.Scratch, name)
	if _, err := s.run(ctx, "graph", "graph", "--file="+file); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Graph struct {
			Nodes map[string]struct {
				Data struct {
					Root    string
					Targets map[string]json.RawMessage
				}
			}
		}
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("nx graph: %w", err)
	}
	projects := make([]Project, 0, len(doc.Graph.Nodes))
	for name, n := range doc.Graph.Nodes {
		root := path.Clean(filepath.ToSlash(n.Data.Root))
		if root == "." {
			root = ""
		}
		projects = append(projects, Project{Name: name, Root: root, Targets: n.Data.Targets})
	}
	return projects, nil
}

// lastLine returns the last nonblank line, where nx prints its JSON after
// any notices.
func lastLine(out []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	return lines[len(lines)-1]
}
