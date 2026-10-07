// Command hookprobe reports what gitx resolves from the current directory:
// the repository root, the staged files visible from it, and the
// repository-location environment it inherited. Tests install it in a real
// Git hook to prove root discovery under hook-inherited environment with
// the process started from a nested directory.
package main

import (
	"fmt"
	"os"

	"github.com/undervoke/ytif/internal/gitx"
)

func main() {
	for _, k := range []string{"GIT_DIR", "GIT_INDEX_FILE", "GIT_PREFIX", "GIT_WORK_TREE"} {
		fmt.Printf("ENV %s=%s\n", k, os.Getenv(k))
	}
	root, err := gitx.Root(".")
	if err != nil {
		fmt.Println("ROOT-ERR:", err)
		os.Exit(1)
	}
	fmt.Println("ROOT=" + root)
	staged, err := gitx.Staged(root)
	if err != nil {
		fmt.Println("STAGED-ERR:", err)
		os.Exit(1)
	}
	for _, f := range staged {
		fmt.Println("STAGED=" + f)
	}
}
