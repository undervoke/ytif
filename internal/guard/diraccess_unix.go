//go:build unix

package guard

import (
	"golang.org/x/sys/unix"
)

// searchable reports whether the process may enter dir: access(2) with
// X_OK, the permission cd alone needs. A directory a shell cannot enter
// makes `cd DIR` fail and leave the working directory alone, so the guard
// must not price what follows it there.
func searchable(dir string) bool { return unix.Access(dir, unix.X_OK) == nil }
