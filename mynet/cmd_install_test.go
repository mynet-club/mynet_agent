package mynet

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 平台无关的安装/卸载契约测试（本文件不带 build tag，四平台都要过）。
// 平台专属的装配测试见 cmd_install_{systemd,launchd}_test.go。

// 表头措辞：真实执行时**不得**出现「--dry-run 只看不做」—— 否则日志里一句
// 「只干跑」会让人以为动作没落地（2026-10-11 .182 升级的 opkg 日志实测踩过）。
func TestPrintInstallActionsHeaderWording(t *testing.T) {
	capture := func(ctx string, dry bool) string {
		old := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stdout = w
		printInstallActionsHeader(ctx, dry)
		_ = w.Close()
		os.Stdout = old
		b, _ := io.ReadAll(r)
		return string(b)
	}
	cases := []struct {
		ctx  string
		dry  bool
		want string
	}{
		{"OpenWrt 服务安装", false, "执行以下动作（OpenWrt 服务安装）："},
		{"OpenWrt 服务安装", true, "--dry-run 只看不做"},
		{"", false, "执行以下动作："},
	}
	for _, tc := range cases {
		got := capture(tc.ctx, tc.dry)
		if !strings.Contains(got, tc.want) {
			t.Errorf("ctx=%q dry=%v: 想要含 %q，实际 %q", tc.ctx, tc.dry, tc.want, got)
		}
		if !tc.dry && strings.Contains(got, "--dry-run") {
			t.Errorf("ctx=%q 真实执行不得出现 --dry-run，实际 %q", tc.ctx, got)
		}
	}
}

func TestInstallRequiresGateways(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	if rc := cmdInstall([]string{"--dry-run", "--home", home}); rc != 2 {
		t.Fatalf("缺 --gateways 应退出码 2（用法错），实际 %d", rc)
	}
}

// G-1：显式 --home 在没有 MYNET_HOME 时也必须能进入安装（dry-run 成功）。
// 旧实现先 requireHome 再解析 --home，导致「参数明明给了路径也进不去」。
func TestInstallHomeFlagWorksWithoutEnv(t *testing.T) {
	t.Setenv("MYNET_HOME", "")
	home := t.TempDir()
	rc := cmdInstall([]string{"--dry-run", "--home", home, "--gateways", "wss://gw.example.com/v1/node/connect"})
	if rc != 0 {
		t.Fatalf("--home 显式给出、无 MYNET_HOME 时应可 dry-run，实际 %d", rc)
	}
}

// C3：既无 --home 又无 MYNET_HOME 必须拒绝（禁止静默回退 prefix/exe/cwd）。
func TestInstallWithoutAnyHomeRefuses(t *testing.T) {
	t.Setenv("MYNET_HOME", "")
	rc := cmdInstall([]string{"--dry-run", "--gateways", "wss://gw.example.com/v1/node/connect"})
	if rc != 1 {
		t.Fatalf("无 home 应拒绝，退出码 1，实际 %d", rc)
	}
}

// home 唯一性：--home 与 MYNET_HOME 都给且不同 → 拒绝（不能有歧义）。
func TestInstallHomeConflictRefuses(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	rc := cmdInstall([]string{"--dry-run", "--home", t.TempDir(), "--gateways", "wss://gw.example.com/v1/node/connect"})
	if rc != 1 {
		t.Fatalf("--home 与 MYNET_HOME 冲突应拒绝，退出码 1，实际 %d", rc)
	}
}

func TestInstallDryRunDoesNotTouchFilesystem(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	prefix := filepath.Join(t.TempDir(), "prefix")
	t.Setenv("MYNET_HOME", home)

	rc := cmdInstall([]string{
		"--dry-run",
		"--prefix", prefix,
		"--home", home,
		"--gateways", "wss://gw.example.com/v1/node/connect",
	})
	if rc != 0 {
		t.Fatalf("dry-run 应退出码 0，实际 %d", rc)
	}
	for _, p := range []string{home, prefix} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("dry-run 不该创建 %s（实际 err=%v）", p, err)
		}
	}
}

func TestUninstallDryRunDoesNotTouchFilesystem(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(home, "conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	rc := cmdUninstall([]string{"--dry-run", "--home", home})
	if rc != 0 {
		t.Fatalf("dry-run 应退出码 0，实际 %d", rc)
	}
	if _, err := os.Stat(filepath.Join(home, "conf")); err != nil {
		t.Fatalf("dry-run 不该动已存在的目录: %v", err)
	}
}

func TestWriteEnvFileIs0600AndCarriesAllThreeVars(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "etc", "mynet", "mynetd.env")
	home := "/opt/mynet"
	gw := "wss://gw.example.com/v1/node/connect"
	binDir := "/opt/mynet/bin"

	if err := writeEnvFile(p, home, gw, binDir); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// 它指向的 MYNET_HOME 里含节点私钥（conf/mynet.conf），必须 0600
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("env 权限 %v，必须 0600", st.Mode().Perm())
	}
	b, _ := os.ReadFile(p)
	body := string(b)
	for _, want := range []string{"MYNET_HOME=" + home, "MYNET_GATEWAYS=" + gw, "MYNET_BIN=" + binDir + "/mynet"} {
		if !strings.Contains(body, want) {
			t.Errorf("env 缺 %q:\n%s", want, body)
		}
	}
}

// 从浏览器/网盘下载解压出来的二进制带「下载来源」标记：
// macOS 是 com.apple.quarantine xattr，Windows 是 Zone.Identifier ADS。
// 不清的话 launchd/SCM 可能拒绝执行 —— 服务「装得上起不来」，报错还不直观。
func TestClearDownloadMarks(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("quarantine 是 macOS 概念；windows 分支需在真机验证")
	}
	dir := t.TempDir()
	marked := filepath.Join(dir, "mynet")
	if err := os.WriteFile(marked, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("xattr", "-w", "com.apple.quarantine",
		"0001;00000000;test;00000000-0000-0000-0000-000000000000", marked).CombinedOutput(); err != nil {
		t.Fatalf("测试前置：xattr -w 失败: %v %s", err, out)
	}

	clean := filepath.Join(dir, "mynetd")
	if err := os.WriteFile(clean, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	warnings := clearDownloadMarks([]string{marked, clean, filepath.Join(dir, "不存在")})
	if len(warnings) != 0 {
		t.Fatalf("清除应成功且无警告，实际: %v", warnings)
	}
	if out, err := exec.Command("xattr", "-p", "com.apple.quarantine", marked).Output(); err == nil && len(out) > 0 {
		t.Fatalf("quarantine 未清除: %q", out)
	}
	// 未标记/不存在的文件必须静默通过（不误报、不中止安装）。
	// 注意只查 quarantine —— macOS 会给新落盘文件自动挂 com.apple.provenance，那不是下载标记。
	if out, err := exec.Command("xattr", "-p", "com.apple.quarantine", clean).Output(); err == nil && len(out) > 0 {
		t.Fatalf("干净文件不该有 quarantine: %q", out)
	}
}

// 「装完还在别的地方找二进制」的根治：install 必须把四件套置备到
// resolveGNBBinary 的第一优先标准位，且幂等、随源刷新。
func TestProvisionDriverGnbBin(t *testing.T) {
	prefix := t.TempDir()
	home := t.TempDir()
	srcDir := filepath.Join(prefix, "driver", "gnb", "bin")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, prog := range gnbSuite {
		if err := os.WriteFile(filepath.Join(srcDir, prog), []byte("v1-"+prog), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dstDir := filepath.Join(prefix, "bin") // D4-E：二进制归 <prefix>/bin

	copied, missing := provisionDriverGnbBin(prefix, home, filepath.Join(prefix, "sbin", "gnb"))
	if len(missing) != 0 {
		t.Fatalf("四件套都在源里，missing 应为空: %v", missing)
	}
	if len(copied) != len(gnbSuite) {
		t.Fatalf("copied=%v，应置备 %d 件", copied, len(gnbSuite))
	}
	for _, prog := range gnbSuite {
		b, err := os.ReadFile(filepath.Join(dstDir, prog))
		if err != nil {
			t.Fatalf("标准位缺 %s: %v", prog, err)
		}
		if string(b) != "v1-"+prog {
			t.Errorf("%s 内容不是源内容: %q", prog, b)
		}
		st, _ := os.Stat(filepath.Join(dstDir, prog))
		if st.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s 没有执行位: %v", prog, st.Mode())
		}
	}

	// 幂等：内容没变 → 一个都不重写
	copied, _ = provisionDriverGnbBin(prefix, home, filepath.Join(prefix, "sbin", "gnb"))
	if len(copied) != 0 {
		t.Fatalf("重复置备应全部跳过，实际重写: %v", copied)
	}

	// 包升级（源内容变了）→ 标准位跟着刷新
	if err := os.WriteFile(filepath.Join(srcDir, "gnb"), []byte("v2-gnb"), 0o755); err != nil {
		t.Fatal(err)
	}
	copied, _ = provisionDriverGnbBin(prefix, home, filepath.Join(prefix, "sbin", "gnb"))
	if len(copied) != 1 || filepath.Base(copied[0]) != "gnb" {
		t.Fatalf("只有 gnb 变了，应只重写它: %v", copied)
	}
	b, _ := os.ReadFile(filepath.Join(dstDir, "gnb"))
	if string(b) != "v2-gnb" {
		t.Fatalf("标准位没跟上源内容: %q", b)
	}
}
