// Package testhelp builds the fake adapter binary tests invoke as a real
// subprocess. It holds no checks of its own.
package testhelp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	once sync.Once
	bin  string
	bErr error
)

// FakeAdapter builds internal/testadapter once and returns its path. The
// binary replays YTIF_FAKE_RESPONSE on stdout and exits YTIF_FAKE_EXIT,
// saving its stdin request to YTIF_FAKE_REQUEST_LOG when set.
func FakeAdapter(t *testing.T) string {
	t.Helper()
	once.Do(func() {
		_, file, _, ok := runtime.Caller(0)
		if !ok {
			bErr = fmt.Errorf("find testhelp source")
			return
		}
		root := filepath.Dir(file)
		for {
			if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
				break
			}
			parent := filepath.Dir(root)
			if parent == root {
				bErr = fmt.Errorf("find module root")
				return
			}
			root = parent
		}
		dir, err := os.MkdirTemp("", "ytif-testadapter-")
		if err != nil {
			bErr = err
			return
		}
		bin = filepath.Join(dir, "testadapter")
		cmd := exec.Command("go", "build", "-o", bin, "./internal/testadapter")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			bErr = fmt.Errorf("build testadapter: %v\n%s", err, out)
		}
	})
	if bErr != nil {
		t.Fatal(bErr)
	}
	return bin
}
