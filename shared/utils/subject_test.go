package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R2：异构建 ctl 必须被拒 —— 同目录才认。
func TestFindSiblingSameDirOnly(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()

	write := func(p string) {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "gnb"))
	write(filepath.Join(other, "gnb_ctl")) // 异构建

	if got := findSibling(dir, "gnb"); got == "" {
		t.Fatal("同目录 gnb 应能找到")
	}
	if got := findSibling(dir, "gnb_ctl"); got != "" {
		t.Fatalf("同目录没有 gnb_ctl，应返回空，却得到 %s", got)
	}

	write(filepath.Join(dir, "gnb_ctl"))
	if got := findSibling(dir, "gnb_ctl"); got == "" {
		t.Fatal("同目录 gnb_ctl 应能找到")
	}
}

// R6：Windows .exe 后缀。
func TestFindSiblingExe(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gnb_ctl.exe"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findSibling(dir, "gnb_ctl"); got == "" {
		t.Fatal("应认 gnb_ctl.exe")
	}
}

func TestParseGnbCmd(t *testing.T) {
	exe, conf, systemd, index := parseGnbCmd(
		"/var/lib/mynet/driver/gnb/bin/gnb -c /var/lib/mynet/driver/gnb/conf/3764021915150630")
	if !strings.HasSuffix(exe, "gnb") {
		t.Fatalf("exe=%s", exe)
	}
	if conf != "/var/lib/mynet/driver/gnb/conf/3764021915150630" {
		t.Fatalf("conf=%s", conf)
	}
	if systemd || index {
		t.Fatal("不应识别 systemd/index")
	}

	_, conf, systemd, index = parseGnbCmd("/opt/mynet/bin/gnb --systemd -c /opt/mynet/var/gnb")
	if conf != "/opt/mynet/var/gnb" || !systemd || index {
		t.Fatalf("systemd 解析错: conf=%s systemd=%v index=%v", conf, systemd, index)
	}

	_, _, _, index = parseGnbCmd("/usr/local/mynet/driver/gnb/bin/gnb -P -l 9016")
	if !index {
		t.Fatal("-P 应识别为 index")
	}
}

func TestLooksLikeGnbCmd(t *testing.T) {
	if !looksLikeGnbCmd("/usr/bin/gnb -c /tmp/x") {
		t.Fatal("应认 gnb")
	}
	if looksLikeGnbCmd("/usr/bin/gnb_ctl -b /tmp/map") {
		t.Fatal("gnb_ctl 不是主进程")
	}
	if looksLikeGnbCmd("bash -c echo SUBJECT SNAPSHOT gnb") {
		t.Fatal("取证脚本应排除")
	}
}

// R4：listen 端口解析。
func TestListenPort(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "node.conf")
	if err := os.WriteFile(p, []byte("nodeid 1\nlisten 0.0.0.0:9016\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := listenPort(p); got != "9016" {
		t.Fatalf("got %q", got)
	}
	if err := os.WriteFile(p, []byte("listen 9020\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := listenPort(p); got != "9020" {
		t.Fatalf("bare port got %q", got)
	}
}

// stubScan 注入进程扫描桩，避免单测依赖本机真 gnb。
func stubScan(t *testing.T, procs []GnbProc, err error) {
	t.Helper()
	old := scanGnbProcsFunc
	scanGnbProcsFunc = func() ([]GnbProc, error) { return procs, err }
	t.Cleanup(func() { scanGnbProcsFunc = old })
}

// R1 有进程时必须走 process 源；R2/R5 无同目录 ctl 时 tools_missing 显式。
func TestResolveSubjectProcessAnchor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)

	bin := filepath.Join(home, "driver", "gnb", "bin")
	conf := filepath.Join(home, "driver", "gnb", "conf", "999000111222333")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(conf, 0o755); err != nil {
		t.Fatal(err)
	}
	gnb := filepath.Join(bin, "gnb")
	if err := os.WriteFile(gnb, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf, "node.conf"),
		[]byte("nodeid 999000111222333\nlisten 0.0.0.0:9199\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 故意不放 gnb_ctl

	stubScan(t, []GnbProc{{
		PID: 4242, Exe: gnb, ConfDir: conf,
	}}, nil)

	s, err := ResolveSubject(ResolveOpts{PID: 4242, Home: home})
	if err != nil {
		t.Fatalf("ResolveSubject err: %v", err)
	}
	if s.Source != "process" {
		t.Fatalf("Source=%s 想要 process", s.Source)
	}
	if filepath.Clean(s.ConfDir) != filepath.Clean(conf) {
		t.Fatalf("ConfDir=%s want %s", s.ConfDir, conf)
	}
	if s.GnbCtl != "" {
		t.Fatalf("无同目录 ctl 时 GnbCtl 必须为空，得到 %s", s.GnbCtl)
	}
	if len(s.ToolsMissing) == 0 {
		t.Fatal("R5：tools_missing 必须显式")
	}
	found := false
	for _, w := range s.Warnings {
		if strings.Contains(w, "禁止使用其它树的 ctl") {
			found = true
		}
	}
	if !found {
		t.Fatalf("警告应含禁止捡异构建，got %v", s.Warnings)
	}
}

// 同目录有 ctl 时 tools_missing 为空。
func TestResolveSubjectSameBuildOK(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)

	bin := filepath.Join(home, "driver", "gnb", "bin")
	conf := filepath.Join(home, "driver", "gnb", "conf", "5555666677778888")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(conf, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"gnb", "gnb_ctl", "gnb_es"} {
		if err := os.WriteFile(filepath.Join(bin, n), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	stubScan(t, []GnbProc{{
		PID: 100, Exe: filepath.Join(bin, "gnb"), ConfDir: conf,
	}}, nil)

	s, err := ResolveSubject(ResolveOpts{PID: 100, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ToolsMissing) != 0 {
		t.Fatalf("同套件齐全不应 tools_missing: %v", s.ToolsMissing)
	}
	if s.GnbCtl == "" {
		t.Fatal("应解析到同目录 gnb_ctl")
	}
	if s.NodeID != "5555666677778888" {
		t.Fatalf("NodeID=%s", s.NodeID)
	}
}

// 无进程时 fallback 必须带 warning，且 tools_missing 显式。
func TestResolveSubjectFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)

	if err := os.MkdirAll(filepath.Join(home, "driver", "gnb", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "driver", "gnb", "bin", "gnb"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "mode.json"),
		[]byte(`{"schema":2,"mode":"online","active":{"tenant_id":"t","node_id":"4242424242424242"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stubScan(t, nil, nil)

	s, err := ResolveSubject(ResolveOpts{Home: home})
	if s == nil {
		t.Fatalf("应有 Subject，err=%v", err)
	}
	if s.Source != "fallback" {
		t.Fatalf("Source=%s", s.Source)
	}
	if s.NodeID != "4242424242424242" {
		t.Fatalf("NodeID=%s 来自 mode.json", s.NodeID)
	}
	if len(s.ToolsMissing) == 0 {
		t.Fatal("fallback 也必须 tools_missing 显式")
	}
	if err == nil {
		t.Fatal("工具不完整时应返回错误")
	}
	if len(s.Warnings) == 0 {
		t.Fatal("fallback 必须带 warning（R1 不满足）")
	}
}

// 多主体时不得猜，要列出 candidates。
func TestResolveSubjectMulti(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)

	stubScan(t, []GnbProc{
		{PID: 11, Exe: "/a/bin/gnb", ConfDir: "/a/conf/1111111111111111"},
		{PID: 22, Exe: "/b/bin/gnb", ConfDir: "/b/conf/2222222222222222"},
	}, nil)

	s, err := ResolveSubject(ResolveOpts{Home: home})
	if err == nil {
		t.Fatal("多主体应报错要求指定")
	}
	if s == nil || len(s.Candidates) < 2 {
		t.Fatalf("应列出 candidates，got %+v", s)
	}
}

// index 进程（-P）不是节点主体，应被过滤。
func TestResolveSubjectSkipsIndex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)

	bin := filepath.Join(home, "b")
	conf := filepath.Join(home, "c", "3333444455556666")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(conf, 0o755); err != nil {
		t.Fatal(err)
	}
	gnb := filepath.Join(bin, "gnb")
	if err := os.WriteFile(gnb, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gnb_ctl"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	stubScan(t, []GnbProc{
		{PID: 1, Exe: gnb, ConfDir: conf, Index: false},
		{PID: 2, Exe: "/usr/local/mynet/gnb", ConfDir: "", Index: true},
	}, nil)

	s, err := ResolveSubject(ResolveOpts{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if s.PID != 1 {
		t.Fatalf("应选节点进程 pid=1，got %d", s.PID)
	}
}

// 188 形态：--systemd + 非标准 conf 目录 /opt/mynet/var/gnb。
func TestResolveSubjectSystemdVarGnb(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)

	conf := filepath.Join(home, "var", "gnb")
	if err := os.MkdirAll(conf, 0o755); err != nil {
		t.Fatal(err)
	}
	gnb := filepath.Join(home, "bin", "gnb")
	if err := os.MkdirAll(filepath.Dir(gnb), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gnb, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf, "node.conf"),
		[]byte("nodeid 7676068715291109\nlisten 0.0.0.0:9016\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stubScan(t, []GnbProc{{
		PID: 9, Exe: gnb, ConfDir: conf, Systemd: true,
	}}, nil)

	s, err := ResolveSubject(ResolveOpts{PID: 9, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(s.ConfDir) != filepath.Clean(conf) {
		t.Fatalf("ConfDir=%s 应是进程 -c 的 %s（不是 driver 树）", s.ConfDir, conf)
	}
	if !s.Systemd {
		t.Fatal("应识别 --systemd")
	}
}
