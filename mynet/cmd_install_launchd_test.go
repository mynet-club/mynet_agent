//go:build darwin

package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// darwin(launchd) 装配专属测试。

// 与服务名矩阵（lifecycle-contract §3）对齐：Label 与 plist 目录必须在位。
// 这些符号按 build tag 只在 darwin 编译，故断言放在本文件而非共享 fixture。
func TestLaunchdRegistrationNamesMatchContract(t *testing.T) {
	if LabelMynetd != "com.mynet.mynetd" {
		t.Errorf("LabelMynetd=%q，合同=com.mynet.mynetd", LabelMynetd)
	}
	if launchdPlistDir != "/Library/LaunchDaemons" {
		t.Errorf("launchdPlistDir=%q，合同=/Library/LaunchDaemons", launchdPlistDir)
	}
}

// launchd plist 的 XML 转义：路径/URL 里有 & 时不能把 plist 写坏。
func TestXMLesc(t *testing.T) {
	got := xmlEsc(`a&b<c>d"e`)
	want := "a&amp;b&lt;c&gt;d&quot;e"
	if got != want {
		t.Fatalf("xmlEsc=%q want %q", got, want)
	}
}

// plist 必须含 Label、绝对路径的 ProgramArguments、KeepAlive ——
// 缺任何一项都是「装得上起不来」。
func TestWriteLaunchdPlistShape(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"MYNET_HOME":     "/opt/mynet",
		"MYNET_GATEWAYS": "wss://gw.example/1",
		"MYNET_BIN":      "/opt/mynet/bin/mynet",
		"PATH":           "/usr/sbin:/usr/bin:/sbin:/bin",
	}
	err := writeLaunchdPlist("com.mynet.gnb", dir,
		[]string{"/usr/local/bin/gnb", "--systemd", "-c", "/opt/mynet/var/gnb"},
		env, "/var/log/mynet/gnb.log", "/var/log/mynet/gnb.err", true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "com.mynet.gnb.plist"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"<string>com.mynet.gnb</string>",
		"<string>/usr/local/bin/gnb</string>",
		"<string>--systemd</string>", // 前台受监管，不自 daemon
		"<key>KeepAlive</key>",
		"<key>RunAtLoad</key>",
		"<key>MYNET_HOME</key>",
		"<string>/opt/mynet</string>",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("plist 缺 %q:\n%s", want, s)
		}
	}
	// 含 & 的 gateways 必须被转义
	if err := writeLaunchdPlist("com.x", dir, []string{"/bin/true"},
		map[string]string{"MYNET_GATEWAYS": "wss://a?b=1&c=2"}, "/tmp/o", "/tmp/e", true); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(dir, "com.x.plist"))
	if !strings.Contains(string(b2), "wss://a?b=1&amp;c=2") {
		t.Fatalf("URL 里的 & 未转义:\n%s", b2)
	}
}

// 托盘是交互式进程：KeepAlive 必须为 false ——
// 为 true 时用户从菜单退出会被 launchd 复活，属于骚扰级 bug。
func TestWriteLaunchdPlistKeepAlive(t *testing.T) {
	dir := t.TempDir()
	if err := writeLaunchdPlist("club.mynet.tray", dir, []string{"/opt/mynet/bin/mynet_tray"},
		map[string]string{"MYNET_HOME": "/var/lib/mynet"}, "/dev/null", "/dev/null", false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "club.mynet.tray.plist"))
	s := string(b)
	if !strings.Contains(s, "<key>KeepAlive</key>\n  <false/>") {
		t.Fatalf("托盘 plist 的 KeepAlive 必须是 <false/>:\n%s", s)
	}
	if !strings.Contains(s, "<key>RunAtLoad</key>\n  <true/>") {
		t.Fatalf("托盘 plist 缺 RunAtLoad:\n%s", s)
	}
}
