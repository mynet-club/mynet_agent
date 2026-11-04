//go:build windows

package utc

import (
	"errors"
	"os"
)

func staticOwned(info os.FileInfo) bool { return false }
func checkStaticStateDirectory(path string) error {
	return errors.New("durable UTC static authority is unsupported on Windows")
}
