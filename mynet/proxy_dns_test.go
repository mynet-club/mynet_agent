package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteResolvConfRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(path, []byte("nameserver 1.1.1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old, err := WriteResolvConf(path, "8.8.8.8")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(old, "1.1.1.1") {
		t.Fatalf("old=%q", old)
	}
	cur, _ := os.ReadFile(path)
	if !strings.Contains(string(cur), "nameserver 8.8.8.8") {
		t.Fatalf("cur=%q", cur)
	}
	if err := RestoreResolvConf(path, old); err != nil {
		t.Fatal(err)
	}
	cur, _ = os.ReadFile(path)
	if !strings.Contains(string(cur), "1.1.1.1") {
		t.Fatalf("恢复失败: %q", cur)
	}
}

func TestWriteDNSSplitConf(t *testing.T) {
	dir := t.TempDir()
	gfw := filepath.Join(dir, "gfwlist.conf")
	extra := filepath.Join(dir, "extra.conf")
	if err := os.WriteFile(gfw, []byte("# gfw\n.google.com\nfacebook.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extra, []byte("github.com\n# skip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "dnsmasq.d", "mynet-split.conf")
	err := WriteDNSSplitConf(DNSSplitOptions{
		DomesticServers: []string{"223.5.5.5", "119.29.29.29"},
		ForeignServer:   "8.8.8.8",
		DomainFiles:     []string{gfw, extra},
		ConfPath:        out,
		FallbackDomain:  "dns.mynet.club",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"server=223.5.5.5",
		"server=/google.com/8.8.8.8",
		"server=/facebook.com/8.8.8.8",
		"server=/github.com/8.8.8.8",
		"server=dns.mynet.club",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺 %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "skip") {
		t.Fatalf("注释不得进入配置:\n%s", text)
	}
}

func TestDNSSplitReloadUsesPidfileNotKillall(t *testing.T) {
	cmds := DNSSplitReloadCmds("/var/run/dnsmasq.pid")
	joined := ""
	for _, c := range cmds {
		joined += strings.Join(c, " ") + "\n"
	}
	if strings.Contains(joined, "killall") || strings.Contains(joined, "pkill") {
		t.Fatalf("禁止按名字杀进程:\n%s", joined)
	}
	if !strings.Contains(joined, "kill -HUP") {
		t.Fatalf("应有 pidfile HUP:\n%s", joined)
	}
	if !strings.Contains(joined, "systemctl reload dnsmasq") {
		t.Fatalf("应优先服务管理器 reload:\n%s", joined)
	}
}
