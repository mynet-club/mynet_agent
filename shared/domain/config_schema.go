package domain

// 配置树 schema v2 —— 本地配置 / 联网配置 / 插件 / 激活态。
//
// 三分（与备份、Apply 一致）：
//   conf/**     真相源（意图 + 身份 + 插件配置），进备份
//   plugins/**  插件包体，可重装
//   var/**      运行态，不进备份
//
// 驱动差异不下泄：业务对象不含 node.conf / AllowedIPs；
// driver/** 只是 Apply 投影。GNB 的 `proxy on` 在 Intent.Listen.Proxy，
// 与插件 proxy 配置（PluginProxyConfig）是两回事。
//
// 开发期无兼容层：旧 mynet.conf / 扁平 conf 不在此 schema 内。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ConfigSchemaVersion 当前 schema。导入时版本不符直接拒。
const ConfigSchemaVersion = 2

// ConfigMode 配置来源。
type ConfigMode string

const (
	// ModeLocal 不登录 mynet，全部本地意图。
	ModeLocal ConfigMode = "local"
	// ModeOnline 经 mynet / 租户下发（BundleEnvelope）。
	ModeOnline ConfigMode = "online"
)

func (m ConfigMode) Valid() bool {
	return m == ModeLocal || m == ModeOnline
}

// ModeFile $MYNET_HOME/mode.json —— 激活谁。
type ModeFile struct {
	Schema int          `json:"schema"`
	Mode   ConfigMode   `json:"mode"`
	Active ActiveTarget `json:"active"`
	// UpdatedAt RFC3339；调试用，不参与语义。
	UpdatedAt string `json:"updated_at,omitempty"`
}

// ActiveTarget 当前激活的配置源。
// Local 时 TenantID/NodeID 可空；Online 时必须齐。
type ActiveTarget struct {
	TenantID string        `json:"tenant_id,omitempty"`
	NodeID   string        `json:"node_id,omitempty"`
	Driver   VPNDriverType `json:"driver,omitempty"`
	// Source 相对 HOME 的目录（如 conf/local 或 conf/online/t_1），便于排障。
	Source string `json:"source,omitempty"`
}

// Validate 激活目标是否自洽。
func (a ActiveTarget) Validate(mode ConfigMode) error {
	switch mode {
	case ModeLocal:
		return nil
	case ModeOnline:
		if a.TenantID == "" {
			return fmt.Errorf("online 模式缺 tenant_id")
		}
		if a.NodeID == "" {
			return fmt.Errorf("online 模式缺 node_id")
		}
		return nil
	default:
		return fmt.Errorf("未知 mode: %s", mode)
	}
}

// NewModeFile 构造并打时间戳。
func NewModeFile(mode ConfigMode, active ActiveTarget) (ModeFile, error) {
	if !mode.Valid() {
		return ModeFile{}, fmt.Errorf("非法 mode: %q", mode)
	}
	if err := active.Validate(mode); err != nil {
		return ModeFile{}, err
	}
	return ModeFile{
		Schema:    ConfigSchemaVersion,
		Mode:      mode,
		Active:    active,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// ── 本地意图 ─────────────────────────────────────────────────

// LocalIntent conf/local/intent.json —— 本地模式的抽象意图。
// 与 BundleEnvelope 的意图同构，但没有生成元数据（generation/checksum）。
type LocalIntent struct {
	Schema   int            `json:"schema"`
	Mode     ConfigMode     `json:"mode"` // 恒为 local
	Driver   VPNDriverType  `json:"driver"`
	Node     LocalNode      `json:"node"`
	Listen   *LocalListen   `json:"listen,omitempty"`
	Peers    []LocalPeer    `json:"peers,omitempty"`
	Routes   []LocalRoute   `json:"routes,omitempty"`
	Discover *LocalDiscover `json:"discovery,omitempty"`
	MTU      int            `json:"mtu,omitempty"`
}

// LocalNode 本节点身份面（不含私钥；密钥在 keys/）。
type LocalNode struct {
	ID      string `json:"id"`
	Overlay string `json:"overlay,omitempty"` // 如 10.182.236.183/32
	Name    string `json:"name,omitempty"`
}

// LocalListen 监听与节点级开关。
// Proxy 是 GNB `proxy on`（未登记目的送出口对端=下一跳），**不是** proxy 插件。
type LocalListen struct {
	UDPPort int  `json:"udp_port,omitempty"`
	Forward bool `json:"forward,omitempty"`
	Proxy   bool `json:"proxy,omitempty"`
}

// LocalPeer 手工对端。
type LocalPeer struct {
	ID       string `json:"id"`
	Overlay  string `json:"overlay,omitempty"`
	Endpoint string `json:"endpoint,omitempty"` // host:port
	PubKey   string `json:"pubkey,omitempty"`
	Relay    bool   `json:"relay,omitempty"`
}

// LocalRoute 一条意图路由。
type LocalRoute struct {
	CIDR string `json:"cidr"`
	Via  string `json:"via,omitempty"`
}

// LocalDiscover 发现服务。
type LocalDiscover struct {
	IndexServers []string `json:"index_servers,omitempty"`
}

// Validate 本地意图自检。
func (l *LocalIntent) Validate() error {
	if l.Schema != 0 && l.Schema != ConfigSchemaVersion {
		return fmt.Errorf("intent schema=%d，期望 %d", l.Schema, ConfigSchemaVersion)
	}
	if l.Mode != "" && l.Mode != ModeLocal {
		return fmt.Errorf("local intent 的 mode 应为 local，实际 %q", l.Mode)
	}
	if l.Driver != DriverTypeGNB && l.Driver != DriverTypeWireGuard {
		return fmt.Errorf("driver 必须是 gnb|wireguard，实际 %q", l.Driver)
	}
	if l.Node.ID == "" {
		return fmt.Errorf("local intent 缺 node.id")
	}
	return nil
}

// NewLocalIntent 默认骨架。
func NewLocalIntent(driver VPNDriverType, nodeID, overlay string) LocalIntent {
	return LocalIntent{
		Schema: ConfigSchemaVersion,
		Mode:   ModeLocal,
		Driver: driver,
		Node:   LocalNode{ID: nodeID, Overlay: overlay},
	}
}

// ── 联网 profile ─────────────────────────────────────────────

// OnlineProfile conf/online/{tenant}/profile.json —— 租户身份面（无密钥）。
type OnlineProfile struct {
	Schema     int        `json:"schema"`
	Mode       ConfigMode `json:"mode"` // 恒为 online
	TenantID   string     `json:"tenant_id"`
	TenantName string     `json:"tenant_name,omitempty"`
	APIBase    string     `json:"api_base,omitempty"`
	// Gateway 覆盖 agent.json 的默认网关；空则用全局。
	Gateway  string        `json:"gateway,omitempty"`
	Driver   VPNDriverType `json:"driver"`
	NodeID   string        `json:"node_id"`
	NodeName string        `json:"node_name,omitempty"`
	// CreatedAt 首次 claim。
	CreatedAt string `json:"created_at,omitempty"`
}

func (p *OnlineProfile) Validate() error {
	if p.Mode != "" && p.Mode != ModeOnline {
		return fmt.Errorf("online profile 的 mode 应为 online，实际 %q", p.Mode)
	}
	if p.TenantID == "" {
		return fmt.Errorf("online profile 缺 tenant_id")
	}
	if p.NodeID == "" {
		return fmt.Errorf("online profile 缺 node_id")
	}
	if p.Driver != DriverTypeGNB && p.Driver != DriverTypeWireGuard {
		return fmt.Errorf("driver 必须是 gnb|wireguard，实际 %q", p.Driver)
	}
	return nil
}

// OnlineBundleFile conf/online/{tenant}/bundle.json —— 上游 envelope 落盘。
// Intent 用 BundleEnvelope 原结构；这里包一层修订号便于增量。
type OnlineBundleFile struct {
	Schema   int             `json:"schema"`
	Revision string          `json:"revision,omitempty"`
	ETag     string          `json:"etag,omitempty"`
	PulledAt string          `json:"pulled_at,omitempty"`
	Envelope *BundleEnvelope `json:"envelope"`
}

func (b *OnlineBundleFile) Validate() error {
	if b.Envelope == nil {
		return fmt.Errorf("bundle 缺 envelope")
	}
	if b.Envelope.Node.ID == "" {
		return fmt.Errorf("bundle.envelope.node.id 为空")
	}
	return nil
}

// ── 插件配置 ─────────────────────────────────────────────────

// PluginNameProxy 代理/转发插件（原 Node 侧 proxy）。
// 与 GNB node.conf 的 `proxy on` 无关。
const PluginNameProxy = "proxy"

// PluginConfig conf/**/plugins/{name}.json 的公共壳。
type PluginConfig struct {
	Schema    int    `json:"schema"`
	Plugin    string `json:"plugin"`
	Enabled   bool   `json:"enabled"`
	Autostart bool   `json:"autostart,omitempty"`
	// Config 由具体插件解释（proxy → PluginProxyConfig）。
	Config json.RawMessage `json:"config,omitempty"`
}

func (p *PluginConfig) Validate() error {
	if p.Plugin == "" {
		return fmt.Errorf("plugin 配置缺 plugin 名")
	}
	if p.Schema != 0 && p.Schema != ConfigSchemaVersion {
		return fmt.Errorf("plugin schema=%d", p.Schema)
	}
	return nil
}

// PluginProxyConfig proxy 插件意图（只在 PluginConfig.Config 里）。
//
// 这是 proxy 的**唯一配置入口**：用户是否启用、从哪些 peer 出去、权重/顺序。
// apply 时投影到驱动侧（gnb → driver/gnb/conf/<id>/proxy.conf；address.conf 不写 f）。
// 与 node.conf 的 `proxy on` 是同一件事的两层：插件决定「开不开、走谁」，
// 驱动投影决定「怎么写进 gnb」。
type PluginProxyConfig struct {
	// Role 明确节点角色：client、server 或 both。
	Role     string              `json:"role,omitempty"`
	Core     string              `json:"core,omitempty"` // mihomo 等（本地代理核，可选）
	Listen   PluginProxyListen   `json:"listen"`
	Outbound PluginProxyOutbound `json:"outbound"`
	Rules    []string            `json:"rules,omitempty"`
	// Exits 出站 peer 列表：选谁、几个、权重、顺序。
	// gnb 侧：出口对端 = 数据目标 / 下一跳（不写 address.conf / ForwardNodes）。
	Exits []ProxyExit `json:"exits,omitempty"`
	// MaxExits 最多同时启用几个出口（0=不限）。权重加权选点时的上限。
	MaxExits int `json:"max_exits,omitempty"`
}

// ProxyExit 一个出站 peer。
type ProxyExit struct {
	NodeID string `json:"node_id"`
	// Via 出口节点的 overlay VIP（策略路由 default via）。可空 → 由对端 tun_address 解析。
	Via string `json:"via,omitempty"`
	// Weight 1–100，越大越优先；默认 10。
	Weight int `json:"weight,omitempty"`
	// Order 1-based 展示/尝试顺序；0 表示跟在后面。
	Order int `json:"order,omitempty"`
	// Enabled 单独开关，便于保留配置但临时下线某出口。
	Enabled bool `json:"enabled"`
}

// EffectiveExits 过滤 Enabled 并按 Order 排序（Order 相同按 Weight 降序）。
func (c *PluginProxyConfig) EffectiveExits() []ProxyExit {
	var out []ProxyExit
	for _, e := range c.Exits {
		if e.Enabled && e.NodeID != "" {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			if out[i].Order == 0 {
				return false
			}
			if out[j].Order == 0 {
				return true
			}
			return out[i].Order < out[j].Order
		}
		return out[i].Weight > out[j].Weight
	})
	if c.MaxExits > 0 && len(out) > c.MaxExits {
		out = out[:c.MaxExits]
	}
	return out
}

// PluginProxyListen 本机监听面。
type PluginProxyListen struct {
	Mixed string `json:"mixed,omitempty"`
	API   string `json:"api,omitempty"`
	HTTP  string `json:"http,omitempty"`
	SOCKS string `json:"socks,omitempty"`
}

// PluginProxyOutbound 出站策略。
type PluginProxyOutbound struct {
	Mode     string   `json:"mode,omitempty"` // rule | direct | …
	Via      string   `json:"via,omitempty"`  // tun | direct
	Upstream []string `json:"upstream,omitempty"`
}

// NewPluginProxy 默认 proxy 插件配置。
func NewPluginProxy() PluginConfig {
	raw, _ := json.Marshal(PluginProxyConfig{
		Core:   "mihomo",
		Listen: PluginProxyListen{Mixed: "127.0.0.1:7890", API: "127.0.0.1:9090"},
		Outbound: PluginProxyOutbound{
			Mode: "rule",
			Via:  "tun",
		},
		Rules:    []string{"MATCH,DIRECT"},
		MaxExits: 3,
	})
	return PluginConfig{
		Schema:    ConfigSchemaVersion,
		Plugin:    PluginNameProxy,
		Enabled:   false, // 默认关：用户显式 enable
		Autostart: true,
		Config:    raw,
	}
}

// DecodePluginProxy 从 PluginConfig 取 proxy 专段。
func DecodePluginProxy(p *PluginConfig) (*PluginProxyConfig, error) {
	if p.Plugin != PluginNameProxy {
		return nil, fmt.Errorf("不是 proxy 插件配置: %s", p.Plugin)
	}
	if len(p.Config) == 0 {
		return nil, fmt.Errorf("proxy 插件 config 为空")
	}
	var c PluginProxyConfig
	if err := json.Unmarshal(p.Config, &c); err != nil {
		return nil, fmt.Errorf("proxy config: %w", err)
	}
	return &c, nil
}

// ── 编解码（统一剥掉未知字段外的校验）────────────────────────

// DecodeModeFile 解析 mode.json。
func DecodeModeFile(raw []byte) (*ModeFile, error) {
	var m ModeFile
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("mode.json: %w", err)
	}
	if m.Schema != ConfigSchemaVersion {
		return nil, fmt.Errorf("mode.json schema=%d，期望 %d", m.Schema, ConfigSchemaVersion)
	}
	if !m.Mode.Valid() {
		return nil, fmt.Errorf("mode.json 非法 mode=%q", m.Mode)
	}
	if err := m.Active.Validate(m.Mode); err != nil {
		return nil, err
	}
	return &m, nil
}

// DecodeLocalIntent 解析本地意图。
func DecodeLocalIntent(raw []byte) (*LocalIntent, error) {
	var l LocalIntent
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("local intent: %w", err)
	}
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return &l, nil
}

// DecodeOnlineProfile 解析租户 profile。
func DecodeOnlineProfile(raw []byte) (*OnlineProfile, error) {
	var p OnlineProfile
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("online profile: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// DecodePluginConfig 解析插件配置。
func DecodePluginConfig(raw []byte) (*PluginConfig, error) {
	var p PluginConfig
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("plugin config: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// EncodePretty 带缩进的 JSON（落盘用）。
func EncodePretty(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// TenantDirName 租户目录名（防路径穿越）。
func TenantDirName(tenantID string) (string, error) {
	t := strings.TrimSpace(tenantID)
	if t == "" {
		return "", fmt.Errorf("tenant_id 为空")
	}
	if strings.ContainsAny(t, `/\..`) || t == "." || t == ".." {
		return "", fmt.Errorf("tenant_id 含非法字符: %q", tenantID)
	}
	return t, nil
}

// EncodePluginProxyBody PluginProxyConfig → PluginConfig.Config。
func EncodePluginProxyBody(c *PluginProxyConfig) (json.RawMessage, error) {
	return json.Marshal(c)
}
