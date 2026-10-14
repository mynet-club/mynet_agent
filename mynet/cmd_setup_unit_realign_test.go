package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// G3 缺口⑦：install 写单元时机早于 setup —— setup 必须把单元的 `-c` 归位。
//
// 已核实机制：writeSystemdUnits 是覆盖写；install 跑在 setup 之前时 home 无
// mode.json → gnbConfRel 回退 conf/local/default → 单元天生坏
// （188 实测 crash-loop 50 次：ExecStart=… -c ${MYNET_HOME}/conf/local/default）。

// setup 后单元内容必须指向 conf/<form>/<nodeId>（文件断言）。
// 顺带锁第二遍：换身份再 setup 也要跟着归位（归位挂在 setup，不只首次）。
func TestSetupRealignsGnbUnitToNodeConfDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)

	unit := filepath.Join(t.TempDir(), "mynet_gnb.service")
	// install 先于 setup 写出的坏单元（188 形态）
	bad := "[Service]\nExecStart=/usr/local/bin/gnb --systemd -c ${MYNET_HOME}/conf/local/default\n"
	if err := os.WriteFile(unit, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	old := gnbSystemdUnitPaths
	gnbSystemdUnitPaths = []string{unit}
	defer func() { gnbSystemdUnitPaths = old }()

	// setup --online：身份定型 → 单元 -c 归位到 conf/online/<nodeId>
	if rc := cmdSetupV2([]string{"--online", "--tenant", "t_9", "--node-id", "n7", "--vpn-type", "gnb"}); rc != 0 {
		t.Fatalf("setup --online rc=%d", rc)
	}
	b, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "-c ${MYNET_HOME}/conf/online/n7") {
		t.Fatalf("setup 后单元应指向 conf/online/n7:\n%s", b)
	}
	if strings.Contains(string(b), "conf/local/default") {
		t.Fatalf("坏的回退目标没被归位:\n%s", b)
	}

	// 换身份（local/别的节点）再 setup：仍要跟着归位
	if rc := cmdSetupV2([]string{"--local", "--node-id", "3001"}); rc != 0 {
		t.Fatalf("setup --local rc=%d", rc)
	}
	b, err = os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "-c ${MYNET_HOME}/conf/local/3001") {
		t.Fatalf("再 setup 后应指向 conf/local/3001:\n%s", b)
	}
}

// install 时 home 尚无 mode.json：单元照写但打印显式警告（缺口⑦）。
// 判据与警告文案走真函数（writeSystemdUnits 在 linux 专属文件里，调用点交叉编译核）。
func TestInstallWarnsWhenModeJsonMissing(t *testing.T) {
	home := t.TempDir()
	if !modeFileMissing(home) {
		t.Fatal("空 home 应判 mode.json 缺失")
	}
	warn := captureStderrFn(t, func() { warnGnbUnitConfRelPending(home) })
	if !strings.Contains(warn, "mynet_gnb.service 的 -c 待 mynet setup 后归位") {
		t.Fatalf("警告文案必须明说待归位: %q", warn)
	}
	if !strings.Contains(warn, "conf/local/default") {
		t.Fatalf("警告要指出暂指的目标: %q", warn)
	}

	// 有 mode.json（conf/ 新位）就不许告警
	if err := os.MkdirAll(filepath.Join(home, "conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "conf", "mode.json"), []byte(`{"mode":"local"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if modeFileMissing(home) {
		t.Fatal("conf/mode.json 在就不算缺失")
	}

	// 旧根位 mode.json 也算有（历史形态）
	home2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(home2, "mode.json"), []byte(`{"mode":"local"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if modeFileMissing(home2) {
		t.Fatal("旧根位 mode.json 也算身份已定")
	}
}
