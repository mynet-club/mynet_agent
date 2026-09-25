package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// BundleEnvelope 是 R2 schema v2 的 Go 形态（driver-abstraction.md §5 / unified-platform-architecture.md §3）。
//
// 它取代扁平的 VPNBundle 草案：公共外壳 + NetworkIntent + 驱动专属 intent。
// GNB route ≠ WG AllowedIPs，不得一一映射。
//
// 线格式以 mynet_ctl/docs/knowledge-base/spec/schemas/bundle-envelope-v2.schema.json
// 为 SSOT；本结构与 PHP 端 App\Modules\VPN\Domain\Bundle 必须能互解同一组 fixtures。
type BundleEnvelope struct {
	SchemaVersion   int              `json:"schema_version"`
	Generation      string           `json:"generation"`
	Checksum        string           `json:"checksum"`
	GeneratedAt     string           `json:"generated_at"`
	MinAgentVersion string           `json:"min_agent_version,omitempty"`
	Node            NodeRef          `json:"node"`
	Driver          VPNDriverType    `json:"driver"`
	Network         NetworkIntent    `json:"network"`
	Gnb             *GnbIntent       `json:"gnb"`
	WireGuard       *WireGuardIntent `json:"wireguard"`
	Connections     []ConnectionPlan `json:"connections,omitempty"`
	Capabilities    map[string]any   `json:"capabilities,omitempty"`
}

// NodeRef 业务节点与接口意图。ID 是业务标识，不得等同任何协议公钥身份。
type NodeRef struct {
	ID         string `json:"id"`
	Interface  string `json:"interface,omitempty"`
	MTU        int    `json:"mtu,omitempty"`
	ListenPort int    `json:"listen_port,omitempty"`
}

// NetworkIntent 操作系统网络状态意图（地址 / FIB / rule / firewall / NAT）。
// 不包含 GNB route.conf 语义，也不包含 WG AllowedIPs。
type NetworkIntent struct {
	Addresses []string  `json:"addresses,omitempty"`
	Routes    []OsRoute `json:"routes,omitempty"`
	Rules     []any     `json:"rules,omitempty"`
	Firewall  []any     `json:"firewall,omitempty"`
	NAT       []any     `json:"nat,omitempty"`
}

// OsRoute 一条 OS FIB 路由。
type OsRoute struct {
	Prefix string `json:"prefix"`
	Via    string `json:"via,omitempty"`
	Dev    string `json:"dev,omitempty"`
	Table  any    `json:"table,omitempty"`
	Metric int    `json:"metric,omitempty"`
	Type   string `json:"type,omitempty"`
	Source string `json:"source,omitempty"`
}

// GnbIntent GNB 专属意图。
type GnbIntent struct {
	Identity   GnbIdentity    `json:"identity"`
	Listeners  []string       `json:"listeners,omitempty"`
	Nodes      []GnbNode      `json:"nodes,omitempty"`
	Routes     []GnbRoute     `json:"routes,omitempty"`
	Discovery  *GnbDiscovery  `json:"discovery,omitempty"`
	Forwarding *GnbForwarding `json:"forwarding,omitempty"`
	Options    *GnbOptions    `json:"options,omitempty"`
}

// GnbIdentity GNB Ed25519 身份。与 WireGuard Curve25519 不可互转。
type GnbIdentity struct {
	NodeID         string            `json:"node_id"`
	KeyAlgorithm   string            `json:"key_algorithm"` // 必须为 "ed25519"
	PrivateKey     string            `json:"private_key"`
	PublicKey      string            `json:"public_key"`
	PeerPublicKeys map[string]string `json:"peer_public_keys,omitempty"`
}

// UnmarshalJSON 容忍 `peer_public_keys: []`。
//
// 为什么要在解码侧也兜一道：PHP 的**空关联数组** json_encode 出来是 `[]` 而不是 `{}`，
// 于是「还没有 peer 的新节点」下发的 bundle 会让 Go 的 map[string]string 解码直接失败
// （实测：`cannot unmarshal array into Go struct field …peer_public_keys of type
// map[string]string`）。back 侧已改为空时输出 `{}`（GnbIdentity::toArray），
// 但**两端各自兜一道**才是稳的：任何一端的旧版本、或将来别的 PHP 生产者，
// 都不该让节点拿不到配置。
//
// 只放宽「空数组 == 空 map」这一种情况；非空的数组仍然是错误 ——
// 那说明对端把 map 当成了 list，属于真的契约不符，必须报出来而不是静默吞掉。
func (g *GnbIdentity) UnmarshalJSON(data []byte) error {
	type alias GnbIdentity
	aux := struct {
		*alias
		PeerPublicKeys json.RawMessage `json:"peer_public_keys"`
	}{alias: (*alias)(g)}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	raw := bytes.TrimSpace(aux.PeerPublicKeys)
	if len(raw) == 0 || string(raw) == "null" {
		g.PeerPublicKeys = nil

		return nil
	}
	if string(raw) == "[]" {
		g.PeerPublicKeys = map[string]string{}

		return nil
	}

	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("peer_public_keys 应是对象（node_id → 公钥），实际 %s: %w",
			truncateForError(raw), err)
	}
	g.PeerPublicKeys = m

	return nil
}

func truncateForError(b []byte) string {
	const max = 60
	if len(b) <= max {
		return string(b)
	}

	return string(b[:max]) + "…"
}

// GnbNode overlay 节点（route.conf 创建/绑定）。
type GnbNode struct {
	NodeID     string `json:"node_id"`
	PublicKey  string `json:"public_key,omitempty"`
	TunAddress string `json:"tun_address,omitempty"`
}

// GnbRoute route.conf 一行：节点 + 前缀 + 可选 relay/proxy。
// 刻意用 CIDR 而不是 {network,netmask}——后者对 IPv6 不自然。
type GnbRoute struct {
	NodeID        string `json:"node_id"`
	Prefix        string `json:"prefix"`
	Source        string `json:"source,omitempty"`
	ViaNodeID     string `json:"via_node_id,omitempty"`
	ProxyFallback bool   `json:"proxy_fallback,omitempty"`
}

// GnbDiscovery index / 静态 endpoint / forward 候选。
type GnbDiscovery struct {
	IndexServers []string          `json:"index_servers,omitempty"`
	StaticNodes  []GnbEndpointHint `json:"static_nodes,omitempty"`
	ForwardNodes []string          `json:"forward_nodes,omitempty"`
}

// GnbEndpointHint address.conf 角色 + 初始 endpoint。运行时 learned endpoint 只进 Status。
type GnbEndpointHint struct {
	NodeID   string `json:"node_id"`
	Endpoint string `json:"endpoint"`
	Role     string `json:"role,omitempty"`
}

// GnbForwarding P2P / Standard / Unified / proxy / 显式 relay。
type GnbForwarding struct {
	Direct      bool            `json:"direct"`
	Standard    bool            `json:"standard"`
	Unified     string          `json:"unified,omitempty"`
	Proxy       bool            `json:"proxy"`
	RelayRoutes []GnbRelayRoute `json:"relay_routes,omitempty"`
}

// GnbRelayRoute 显式中继路径。中继保持 GNB payload 端到端保护，不等同 WG hub。
type GnbRelayRoute struct {
	ViaNodeID string `json:"via_node_id"`
	Prefix    string `json:"prefix"`
}

// GnbOptions node.conf 其余选项。
type GnbOptions struct {
	MultiSocket bool   `json:"multi_socket,omitempty"`
	SafeIndex   bool   `json:"safe_index,omitempty"`
	MTU         int    `json:"mtu,omitempty"`
	Ifname      string `json:"ifname,omitempty"`
	Passcode    string `json:"passcode,omitempty"`
}

// WireGuardIntent WireGuard 专属意图。
type WireGuardIntent struct {
	Interface WgInterface `json:"interface"`
	Peers     []WgPeer    `json:"peers"`
	Routing   *WgRouting  `json:"routing,omitempty"`
}

// WgInterface 本端 WG 接口。Address/MTU/FIB 归 NetworkIntent，不在 syncconf 范围。
type WgInterface struct {
	PrivateKey   string   `json:"private_key"`
	PublicKey    string   `json:"public_key"`
	KeyAlgorithm string   `json:"key_algorithm"` // 必须为 "curve25519"
	ListenPort   int      `json:"listen_port,omitempty"`
	Fwmark       *int     `json:"fwmark"`
	DNS          []string `json:"dns,omitempty"`
}

// WgPeer 对端。AllowedIPs 是双向 cryptokey routing，不是普通系统路由。
type WgPeer struct {
	ID                      string   `json:"id,omitempty"`
	BusinessNodeID          string   `json:"business_node_id,omitempty"`
	PublicKey               string   `json:"public_key"`
	PresharedKey            *string  `json:"preshared_key"`
	BootstrapEndpoint       *string  `json:"bootstrap_endpoint"`
	AllowedIPs              []string `json:"allowed_ips"`
	PersistentKeepaliveSecs int      `json:"persistent_keepalive_seconds,omitempty"`
}

// WgRouting 路由接管模式。
type WgRouting struct {
	Mode  string `json:"mode,omitempty"`
	Table any    `json:"table,omitempty"`
}

// ConnectionPlan L2 传输计划（可选元数据，driver 不直接消费）。
type ConnectionPlan struct {
	ConnectionID    any            `json:"connection_id"`
	TargetNodeID    string         `json:"target_node_id"`
	TransportMode   string         `json:"transport_mode,omitempty"`
	TransportPolicy map[string]any `json:"transport_policy,omitempty"`
}

// DecodeBundleEnvelope 解码线上 envelope。
// 未知字段被忽略（向前兼容）；schema_version 大于 2 时仍可解出已知字段。
func DecodeBundleEnvelope(raw []byte) (*BundleEnvelope, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("envelope 为空")
	}
	var env BundleEnvelope
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // 保持数字原样，避免 float64 往返污染 checksum
	if err := dec.Decode(&env); err != nil {
		return nil, fmt.Errorf("解码 BundleEnvelope: %w", err)
	}
	return &env, nil
}

// EncodeBundleEnvelope 编码（不计算 checksum，调用方自行填）。
func EncodeBundleEnvelope(env *BundleEnvelope) ([]byte, error) {
	if env == nil {
		return nil, fmt.Errorf("envelope 为 nil")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ComputeBundleChecksum 计算 schema 约定的 checksum：
//
//	checksum = "sha256:" + hex(sha256(canonical_json(envelope 去掉 checksum 键)))
//
// canonical_json：对象键按 UTF-8 字节序升序、数组保持顺序、无多余空白、
// 字符串做 JSON 转义但**不做 HTML 转义**、整数无小数点。
// 与 PHP 端 BundleEnvelope::computeChecksum 及 fixtures 生成脚本一致。
func ComputeBundleChecksum(raw []byte) (string, error) {
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return "", fmt.Errorf("checksum 输入不是 JSON 对象: %w", err)
	}
	delete(generic, "checksum")

	canonical, err := canonicalJSON(generic)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// VerifyBundleChecksum 校验 envelope 自带 checksum。
func VerifyBundleChecksum(raw []byte) error {
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return fmt.Errorf("checksum 校验输入无效: %w", err)
	}
	want, _ := generic["checksum"].(string)
	got, err := ComputeBundleChecksum(raw)
	if err != nil {
		return err
	}
	if want == "" {
		return fmt.Errorf("checksum 缺失")
	}
	if want != got {
		return fmt.Errorf("checksum 不匹配: 期望 %s, 实际 %s", want, got)
	}
	return nil
}

// canonicalJSON 生成与 PHP/Python 一致的 canonical 形态。
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// map[string]any 的键在 encoding/json 中按 UTF-8 字节序排序
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
