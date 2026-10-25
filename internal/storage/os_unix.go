//go:build unix

package storage

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// Open is os.OpenFile: unix already lets an open file be renamed or removed.
func Open(path string, flag int, perm fs.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag, perm)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func flock(f *os.File, how int) error {
	for {
		err := unix.Flock(int(f.Fd()), how)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return ErrBusy
		}
		return err
	}
}

func Shared(f *os.File) error { return flock(f, unix.LOCK_SH) }

func Exclusive(f *os.File, wait bool) error {
	how := unix.LOCK_EX
	if !wait {
		how |= unix.LOCK_NB
	}
	return flock(f, how)
}

func Linked(f *os.File) (bool, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return false, err
	}
	return stat.Nlink != 0, nil
}

func Unlock(f *os.File) error {
	if f == nil {
		return nil
	}
	err := flock(f, unix.LOCK_UN)
	return errors.Join(err, f.Close())
}
