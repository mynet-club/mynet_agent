package mynet

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

func TestFetchProxyIPListDownloadsAndCaches(t *testing.T) {
	body := "# comment\n1.0.0.0/8\n2.0.0.0/8\n"
	sum := sha256.Sum256([]byte(body))
	ref := &domain.ProxyIPListRef{
		Source: domain.ProxyListInternational,
		URL:    "", // 填下面的 test server
		SHA256: hex.EncodeToString(sum[:]),
		Bytes:  int64(len(body)),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	ref.URL = srv.URL

	dir := t.TempDir()
	cache := ProxyListCachePath(dir, domain.ProxyListInternational)
	path, err := FetchProxyIPList(ref, cache, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if path != cache {
		t.Fatalf("path=%s", path)
	}
	got, err := os.ReadFile(cache)
	if err != nil || string(got) != body {
		t.Fatalf("缓存内容不对: %q %v", got, err)
	}
	if n := CountIPListLines(got); n != 2 {
		t.Fatalf("有效行=%d", n)
	}

	// 第二次应命中缓存（即使 server 挂了也不重下）
	srv.Close()
	if _, err := FetchProxyIPList(ref, cache, srv.Client()); err != nil {
		t.Fatalf("缓存命中仍失败: %v", err)
	}
}

func TestFetchProxyIPListRejectsBadHash(t *testing.T) {
	body := "1.0.0.0/8\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	ref := &domain.ProxyIPListRef{
		Source: domain.ProxyListChina,
		URL:    srv.URL,
		SHA256: strings.Repeat("0", 64),
	}
	_, err := FetchProxyIPList(ref, ProxyListCachePath(t.TempDir(), "china"), srv.Client())
	if err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatalf("坏 sha256 应被拒, got %v", err)
	}
}

func TestWriteNftSetFragment(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "interip.txt")
	if err := os.WriteFile(cache, []byte("1.0.0.0/8\n# x\n10.0.0.0/8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteNftSetFragment(cache); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cache + ".nft")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"add set ip mynet_proxy mynet_proxy { type ipv4_addr; flags interval; }",
		"add element ip mynet_proxy mynet_proxy { 1.0.0.0/8 }",
		"add element ip mynet_proxy mynet_proxy { 10.0.0.0/8 }",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺 %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "# x") && strings.Contains(text, "add element") {
		// 注释不得变成 element
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "add element") && strings.Contains(line, "#") {
				t.Fatalf("注释进了 element: %s", line)
			}
		}
	}
}

func TestLoadIPSetFileCmds(t *testing.T) {
	snap := &shared.SystemSnapshot{Capabilities: map[string]bool{"nft": true}}
	cmds := LoadIPSetFileCmds(snap, "/var/proxy-lists/interip.txt")
	if len(cmds) != 1 || cmds[0].argv[0] != "nft" {
		t.Fatalf("nft 路径不对: %v", cmds)
	}
	snapI := &shared.SystemSnapshot{Capabilities: map[string]bool{"iptables": true}}
	cmds = LoadIPSetFileCmds(snapI, "/var/proxy-lists/chinaip.txt")
	if len(cmds) == 0 || !strings.Contains(strings.Join(cmds[len(cmds)-1].argv, " "), "ipset restore") {
		t.Fatalf("ipset 路径不对: %v", cmds)
	}
}
