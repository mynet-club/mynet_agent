package mynet

// ApplyFromActive：local intent → gnb 投影。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

func TestApplyFromActiveLocalProjectsGnb(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	t.Setenv("MYNET_RUN_ROOT", t.TempDir()) // 测试无 root，不写 /var/run

	if rc := cmdSetupV2([]string{"--local", "--node-id", "3001", "--overlay", "10.182.236.1/32"}); rc != 0 {
		t.Fatalf("setup rc=%d", rc)
	}
	// setup 不生成密钥 —— 显式 generate（或 keys set 导入）
	if rc := configFaceKeysGenerate([]string{"--vpn", "gnb", "--node-id", "3001"}); rc != 0 {
		t.Fatalf("keys generate rc=%d", rc)
	}
	// 加一个对端，便于 route/address 非空
	store := &config.Store{Root: root}
	li, err := store.LoadLocalIntent()
	if err != nil {
		t.Fatal(err)
	}
	li.Listen = &domain.LocalListen{UDPPort: 9000, Proxy: true}
	li.Peers = []domain.LocalPeer{{
		ID: "3002", Overlay: "10.182.236.2/32",
		Endpoint: "203.0.113.2:9016",
		PubKey:   "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
	}}
	li.Routes = []domain.LocalRoute{{CIDR: "10.182.236.2/32", Via: "3002"}}
	if err := store.SaveLocalIntent(li); err != nil {
		t.Fatal(err)
	}

	if err := ApplyFromActive(false); err != nil {
		t.Fatalf("ApplyFromActive: %v", err)
	}

	confDir := filepath.Join(root, "conf", "local", "3001") // D4-E 节点唯一存放位
	for _, f := range []string{"node.conf", "route.conf"} {
		if _, err := os.Stat(filepath.Join(confDir, f)); err != nil {
			t.Fatalf("缺 %s: %v", f, err)
		}
	}
	nc, _ := os.ReadFile(filepath.Join(confDir, "node.conf"))
	if !strings.Contains(string(nc), "nodeid") {
		t.Fatalf("node.conf 应含 nodeid:\n%s", nc)
	}
	// listen.proxy **不再**投影到 node.conf：gnb proxy 开关归独立 proxy.conf
	if strings.Contains(string(nc), "proxy on") {
		t.Fatalf("node.conf 不该有 proxy on:\n%s", nc)
	}
	// listen 必须是**裸端口**：gnb 按形态分流（gnb_conf.c:71-81 +
	// gnb_conf_file.c:300-333），纯数字才 v4+v6 同端口，写成 `0.0.0.0:9000`
	// 会只设 IPv4、把 IPv6 留在出厂 9001 —— 现网网关 182 就是这么单侧的。
	if !strings.Contains(string(nc), "listen 9000") {
		t.Fatalf("node.conf 应含 `listen 9000`（裸端口）:\n%s", nc)
	}
	if strings.Contains(string(nc), "0.0.0.0:9000") {
		t.Fatalf("node.conf 的 listen 不该带 v4 前缀（等于放弃 IPv6）:\n%s", nc)
	}
	rc, _ := os.ReadFile(filepath.Join(confDir, "route.conf"))
	if !strings.Contains(string(rc), "3001") || !strings.Contains(string(rc), "3002") {
		t.Fatalf("route.conf 应含本节点与对端:\n%s", rc)
	}
	// last-good
	lg := filepath.Join(root, "state", "last-good")
	ents, err := os.ReadDir(lg)
	if err != nil || len(ents) == 0 {
		t.Fatalf("last-good 应有快照: %v %v", ents, err)
	}
}

func TestApplyFromActiveDryWritesNothing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	t.Setenv("MYNET_RUN_ROOT", t.TempDir())
	if rc := cmdSetupV2([]string{"--local", "--node-id", "1"}); rc != 0 {
		t.Fatal(rc)
	}
	if rc := configFaceKeysGenerate([]string{"--vpn", "gnb", "--node-id", "1"}); rc != 0 {
		t.Fatal(rc)
	}
	if err := ApplyFromActive(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "driver", "gnb", "conf", "1", "node.conf")); err == nil {
		t.Fatal("dry-run 不应落盘")
	}
}

// last-good 快照自清（附带小修）：只留最近 keep 份，更旧的连同密钥副本一起删。
// 无界增长实测 12 天 55 份 —— 每份都是一整套私钥。
func TestPruneLastGoodKeepsRecentOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	lg := filepath.Join(root, "state", "last-good")
	for _, ts := range []string{"20260101T000000Z", "20260102T000000Z", "20260103T000000Z", "20260104T000000Z", "20260105T000000Z"} {
		if err := os.MkdirAll(filepath.Join(lg, ts), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(lg, ts, "node.conf"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pruneLastGood(3)
	ents, err := os.ReadDir(lg)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	want := []string{"20260103T000000Z", "20260104T000000Z", "20260105T000000Z"}
	if len(names) != 3 || names[0] != want[0] || names[1] != want[1] || names[2] != want[2] {
		t.Fatalf("只应留最近 3 份: %v", names)
	}
}

func TestProjectToRunSkipsDirtyFiles(t *testing.T) {
	t.Setenv("MYNET_RUN_ROOT", t.TempDir())
	conf := t.TempDir()
	if err := os.WriteFile(filepath.Join(conf, "node.conf"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf, "gnb.map"), []byte("m"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf, "gnb.pid"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := projectToRun(conf, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(run, "node.conf")); err != nil {
		t.Fatalf("投影缺 node.conf: %v", err)
	}
	if _, err := os.Stat(filepath.Join(run, "gnb.map")); err == nil {
		t.Fatal("gnb.map 不应进投影")
	}
	if _, err := os.Stat(filepath.Join(run, "gnb.pid")); err == nil {
		t.Fatal("gnb.pid 不应进投影")
	}
}
