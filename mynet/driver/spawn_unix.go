//go:build !windows

package driver

import "os/exec"

// spawnDetached 启动会自我 daemon 化的进程。
// Unix：等父进程 daemon 化退出；std 留 nil（/dev/null），无管道可 drain。
func spawnDetached(name string, args ...string) error {
	_, err := spawnDetachedPID(name, args...)
	return err
}

// spawnDetachedPID 同 spawnDetached，返回 PID（unix 上由 gnb 自己写 pid 文件）。
func spawnDetachedPID(name string, args ...string) (int, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	if err := cmd.Wait(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}
