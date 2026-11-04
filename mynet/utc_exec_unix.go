//go:build !windows

package mynet

import "golang.org/x/sys/unix"

func replaceUTCProcess(binary string, args, env []string) error {
	return unix.Exec(binary, args, env)
}
