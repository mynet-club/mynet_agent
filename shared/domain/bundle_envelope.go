package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
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
//
// Proxy 是云端下发的**出口许可**（T11）：本节点被授权作出口吗、对谁开放。
// 客户端分流策略（role/region/split/exits/dns/…）与本机环境相关，不走 wire，
// 归节点 Local 轨（见 proxy_intent.go / proxy_unify.go）。
type NetworkIntent struct {
	Addresses []string         `json:"addresses,omitempty"`
	Routes    []OsRoute        `json:"routes,omitempty"`
	Rules     []any            `json:"rules,omitempty"`
	Firewall  []any            `json:"firewall,omitempty"`
	NAT       []any            `json:"nat,omitempty"`
	Proxy     *ProxyPermission `json:"proxy,omitempty"`
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
	ForwardNodes []string          `json:"forward_nodes,omitempty"` // 旧 mesh f 列表；proxy 插件不用（出口=下一跳）
}

// GnbEndpointHint address.conf 角色 + 初始 endpoint。运行时 learned endpoint 只进 Status。
type GnbEndpointHint struct {
	NodeID   string `json:"node_id"`
	Endpoint string `json:"endpoint"`
	Role     string `json:"role,omitempty"`
}

// GnbForwarding P2P / Standard / Unified / 显式 relay。**不含 proxy**：
// gnb 的代理开关归独立 proxy.conf，不进 node.conf（proxy 与 gnb.forwarding 无关）。
type GnbForwarding struct {
	Direct      bool            `json:"direct"`
	Standard    bool            `json:"standard"`
	Unified     string          `json:"unified,omitempty"`
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

// TunDeviceMaxLen Linux IFNAMSIZ=16（含结尾 \0）；gnb 的 conf 解析器同样是 16 字节
// 缓冲并**静默截断**到 15（gnb_conf_file.c:275-277）。
const TunDeviceMaxLen = 15

// tunDevicePattern 是**节点侧**对接口名的形态要求，刻意与 back 的字段声明
// （`vpn.gnb…ifname.validation`）分开的两份判据：
//
//   - back 那份是**写入门禁**，决定「管理员能不能存下这个值」，可以更严；
//   - 这份是**安全边界**，只挡会出事的两类：超长（内核与 gnb 静默截断）
//     与含拼接字符（agent 把名字拼进 `ip route … dev X` / `iptables -o X`，
//     空格=断词、分号=注入、斜杠=非法接口名）。
//
// 不在这里复制 back 的字符集，是为了让 back 收紧或放宽声明时**不必同步发 agent**：
// 两边钉死同一字符串的话，改一处就把自己刚下发的合法配置判成非法，节点直接停更。
var tunDevicePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,15}$`)

// ValidateTunDevice 校验一个接口名能不能安全地落到系统里。
func ValidateTunDevice(name string) error {
	if name == "" {
		return fmt.Errorf("接口名为空")
	}
	// 先给长度单独报错：截断是这条链上最阴的失败模式（不报错、名字变样），
	// 报错里带上实际长度才看得出是「谁拼了 node_id」。
	if len(name) > TunDeviceMaxLen {
		return fmt.Errorf("接口名 %q 长 %d 字符，超过上限 %d（内核与 gnb 都会静默截断，之后按名字找接口全对不上）",
			name, len(name), TunDeviceMaxLen)
	}
	if !tunDevicePattern.MatchString(name) {
		return fmt.Errorf("接口名 %q 不合法（须为 [A-Za-z0-9._-]，最长 %d 字符）：会被拼进 ip route / 防火墙命令与驱动配置",
			name, TunDeviceMaxLen)
	}

	return nil
}

// TunDevice 返回本节点的 TUN 接口名。
//
// **唯一权威是 `node.interface`**（back 侧同源：node_configs.config.ifname →
// BundleEnvelopePublisher::resolveTunIfname）。`gnb.options.ifname` 是过渡期别名：
// back 两处发的是同一个值，agent 老版本只读后者，所以这里仍接受它 —— 但只接受
// 「与权威一致」或「权威缺失」两种情况。
//
// 为什么不回退：接口名决定「流量进哪条隧道」，是阻断性配置。一台主机可能多个
// zone 共存（现网网关 182：`gnb_tun` 被另一个 zone 占用，本节点用 `gnb_tun_16`），
// 猜出来的名字**能建接口、能让 ip route 执行成功**，只是把流量搬到别的隧道上且不报错。
// 宁可 apply 失败、错误可见，也不下发一份会搬走流量的配置。
func (e *BundleEnvelope) TunDevice() (string, error) {
	if e == nil {
		return "", fmt.Errorf("envelope 为 nil")
	}

	authority := strings.TrimSpace(e.Node.Interface)
	legacy := ""
	if e.Gnb != nil && e.Gnb.Options != nil {
		legacy = strings.TrimSpace(e.Gnb.Options.Ifname)
	}

	name := authority
	switch {
	case authority == "" && legacy == "":
		return "", fmt.Errorf("bundle 没有接口名（node.interface 与 gnb.options.ifname 皆空）：" +
			"不接受让 gnb 用它自己的默认名静默建口")
	case authority == "":
		name = legacy // 旧 back 的 bundle：过渡期认 gnb.options.ifname
	case legacy != "" && legacy != authority:
		return "", fmt.Errorf("bundle 接口名冲突：node.interface=%q 与 gnb.options.ifname=%q 不一致，"+
			"两者必须由 back 同源产生", authority, legacy)
	}

	if err := ValidateTunDevice(name); err != nil {
		return "", err
	}

	return name, nil
}

// DeviceForRoute 校验一条 OS 路由的 dev 是否指向本节点的隧道口。
//
// back 的 `network.routes[].dev` 与接口名同源，所以不一致只可能是**陈旧 bundle**
// （上一版配置）、手工编辑、或 back 侧漂移。这正是 182 事故的形态：
// `ip route … dev gnb_tun` 命中了另一个 zone 的接口，路由"装成功了"，流量进了别人的隧道。
// dev 为空是给网关/别的表用的，不在此列。
func (e *BundleEnvelope) DeviceForRoute(dev string) error {
	if strings.TrimSpace(dev) == "" {
		return nil
	}
	want, err := e.TunDevice()
	if err != nil {
		return err
	}
	if dev != want {
		return fmt.Errorf("OS 路由的 dev=%q 不是本节点隧道口 %q：拒绝按这份路由装 FIB", dev, want)
	}

	return nil
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
