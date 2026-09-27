package driver

import (
	"os"
	"os/exec"
	"path/filepath"
)

func writeFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func execCombined(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// spawnDetached 启动一个会**自我 daemon 化**的进程。
//
// ⚠️ 这类进程不能用 execCombined：CombinedOutput 会等 stdout/stderr 管道关闭。
// gnb 在 `daemon on` 时双 fork（gnb_daemon.c:36-48），父进程立即 exit(0)，
// 但**子进程继承了那根管道** → CombinedOutput 要等到守护进程退出才返回。
// 实测：gnb 已经起来、gnb_tun 已 UP、ppid 已变成 1，而 `mynet service start gnb`
// 挂了 349 秒不返回。
//
// 这里把 stdio 全留 nil（os/exec 此时接到 /dev/null），没有管道要 drain，
// Wait() 在父进程 exit 后立即返回；父进程若在 daemon 化**之前**就失败
// （配置解析错 → exit(1)），Wait() 也能把那个错误带回来。
// gnb 在 daemon 模式下本来就把 console 日志关了（gnb_core.c:113-115），不丢信息。
// spawnDetached 启动一个会**自我 daemon 化**的进程。
//
// ⚠️ 这类进程不能用 execCombined：CombinedOutput 会等 stdout/stderr 管道关闭。
// gnb 在 `daemon on` 时双 fork（gnb_daemon.c:36-48），父进程立即 exit(0)，
// 但**子进程继承了那根管道** → CombinedOutput 要等到守护进程退出才返回。
//
// Unix：std 留 nil + Wait（捕获 daemon 化前的启动失败）。
// Windows：gnb **不 daemon**、前台不退且**可能不写 gnb.pid** —— 只 Start 不 Wait，
// 并把 cmd 的 PID 返回给调用方写进 pid 文件（183 实测：进程活了但 waitAlive 误判死）。
// spawnDetached / spawnDetachedPID 见 spawn_{unix,windows}.go。

func writeFile0600(path string, data []byte) error {
	return writeFile(path, data, 0o600)
}
