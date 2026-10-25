//go:build unix

package storage

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ErrBusy = errors.New("maintenance already running")

func MaintenanceLock(common string, wait bool) (*os.File, error) {
	return Lock(filepath.Join(Dir(common), "maintenance.lock"), wait)
}

func Lock(path string, wait bool) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := Exclusive(f, wait); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
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
