package domain

// config_schema 单测：本地/联网/插件/激活态的编解码与校验。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestModeFileRoundTrip(t *testing.T) {
	m, err := NewModeFile(ModeOnline, ActiveTarget{
		TenantID: "t_1",
		NodeID:   "8107",
		Driver:   DriverTypeGNB,
		Source:   "conf/online/t_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodePretty(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeModeFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModeOnline || got.Active.TenantID != "t_1" || got.Active.NodeID != "8107" {
		t.Fatalf("roundtrip: %+v", got)
	}
}

func TestModeFileRejectsBadSchemaAndMode(t *testing.T) {
	if _, err := DecodeModeFile([]byte(`{"schema":1,"mode":"online","active":{}}`)); err == nil {
		t.Fatal("schema=1 应拒")
	}
	if _, err := DecodeModeFile([]byte(`{"schema":2,"mode":"cloud","active":{}}`)); err == nil {
		t.Fatal("非法 mode 应拒")
	}
	// online 缺 tenant
	if _, err := DecodeModeFile([]byte(`{"schema":2,"mode":"online","active":{"node_id":"1"}}`)); err == nil {
		t.Fatal("online 缺 tenant 应拒")
	}
}

func TestLocalIntentValidate(t *testing.T) {
	ok := NewLocalIntent(DriverTypeGNB, "n1", "10.0.0.1/32")
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}

	bad := ok
	bad.Node.ID = ""
	if err := bad.Validate(); err == nil {
		t.Fatal("缺 node.id 应拒")
	}

	bad2 := ok
	bad2.Driver = "openvpn"
	if err := bad2.Validate(); err == nil {
		t.Fatal("未知 driver 应拒")
	}

	raw, _ := EncodePretty(ok)
	got, err := DecodeLocalIntent(raw)
	if err != nil || got.Node.ID != "n1" {
		t.Fatalf("decode: %v %+v", err, got)
	}
}

func TestOnlineProfileValidate(t *testing.T) {
	p := OnlineProfile{
		Schema: ConfigSchemaVersion, Mode: ModeOnline,
		TenantID: "t_9", NodeID: "n2", Driver: DriverTypeWireGuard,
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.TenantID = ""
	if err := p.Validate(); err == nil {
		t.Fatal("缺 tenant 应拒")
	}
}

func TestPluginProxyRoundTrip(t *testing.T) {
	pc := NewPluginProxy()
	if err := pc.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := EncodePretty(pc)
	got, err := DecodePluginConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Plugin != PluginNameProxy {
		t.Fatalf("%+v", got)
	}
	// 默认关：用户显式 enable
	if got.Enabled {
		t.Fatal("NewPluginProxy 默认应 Enabled=false")
	}
	cfg, err := DecodePluginProxy(got)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen.Mixed == "" || cfg.Outbound.Mode == "" {
		t.Fatalf("proxy config: %+v", cfg)
	}
}

func TestTenantDirNameRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"", "a/b", "..", "a.b", `a\b`} {
		if _, err := TenantDirName(bad); err == nil {
			t.Errorf("应拒 %q", bad)
		}
	}
	if v, err := TenantDirName("t_10086"); err != nil || v != "t_10086" {
		t.Fatalf("t_10086 → %q %v", v, err)
	}
}

func TestEncodePrettyIsStableShape(t *testing.T) {
	b, err := EncodePretty(map[string]int{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "\n") || !json.Valid(b) {
		t.Fatalf("pretty json 形状不对: %q", b)
	}
}
