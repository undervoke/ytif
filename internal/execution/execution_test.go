package execution_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/undervoke/ytif/internal/execution"
)

// TestExecutionLoad covers the execution list contract: a missing file
// keeps native execution, and every malformed list fails with a reason.
func TestExecutionLoad(t *testing.T) {
	t.Run("missing keeps native execution", func(t *testing.T) {
		ex, err := execution.Load(t.TempDir())
		if err != nil || ex != nil {
			t.Fatalf("Load = %v, %v; want nil, nil", ex, err)
		}
	})

	valid := `version: 1
adapter:
  command: [node, tools/workspace/verification/adapter.mjs]
  runners: [command, vitest-test, playwright-test]
profiles:
  node:
    description: Node checks for hooks.
`
	t.Run("valid", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, valid)
		ex, err := execution.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if ex.Version != 1 || len(ex.Adapter.Command) != 2 || len(ex.Adapter.Runners) != 3 {
			t.Fatalf("unexpected decode: %+v", ex)
		}
		if ex.Profiles["node"].Description != "Node checks for hooks." {
			t.Fatalf("unexpected profiles: %+v", ex.Profiles)
		}
	})

	cases := map[string]string{
		"empty file":         ``,
		"wrong version":      "version: 2\nadapter:\n  command: [x]\n  runners: [a]\n",
		"two documents":      "version: 1\nadapter:\n  command: [x]\n  runners: [a]\n---\nversion: 1\n",
		"unknown field":      "version: 1\nadapter:\n  command: [x]\n  runners: [a]\n  extra: true\n",
		"empty command":      "version: 1\nadapter:\n  command: []\n  runners: [a]\n",
		"no runners":         "version: 1\nadapter:\n  command: [x]\n  runners: []\n",
		"duplicate runners":  "version: 1\nadapter:\n  command: [x]\n  runners: [a, a]\n",
		"empty profile name": "version: 1\nadapter:\n  command: [x]\n  runners: [a]\nprofiles:\n  '':\n    description: d\n",
		"empty description":  "version: 1\nadapter:\n  command: [x]\n  runners: [a]\nprofiles:\n  node:\n    description: ''\n",
		"not a mapping":      "version: 1\nadapter: [x]\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, body)
			if ex, err := execution.Load(dir); err == nil {
				t.Fatalf("Load = %+v, nil; want an error", ex)
			} else if !strings.Contains(err.Error(), execution.File) {
				t.Fatalf("error %q does not name %s", err, execution.File)
			}
		})
	}
}

func write(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, execution.File), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
