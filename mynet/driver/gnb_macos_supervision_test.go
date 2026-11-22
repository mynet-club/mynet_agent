//go:build !windows

package driver

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// 验证 Stop() 会等待收到 SIGTERM 的慢退出 gnb 真正退出，而不是发完信号就返回。
func TestStopWaitsUntilSlowProcessExits(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	conf := t.TempDir()
	if err := os.WriteFile(filepath.Join(conf, "gnb.pid"), []byte("8888"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	alive := true
	kill0Count := 0
	d := &GNBDriver{
		NodeID:  1,
		ConfDir: conf,
		Binary:  "gnb",
		Exec: func(name string, args ...string) ([]byte, error) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case name == "systemctl":
				return []byte("not-found"), nil
			case name == "kill" && len(args) == 2 && args[0] == "-0":
				kill0Count++
				if alive {
					return nil, nil
				}
				return nil, os.ErrNotExist
			case name == "kill" && len(args) == 2 && args[0] == "-TERM":
				go func() {
					time.Sleep(150 * time.Millisecond)
					mu.Lock()
					alive = false
					mu.Unlock()
				}()
				return nil, nil
			case name == "ps":
				return []byte(""), nil
			}
			return nil, nil
		},
	}

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	mu.Lock()
	stillAlive := alive
	checks := kill0Count
	mu.Unlock()
	if stillAlive {
		t.Fatal("Stop 返回时原 gnb 进程尚未退出")
	}
	if checks < 2 {
		t.Fatalf("Stop 应轮询确认退出，实际 kill -0 仅 %d 次", checks)
	}
}

// 验证符号链接路径（类比 macOS /var/run/mynet/... -> /private/var/run/mynet/...）也能被收敛命中。
func TestConvergeMatchesSymlinkNormalizedConfDir(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	realBase := t.TempDir()
	realConf := filepath.Join(realBase, "real", "3764021915150630")
	if err := os.MkdirAll(realConf, 0o700); err != nil {
		t.Fatal(err)
	}
	linkBase := filepath.Join(realBase, "var_link")
	if err := os.Symlink(filepath.Join(realBase, "real"), linkBase); err != nil {
		t.Fatal(err)
	}
	linkConf := filepath.Join(linkBase, "3764021915150630")

	d := &GNBDriver{NodeID: 1, ConfDir: linkConf}
	cands := d.confDirCandidates()
	psOut := "4242 /opt/mynet/bin/gnb -c " + realConf + " --utc-managed\n" +
		"9999 /opt/mynet/bin/gnb -c /other/node\n"
	pids := parseSameConfdirPidsMulti(psOut, cands, "gnb")
	if len(pids) != 1 || pids[0] != 4242 {
		t.Fatalf("期望命中符号链接等价路径 pid=4242，实际=%v (cands=%v)", pids, cands)
	}
	_ = strconv.Itoa(pids[0])
}
