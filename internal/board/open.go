package board

import (
	"fmt"
	"os/exec"
	"runtime"
)

// Open shows the file in the default browser.
func Open(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "linux":
		cmd = exec.Command("xdg-open", path)
	default:
		return fmt.Errorf("no opener for %s", runtime.GOOS)
	}
	return cmd.Run()
}
