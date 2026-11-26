package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

func TestParseProxyConfMetrics(t *testing.T) {
	dir := t.TempDir()

	// 1. 文件不存在
	got := parseProxyConfMetrics(filepath.Join(dir, "missing.conf"))
	if got.Configured || got.Enabled || got.IngressOpen || got.Role != "" {
		t.Fatalf("missing file should return zero metrics, got %+v", got)
	}

	// 2. server + ingress_open on
	srvPath := filepath.Join(dir, "server.conf")
	if err := os.WriteFile(srvPath, []byte("# comment\nrole server\nproxy on\ningress_open on\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = parseProxyConfMetrics(srvPath)
	if !got.Configured || !got.Enabled || !got.IngressOpen || got.Role != "server" {
		t.Fatalf("unexpected server metrics: %+v", got)
	}

	// 3. proxy off + ingress_open off
	offPath := filepath.Join(dir, "off.conf")
	if err := os.WriteFile(offPath, []byte("proxy off\ningress_open off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = parseProxyConfMetrics(offPath)
	if !got.Configured || got.Enabled || got.IngressOpen || got.Role != "" {
		t.Fatalf("unexpected off metrics: %+v", got)
	}

	// 4. client only (no ingress_open line → false)
	cliPath := filepath.Join(dir, "client.conf")
	if err := os.WriteFile(cliPath, []byte("role client\nproxy on\n[default]\nexit 1001 w=10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = parseProxyConfMetrics(cliPath)
	if !got.Configured || !got.Enabled || got.IngressOpen || got.Role != "client" {
		t.Fatalf("unexpected client metrics: %+v", got)
	}
}

func TestMergeCloudExitPermission(t *testing.T) {
	permOn := &domain.ProxyPermission{Enabled: true}
	permOff := &domain.ProxyPermission{Enabled: false}

	// 1. 本地无 proxy.json + 云端有出口许可 → role=server, ingress_open on
	got := mergeCloudExitPermission(nil, permOn)
	if got == nil || got.Role != "server" {
		t.Fatalf("expected synthesized role=server, got %+v", got)
	}
	rendered := RenderGnbProxyConf(got)
	if !strings.Contains(rendered, "role server") || !strings.Contains(rendered, "ingress_open on") {
		t.Fatalf("rendered server proxy.conf missing expected lines:\n%s", rendered)
	}

	// 2. 本地为 client + 云端有出口许可 → 升级为 both，保留原 exits 且 ingress_open on
	localClient := &domain.PluginProxyConfig{
		Role:  "client",
		Exits: []domain.ProxyExit{{NodeID: "4997342800601732", Weight: 10, Enabled: true}},
	}
	got = mergeCloudExitPermission(localClient, permOn)
	if got == nil || got.Role != "both" || len(got.Exits) != 1 {
		t.Fatalf("expected upgraded role=both with exits preserved, got %+v", got)
	}
	if localClient.Role != "client" {
		t.Fatalf("original localClient must not be mutated")
	}
	rendered = RenderGnbProxyConf(got)
	if !strings.Contains(rendered, "role both") || !strings.Contains(rendered, "ingress_open on") || !strings.Contains(rendered, "4997342800601732") {
		t.Fatalf("rendered both proxy.conf unexpected:\n%s", rendered)
	}

	// 3. 云端未授权 → 保持 nil / 原样（不误开 ingress）
	if mergeCloudExitPermission(nil, nil) != nil {
		t.Fatalf("expected nil when both local and cloud are nil")
	}
	if mergeCloudExitPermission(nil, permOff) != nil {
		t.Fatalf("expected nil when cloud perm disabled")
	}
}
