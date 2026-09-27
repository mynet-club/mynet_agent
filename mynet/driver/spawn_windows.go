//go:build windows

package driver

import "os/exec"

// spawnDetached 启动 gnb。Windows 上 gnb **不自我 daemon**（无 fork），
// Wait() 会把 `mynet service start` 挂死 —— 只 Start 不 Wait。
func spawnDetached(name string, args ...string) error {
	_, err := spawnDetachedPID(name, args...)
	return err
}

// spawnDetachedPID 返回子进程 PID（Windows 判活靠我们写的 gnb.pid）。
func spawnDetachedPID(name string, args ...string) (int, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}
