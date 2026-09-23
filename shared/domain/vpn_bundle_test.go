package domain

import (
	"encoding/json"
	"testing"
)

// 编译期证明 VPNLifecycleDriver 的签名自洽、可被实现。
// S1 阶段尚无真实实现（GNBDriver 仍只读），此桩仅用于锁住接口形状。
type stubLifecycleDriver struct{}

func (stubLifecycleDriver) Capabilities() DriverCaps       { return DriverCaps{} }
func (stubLifecycleDriver) WriteConfig(*VPNBundle) error   { return nil }
func (stubLifecycleDriver) Start() error                   { return nil }
func (stubLifecycleDriver) Stop() error                    { return nil }
func (stubLifecycleDriver) Reload() error                  { return nil }
func (stubLifecycleDriver) Status() (*DriverStatus, error) { return nil, nil }

var _ VPNLifecycleDriver = stubLifecycleDriver{}

// 锁定抽象 bundle 的线上字段名。back 与各 driver 依赖这些 key 通信，
// 改名即为破坏性变更（见 spec/driver-abstraction.md §3.1）。
func TestVPNBundleJSONShape(t *testing.T) {
	b := VPNBundle{
		Driver: DriverTypeGNB,
		Node:   BundleNode{ID: "1001", Interface: "gnb_tun_178", ListenPort: 9016, MTU: 1450},
		Peers:  []BundlePeer{{ID: "1002", PublicKey: "abc", AllowedIPs: []string{"10.0.0.0/24"}}},
		Routes: []BundleRoute{{NodeID: "1001", Network: "10.182.236.0", Netmask: "255.255.255.0"}},
		Keys:   BundleKeys{Self: BundleKeyPair{Public: "pub"}, Peers: map[string]string{"1002": "peerpub"}},
		Flags:  BundleFlags{Proxy: "off"},
		Discovery: &BundleDiscovery{
			IndexServers: []string{"idx:9016"},
		},
	}

	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, key := range []string{"driver", "node", "peers", "routes", "keys", "flags", "discovery"} {
		if _, ok := m[key]; !ok {
			t.Errorf("缺少顶层字段 %q；实际: %s", key, raw)
		}
	}

	var node map[string]json.RawMessage
	if err := json.Unmarshal(m["node"], &node); err != nil {
		t.Fatalf("node 解析失败: %v", err)
	}
	for _, key := range []string{"id", "interface", "listen_port", "mtu"} {
		if _, ok := node[key]; !ok {
			t.Errorf("node 缺少字段 %q", key)
		}
	}

	var back VPNBundle
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if back.Node.ListenPort != 9016 {
		t.Errorf("listen_port 丢失: %+v", back.Node)
	}
	if len(back.Peers) != 1 || back.Peers[0].ID != "1002" {
		t.Errorf("peers 丢失: %+v", back.Peers)
	}
	if back.Keys.Peers["1002"] != "peerpub" {
		t.Errorf("keys.peers 丢失: %+v", back.Keys)
	}
}

// discovery 是可选能力：gnb 有索引发现，wireguard 无等价物。
// 缺省时不得序列化出占位，否则 wireguard 侧会拿到一个它无法理解的概念。
func TestVPNBundleOmittedOptionals(t *testing.T) {
	raw, err := json.Marshal(VPNBundle{Driver: DriverTypeWireGuard})
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}

	if v, ok := m["discovery"]; ok {
		t.Errorf("discovery 应被 omitempty 省略，实际为 %s", v)
	}
}
