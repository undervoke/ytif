package storage

import (
	"errors"
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Open opens like os.OpenFile but also shares delete access, which
// os.OpenFile withholds on Windows: rotation renames and eviction removes
// files that writers and readers may hold open, as unix allows.
func Open(path string, flag int, perm fs.FileMode) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	var access uint32
	switch flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_RDONLY:
		access = windows.GENERIC_READ
	case os.O_WRONLY:
		access = windows.GENERIC_WRITE
	case os.O_RDWR:
		access = windows.GENERIC_READ | windows.GENERIC_WRITE
	}
	if flag&os.O_APPEND != 0 {
		// append access without write access keeps every write at the end;
		// read access lets the handle take byte-range locks
		access = access&^windows.GENERIC_WRITE | windows.GENERIC_READ | windows.FILE_APPEND_DATA
	}
	create := uint32(windows.OPEN_EXISTING)
	if flag&os.O_CREATE != 0 {
		create = windows.OPEN_ALWAYS
	}
	attrs := uint32(windows.FILE_ATTRIBUTE_NORMAL)
	if perm&0o200 == 0 && flag&os.O_CREATE != 0 {
		attrs = windows.FILE_ATTRIBUTE_READONLY
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	h, err := windows.CreateFile(name, access, share, nil, create, attrs, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// syncDir has nothing to flush: NTFS journals the rename itself, and a
// directory handle cannot be flushed.
func syncDir(string) error { return nil }

// lockRange places every lock on one byte far past any record, because
// Windows locks are mandatory: a lock over the data would block the
// holder's own appends.
func lockRange() *windows.Overlapped {
	return &windows.Overlapped{Offset: 0xFFFFFFFF, OffsetHigh: 0x7FFFFFFF}
}

func lock(f *os.File, flags uint32) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, lockRange())
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrBusy
	}
	return err
}

func Shared(f *os.File) error { return lock(f, 0) }

func Exclusive(f *os.File, wait bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if !wait {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	return lock(f, flags)
}

// fileStandardInfo mirrors FILE_STANDARD_INFO.
type fileStandardInfo struct {
	AllocationSize int64
	EndOfFile      int64
	NumberOfLinks  uint32
	DeletePending  bool
	Directory      bool
}

// Linked reports whether the file still has a name: a removed file keeps
// its open handles but is pending deletion or has no links left.
func Linked(f *os.File) (bool, error) {
	var info fileStandardInfo
	err := windows.GetFileInformationByHandleEx(windows.Handle(f.Fd()), windows.FileStandardInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		return false, err
	}
	return !info.DeletePending && info.NumberOfLinks != 0, nil
}

func Unlock(f *os.File) error {
	if f == nil {
		return nil
	}
	err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRange())
	if errors.Is(err, windows.ERROR_NOT_LOCKED) {
		err = nil
	}
	return errors.Join(err, f.Close())
}
