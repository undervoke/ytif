package gateconf

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/reconcile"
)

type runDefaults struct {
	Run struct {
		Shell            string `yaml:"shell"`
		WorkingDirectory string `yaml:"working-directory"`
	} `yaml:"run"`
}

type workflow struct {
	Defaults runDefaults `yaml:"defaults"`
	Jobs     map[string]struct {
		Uses     string      `yaml:"uses"`
		RunsOn   yaml.Node   `yaml:"runs-on"`
		Defaults runDefaults `yaml:"defaults"`
		Steps    []struct {
			Name             string `yaml:"name"`
			ID               string `yaml:"id"`
			Uses             string `yaml:"uses"`
			Run              string `yaml:"run"`
			Shell            string `yaml:"shell"`
			WorkingDirectory string `yaml:"working-directory"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// workflowEntries reads every tracked workflow under .github/workflows.
func workflowEntries(repo check.Repo) ([]entry, []reconcile.Finding) {
	var entries []entry
	var findings []reconcile.Finding
	for _, f := range repo.Tracked {
		if path.Dir(f) != ".github/workflows" || !isYAML(f) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repo.Root, filepath.FromSlash(f)))
		if err != nil {
			findings = append(findings, reconcile.Finding{Kind: reconcile.GateConfig, Location: f, Detail: err.Error()})
			continue
		}
		var wf workflow
		if err := yaml.Unmarshal(data, &wf); err != nil {
			findings = append(findings, reconcile.Finding{Kind: reconcile.GateConfig, Location: f, Detail: "cannot parse: " + err.Error()})
			continue
		}
		for _, jobID := range sortedKeys(wf.Jobs) {
			job := wf.Jobs[jobID]
			if job.Uses != "" {
				entries = append(entries, entry{surface: inventory.SurfaceGitHubActions, location: fmt.Sprintf("%s: jobs.%s", f, jobID), uses: job.Uses, job: true})
			}
			shell := firstNonEmpty(job.Defaults.Run.Shell, wf.Defaults.Run.Shell, defaultShell(&job.RunsOn))
			dir := firstNonEmpty(job.Defaults.Run.WorkingDirectory, wf.Defaults.Run.WorkingDirectory)
			for i, step := range job.Steps {
				loc := fmt.Sprintf("%s: jobs.%s.steps[%d]", f, jobID, i)
				if label := firstNonEmpty(step.ID, step.Name); label != "" {
					loc += " (" + strings.TrimSpace(label) + ")"
				}
				switch {
				case step.Uses != "":
					entries = append(entries, entry{surface: inventory.SurfaceGitHubActions, location: loc, uses: step.Uses})
				case step.Run != "":
					entries = append(entries, entry{surface: inventory.SurfaceGitHubActions, location: loc, run: step.Run,
						shell: firstNonEmpty(step.Shell, shell), scope: dirScope(firstNonEmpty(step.WorkingDirectory, dir))})
				}
			}
		}
	}
	return entries, findings
}

// defaultShell is the shell a run step gets without one set: pwsh on
// Windows runners, bash elsewhere, and unknown when runs-on is computed.
func defaultShell(runsOn *yaml.Node) string {
	var labels []string
	var collect func(n *yaml.Node)
	collect = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode {
			labels = append(labels, n.Value)
		}
		for _, c := range n.Content {
			collect(c)
		}
	}
	collect(runsOn)
	for _, l := range labels {
		switch {
		case strings.Contains(l, "${{"):
			return "the default for a computed runs-on"
		case strings.Contains(strings.ToLower(l), "windows"):
			return "pwsh"
		}
	}
	return ""
}

// dirScope is the scope of a working directory relative to the repository
// root; a computed directory cannot be resolved.
func dirScope(dir string) scope {
	switch {
	case dir == "":
		return rootScope
	case strings.Contains(dir, "${{") || strings.Contains(dir, "{") || strings.Contains(dir, "$") || filepath.IsAbs(dir):
		return scope{}
	}
	return scope{dir: path.Clean(dir), known: true}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
