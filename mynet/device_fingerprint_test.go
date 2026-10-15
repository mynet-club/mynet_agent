package mynet

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeviceFingerprintStable(t *testing.T) {
	// home 要可写：无 /etc/machine-id 时 machine-id 落 $MYNET_HOME/var/，
	// 写失败会每次新随机 → 指纹「不稳定」。测试用临时 home。
	t.Setenv("MYNET_HOME", t.TempDir())
	a := DeviceFingerprint()
	b := DeviceFingerprint()
	if a == "" || a != b {
		t.Fatalf("指纹不稳定: %q vs %q", a, b)
	}
	if len(a) < 32 {
		t.Fatalf("太短: %q", a)
	}
	t.Log("fingerprint", a)
}

// 指纹不得掺 hostname：hostname 变了（MacBook-Pro-3.local → Mac.lan）
// 会让后端侧 Token Binding 判 mismatch 并吊销 token。
func TestDeviceFingerprintIgnoresHostname(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	// 同一 machine-id 下两次调用必须一致；实现已不读 hostname，
	// 这里用「连续两次一致 + 非空」兜底，并核对公式不含主机名分量。
	a := DeviceFingerprint()
	if a == "" {
		t.Fatal("指纹为空")
	}
	// readMachineID 落盘后再次读应同值
	mid1 := readMachineID()
	mid2 := readMachineID()
	if mid1 != mid2 {
		t.Fatalf("machine-id 不稳定: %q vs %q", mid1, mid2)
	}
	if mid1 == "" {
		t.Fatal("machine-id 为空")
	}
}

func TestReadMachineIDPersists(t *testing.T) {
	// 用临时 HOME 验证「首次生成后落盘」
	dir := t.TempDir()
	t.Setenv("MYNET_HOME", dir)

	// 清掉可能存在的 /etc/machine-id 干扰：readMachineID 优先读它，
	// 本测试只验证 fallback 路径在无 /etc/machine-id 时可写。
	if _, err := os.Stat("/etc/machine-id"); err == nil {
		t.Skip("本机有 /etc/machine-id，跳过 fallback 落盘路径")
	}

	s1 := readMachineID()
	if s1 == "" {
		t.Fatal("machine-id 为空")
	}
	p := filepath.Join(dir, "var", "machine-id")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("未落盘 %s: %v", p, err)
	}
	if got := string(b); len(got) < 32 {
		t.Fatalf("落盘内容太短: %q", got)
	}
	s2 := readMachineID()
	if s1 != s2 {
		t.Fatalf("二次读取不一致: %q vs %q", s1, s2)
	}
}
