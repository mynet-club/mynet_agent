//go:build windows

package endpoint

func isProcessAlive(pid int) bool {
	return pid > 0
}
