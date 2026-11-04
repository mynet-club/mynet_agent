//go:build !windows

package utc

import (
	"errors"
	"os"
	"syscall"
)

func staticOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(stat.Uid) == uint64(os.Geteuid())
}
func checkStaticStateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 || !staticOwned(info) {
		return errors.New("UTC authority state must be an owned private directory")
	}
	return nil
}
