//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

// tryLock 非阻塞抢独占锁。ok=false 表示已有实例在跑 —— 那是正常状态，
// 不是错误（err 只在真正打不开锁文件/系统调用失败时才非 nil）。
func tryLock(path string) (f *os.File, ok bool, err error) {
	f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if flockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); flockErr != nil {
		_ = f.Close()
		if errors.Is(flockErr, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, flockErr
	}
	return f, true, nil
}

func unlock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
