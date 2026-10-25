package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ctl block 的落点必须随启动方式解析：只认 <conf>/gnb.map 在 OpenWrt 上恒失败，
// metrics 的 gnb 项没有 peers、node-status 链路全 unknown。
func TestCtlBlockPathPrefersConfMap(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "gnb.map")
	if err := os.WriteFile(want, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &GNBDriver{ConfDir: dir}
	if got := d.ctlBlockPath(); got != want {
		t.Fatalf("ctlBlockPath=%q want %q", got, want)
	}
}

func TestGnbCtlStatusArgsUsesResolvedMap(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "gnb.map")
	if err := os.WriteFile(want, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &GNBDriver{ConfDir: dir}
	d.Exec = func(name string, args ...string) ([]byte, error) {
		if name != "gnb_ctl" || len(args) != 4 || args[0] != "-b" || args[1] != want {
			return nil, fmt.Errorf("unexpected exec %s %v", name, args)
		}
		return []byte("conf->conf_dir[" + dir + "]\nnode_num[1]\n"), nil
	}
	if _, err := d.ctlStatus(); err != nil {
		t.Fatalf("ctlStatus: %v", err)
	}
}

func TestCtlBlockMatchesConf(t *testing.T) {
	dir := t.TempDir()
	d := &GNBDriver{ConfDir: dir}
	d.Exec = func(string, ...string) ([]byte, error) {
		return []byte("conf->conf_dir[" + dir + "]\nnode_num[1]\n"), nil
	}
	if !d.ctlBlockMatchesConf("/tmp/gnb.9001.map") {
		t.Fatal("conf_dir 相同应匹配")
	}
	other := &GNBDriver{ConfDir: filepath.Join(dir, "other")}
	other.Exec = d.Exec
	if other.ctlBlockMatchesConf("/tmp/gnb.9001.map") {
		t.Fatal("conf_dir 不同不应匹配")
	}
}
