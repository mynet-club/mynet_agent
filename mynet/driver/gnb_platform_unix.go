//go:build !windows

package driver

import "strconv"

// gnbExeName 平台可执行文件名。
func gnbExeName() string { return "gnb" }

// gnbPlatformConfKeys 平台专有 node.conf 键（unix 无）。
func gnbPlatformConfKeys() string { return "" }

// gnbDaemonLine gnb 的 daemon 键。
// Unix：daemon on —— 自己起时 gnb 双 fork、父进程立即 exit(0)，Start() 不阻塞；
// 被 systemd 以 --systemd 拉起时该键被忽略。
func gnbDaemonLine() string { return "daemon on\n" }

// pidAliveOS 进程存在性。Unix 用 kill -0（只探测，不发信号）。
func (d *GNBDriver) pidAliveOS(pid int) bool {
	_, err := d.Exec("kill", "-0", strconv.Itoa(pid))
	return err == nil
}

// pidKillOS 停进程。Unix 发 SIGTERM（gnb 装了 handler）；不按名字杀。
func (d *GNBDriver) pidKillOS(pid int) error {
	_, err := d.Exec("kill", "-TERM", strconv.Itoa(pid))
	return err
}

// spawnGnb 拉起 gnb。Unix：Spawn（等父进程 daemon 化退出），返回 0（pid 由 gnb.pid 记）。
func (d *GNBDriver) spawnGnb(bin string) (int, error) {
	if err := d.Spawn(bin, "-c", d.ConfDir); err != nil {
		return 0, err
	}
	return 0, nil
}

// writePidAfterSpawn unix 上 gnb 自己写 gnb.pid，无需我们补。
func (d *GNBDriver) writePidAfterSpawn(pid int) {}
