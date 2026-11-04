//go:build !windows

package utc

import (
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"time"
)

func (s DiskStore) Lock(ctx context.Context, scope string, pair uint64) (func(), error) {
	if s.Directory == "" || !filepath.IsAbs(s.Directory) {
		return nil, fmt.Errorf("UTC coordinator needs an absolute state directory")
	}
	if err := os.MkdirAll(s.Directory, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.path(scope, pair, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); _ = file.Close() }, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			file.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
