package gotest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/undervoke/ytif/internal/check"
)

// A result-mapping refactor can label Go's replayed test events as fresh passes.
// Drive discovery and both real go test invocations so cached output, not a
// hand-fed flag, decides the outcome. Retire with native Go result reporting.
func TestSourceReportsGoCacheReplay(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":        "module example.com/cacheproof\n\ngo 1.25.0\n",
		"cache_test.go": fmt.Sprintf("package cacheproof\nimport \"testing\"\nfunc TestProof(t *testing.T) { t.Log(%q) }\n", fmt.Sprint(time.Now().UnixNano())),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	repo := check.Repo{Root: root, Tracked: []string{"go.mod", "cache_test.go"}}
	s := &Source{}
	keys, _, err := s.Discover(context.Background(), repo)
	if err != nil || len(keys) != 1 {
		t.Fatalf("discover: keys=%v err=%v", keys, err)
	}
	for i, want := range []check.Outcome{check.Pass, check.Cached} {
		rep, err := s.Run(context.Background(), repo, keys, check.Input{})
		if err != nil || len(rep.Results) != 1 || len(rep.Invocations) != 1 || rep.Invocations[0].Err != nil {
			t.Fatalf("run %d: report=%+v err=%v", i, rep, err)
		}
		result := rep.Results[0]
		if result.Outcome != want || result.Timed != (i == 0) {
			t.Fatalf("run %d: outcome=%s timed=%v, want %s timed=%v", i, result.Outcome, result.Timed, want, i == 0)
		}
	}
}
