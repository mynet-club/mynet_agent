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

	realConfResolved, err := filepath.EvalSymlinks(realConf)
	if err != nil {
		t.Fatal(err)
	}
	d := &GNBDriver{NodeID: 1, ConfDir: linkConf}
	cands := d.confDirCandidates()
	psOut := "4242 /opt/mynet/bin/gnb -c " + realConfResolved + " --utc-managed\n" +
		"9999 /opt/mynet/bin/gnb -c /other/node\n"
	pids := parseSameConfdirPidsMulti(psOut, cands, "gnb")
	if len(pids) != 1 || pids[0] != 4242 {
		t.Fatalf("期望命中符号链接等价路径 pid=4242，实际=%v (cands=%v)", pids, cands)
	}
	_ = strconv.Itoa(pids[0])
}

// 回归 2026-11-23 .181 实测：旧 gnb 退出后，其子进程 gnb_es 继承了 utun7 与 UDP 9016 fd
// 并以 /opt/mynet/conf/online/<nodeID>/gnb.map 残留，导致新 gnb 抢不到 9016 且双 utun 并存。
func TestConvergeReapsOrphanGnbEsFromLegacyConfDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	nodeID := int64(3764021915150630)
	runConf := filepath.Join(t.TempDir(), "3764021915150630")
	legacyConf := filepath.Join(home, "conf", "online", "3764021915150630")

	d := &GNBDriver{NodeID: nodeID, ConfDir: runConf}
	cands := d.confDirCandidates()
	psOut := "12672 /opt/mynet/bin/gnb_es -s -L -b " + legacyConf + "/gnb.map\n" +
		"31303 /opt/mynet/bin/gnb -c " + runConf + "\n" +
		"99999 /opt/mynet/bin/gnb_es -s -L -b " + filepath.Join(home, "conf", "online", "1000000000000001") + "/gnb.map\n"

	// keepPid==0（Stop / 启动前收敛）：同时命中同节点孤儿 gnb_es 与 gnb，不碰其它节点
	pids := parseSameConfdirPidsMulti(psOut, cands, "gnb", "gnb_es")
	if len(pids) != 2 || pids[0] != 12672 || pids[1] != 31303 {
		t.Fatalf("期望命中 [12672 31303]，实际=%v (cands=%v)", pids, cands)
	}
}
