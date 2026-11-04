//go:build windows

package mynet

import "fmt"

func replaceUTCProcess(binary string, args, env []string) error {
	return fmt.Errorf("UTC service launcher is unsupported on Windows")
}
