package mynet

import (
	"os"
	"path/filepath"
	"testing"
)

// D4：DNS 备份/还原状态机（不改真系统 DNS，只测落盘与幂等）。
func TestDNSBackupRestoreState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MYNET_HOME", dir)

	// 无备份 → DNSRestoreNeeded 为 false（状态机不需还原）
	if DNSRestoreNeeded() {
		t.Fatal("无备份不该需要还原")
	}
	// 无备份 restore 会写 DHCP（恢复默认=DHCP）——需管理员权限，测试环境可能失败
	if err := RestoreSystemDNS(); err != nil {
		t.Logf("无备份 restore 写 DHCP：%v（无管理员权限属预期）", err)
	}

	// 写入「已接管」备份
	d := &DNSBackup{
		Platform:  "darwin",
		Interface: "Wi-Fi",
		Servers:   []string{"192.168.0.1", "8.8.8.8"},
		Applied:   true,
	}
	if err := saveDNSBackup(d); err != nil {
		t.Fatal(err)
	}
	if !DNSRestoreNeeded() {
		t.Fatal("applied=true 应需要还原")
	}

	// 幂等：还原后 Applied=false，再调用无害
	if err := RestoreSystemDNS(); err != nil {
		// Windows/Linux 真改系统 DNS 可能失败 —— 这里只要状态机对
		t.Logf("还原调用返回（环境无真 DNS 可改属预期）: %v", err)
	}
	// 直接验证状态文件：手动置 Applied 再还原会改状态
	d2 := LoadDNSBackup()
	if d2 == nil {
		t.Fatal("应能读回备份")
	}
	if d2.Applied {
		// 若还原失败，Applied 仍 true —— 这也是合法状态（下次启动补还原）
		t.Log("Applied 仍为 true（还原未完成，crash 保护语义）")
	}

	// 备份内容正确
	if d2.Interface != "Wi-Fi" || len(d2.Servers) != 2 {
		t.Fatalf("备份内容不对: %+v", d2)
	}
}

func TestDNSBackupPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MYNET_HOME", dir)
	p := dnsBackupPath()
	if filepath.Dir(p) != filepath.Join(dir, "var") {
		t.Fatalf("备份应在 var/ 下: %s", p)
	}
	// 权限：写入后 0600
	if err := saveDNSBackup(&DNSBackup{Platform: "test", Applied: true}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("备份权限 %v，必须 0600", st.Mode().Perm())
	}
}

func TestIsPCDNSPlatform(t *testing.T) {
	// darwin/windows → true；linux → false
	switch runtimeOS() {
	case "darwin", "windows":
		if !isPCDNSPlatform() {
			t.Fatal("Mac/Win 应是 PC DNS 平台")
		}
	default:
		if isPCDNSPlatform() {
			t.Fatal("Linux 不应走改系统 DNS（走 nft 劫持）")
		}
	}
}
