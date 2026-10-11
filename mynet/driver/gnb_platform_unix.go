//go:build !windows

package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

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

// pidKill9OS 不可优雅退出时的硬杀（killUntilDead 的第二段）。
func (d *GNBDriver) pidKill9OS(pid int) error {
	_, err := d.Exec("kill", "-KILL", strconv.Itoa(pid))
	return err
}

// lockLifecycle 取「每 confdir 一把」的跨进程排他锁（flock）。
//
// 用 flock 而不是锁文件存在性：进程被 kill -9 时内核自动释放，不会留下永久死锁。
// 非阻塞轮询 + 截止时间 —— 等不到就报错，绝不硬闯（重叠正是要防的）。
func (d *GNBDriver) lockLifecycle() (func(), error) {
	if d.ConfDir == "" {
		return func() {}, nil
	}
	lockPath := filepath.Join(d.ConfDir, ".gnb-lifecycle.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("gnb 生命周期锁: %w", err)
	}
	deadline := time.Now().Add(lifecycleLockTimeout)
	for {
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return func() {
				_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("gnb 生命周期锁等待超时（%s）：另一个启停/重载仍在进行", lockPath)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// sameConfdirPidsOS 列出「命令行指向本 ConfDir 的 gnb」进程。
//
// 用 `ps -Ao pid=,args=`（Linux 与 macOS/BSD 都认 args 这个别名）而非 /proc ——
// macOS 没有 /proc，而这条判据要跨 unix 一起用。
func (d *GNBDriver) sameConfdirPidsOS() []int {
	out, err := d.Exec("ps", "-Ao", "pid=,args=")
	if err != nil {
		return nil
	}
	return parseSameConfdirPids(string(out), d.ConfDir, gnbExeName())
}
