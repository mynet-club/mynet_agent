//go:build windows

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLock 非阻塞抢独占锁（LockFileEx）。ok=false 表示已有实例在跑 ——
// 那是正常状态，不是错误。
func tryLock(path string) (f *os.File, ok bool, err error) {
	f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	ol := new(windows.Overlapped)
	lockErr := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol)
	if lockErr != nil {
		_ = f.Close()
		if errors.Is(lockErr, windows.ERROR_LOCK_VIOLATION) {
			return nil, false, nil
		}
		return nil, false, lockErr
	}
	return f, true, nil
}

func unlock(f *os.File) {
	ol := new(windows.Overlapped)
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
	_ = f.Close()
}
