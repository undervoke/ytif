package guard

import "os"

// searchable reports whether the process may enter dir. Windows has no
// search bit to test, so the guard asks the operating system the way a
// shell does and treats a directory it cannot open as one it cannot enter.
func searchable(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
