// Package affected reports which checks a change cannot affect, from a
// dependency graph the repository already maintains. Nx is the only graph
// source. A unit the graph does not place in a project is never reported
// unaffected.
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

// Graph places units in projects and knows which projects a change affects.
type Graph struct {
	roots    map[string]string // project → slash root, "" for the repository root
	affected map[string]bool
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

// NxFiles reports which projects the changed files affect. It loads the Nx
// project graph once, then asks Nx for the affected projects for exactly
// those files. changed holds slash-separated paths relative to the
// repository root. An empty list returns the graph with nothing marked
// affected, without invoking the affected query. Verification units outside
// the graph's projects remain selected. Unrepresentable names and Nx failures return an
// error so the caller falls back to the eligible set.
//
// Contract: --files is one comma-joined argv; Nx splits items on commas
// only and strips one pair of surrounding double quotes, so commas and
// quote-wrapped names fall back while other names pass through verbatim.
// Deleted paths are valid affected input. Overlong lists fall back through
// the invocation error, with no length pre-check here.
func NxFiles(ctx context.Context, repo check.Repo, changed []string) (*Graph, string, []check.Invocation, error) {
	if !slices.Contains(repo.Tracked, "nx.json") {
		return nil, "no nx.json at the repository root", nil, nil
	}
	bin, err := nodetool.Bin(repo.Root, ".", "nx")
	if err != nil {
		return nil, "", nil, err
	}
	var invs []check.Invocation
	nx := func(label string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = repo.Root
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		elapsed, err := proc.Run(ctx, cmd)
		if err != nil {
			err = fmt.Errorf("nx %s: %w\n%s", label, err, strings.TrimSpace(stderr.String()))
		}
		// The gate reports the fallback, so this invocation records only cost.
		invs = append(invs, check.Invocation{Runner: "nx", Unit: label, What: "affected",
			Elapsed: elapsed, Interrupted: errors.Is(err, proc.ErrInterrupted)})
		return stdout.Bytes(), err
	}

	file := filepath.Join(repo.Scratch, "nx-graph.json")
	if _, err := nx("graph", "graph", "--file="+file); err != nil {
		return nil, "", invs, err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, "", invs, err
	}
	var doc struct {
		Graph struct {
			Nodes map[string]struct {
				Data struct{ Root string }
			}
		}
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, "", invs, fmt.Errorf("nx graph: %w", err)
	}
	g := &Graph{roots: map[string]string{}, affected: map[string]bool{}}
	for name, n := range doc.Graph.Nodes {
		root := path.Clean(filepath.ToSlash(n.Data.Root))
		if root == "." {
			root = ""
		}
		g.roots[name] = root
	}
	if len(changed) == 0 {
		return g, "", invs, nil
	}
	seen := map[string]bool{}
	var files []string
	for _, f := range changed {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		if strings.Contains(f, ",") || (strings.HasPrefix(f, `"`) && strings.HasSuffix(f, `"`)) {
			return nil, "", invs, fmt.Errorf("nx --files: path %q cannot be represented unambiguously", f)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return g, "", invs, nil
	}
	sort.Strings(files)
	joined := strings.Join(files, ",")
	out, err := nx("show projects", "show", "projects", "--affected", "--files="+joined, "--json")
	if err != nil {
		return nil, "", invs, err
	}
	var names []string
	if err := json.Unmarshal(lastLine(out), &names); err != nil {
		return nil, "", invs, fmt.Errorf("nx show projects: %w", err)
	}
	for _, n := range names {
		g.affected[n] = true
	}
	return g, "", invs, nil
}

// lastLine returns the last nonblank line, where nx prints its JSON after
// any notices.
func lastLine(out []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	return lines[len(lines)-1]
}
