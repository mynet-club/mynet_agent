package domain

// 已废弃（R0/R2）：扁平 VPNBundle 草案把 GNB route 与 WG AllowedIPs 混为一谈。
// 新契约见同包 bundle_envelope.go（BundleEnvelope + NetworkIntent + GnbIntent + WireGuardIntent），
// SSOT: mynet_ctl/docs/knowledge-base/spec/schemas/bundle-envelope-v2.schema.json。
// 本类型仍被旧 WriteConfig 生产链使用，待 R4–R5 用 Apply 取代后再删除。
//
// VPNBundle 是「驱动无关」的节点配置抽象。
//
// 它由 mynet_api 生成，交给具体 driver 翻译成原生配置：
//   - gnb       → node.conf / address.conf / route.conf / ed25519/ / security/
//   - wireguard → wg 接口与 peer 配置
//
// 关键约束：后端只产出本结构，**不产出任何厂商原生语法**。
// 否则「业务上移」只是把厂商耦合从客户端搬到了后端。
//
// 设计依据：mynet_ctl/docs/knowledge-base/spec/driver-abstraction.md §3.1
type VPNBundle struct {
	Driver VPNDriverType `json:"driver"`

	Node   BundleNode    `json:"node"`
	Peers  []BundlePeer  `json:"peers,omitempty"`
	Routes []BundleRoute `json:"routes,omitempty"`

	Keys  BundleKeys  `json:"keys"`
	Flags BundleFlags `json:"flags,omitempty"`

	// Discovery 是可选字段：gnb 有索引发现，wireguard 无等价概念。
	// 使用者必须先查 DriverCaps.Discovery，不得假设其存在。
	Discovery *BundleDiscovery `json:"discovery,omitempty"`
}

// BundleNode 节点自身参数。
type BundleNode struct {
	ID         string `json:"id"`
	Interface  string `json:"interface,omitempty"`
	ListenPort int    `json:"listen_port,omitempty"`
	MTU        int    `json:"mtu,omitempty"`
}

// BundlePeer 对端节点。
type BundlePeer struct {
	ID         string   `json:"id"`
	PublicKey  string   `json:"public_key,omitempty"`
	Endpoint   string   `json:"endpoint,omitempty"`
	AllowedIPs []string `json:"allowed_ips,omitempty"`
}

// BundleRoute 一条网段路由（gnb 的 route.conf 行）。
type BundleRoute struct {
	NodeID  string `json:"node_id"`
	Network string `json:"network"`
	Netmask string `json:"netmask"`
}

// BundleKeys 本端与对端密钥。
type BundleKeys struct {
	Self  BundleKeyPair     `json:"self"`
	Peers map[string]string `json:"peers,omitempty"`
}

// BundleKeyPair 一对密钥。公钥必填；私钥仅在服务端持有或客户端上传时存在。
type BundleKeyPair struct {
	Private string `json:"private,omitempty"`
	Public  string `json:"public,omitempty"`
}

// BundleFlags 驱动无关的开关。取值统一为 "on"/"off"。
type BundleFlags struct {
	Proxy string `json:"proxy,omitempty"`
}

// BundleDiscovery 索引/发现服务地址（可选能力，见 DriverCaps.Discovery）。
type BundleDiscovery struct {
	IndexServers []string `json:"index_servers,omitempty"`
}

// DriverStatus 驱动运行时状态。字段刻意与 gnb 的 gnb.pid/gnb.map 解耦，
// 由各 driver 自行填充。
type DriverStatus struct {
	Running   bool   `json:"running"`
	PID       int    `json:"pid,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	Interface string `json:"interface,omitempty"`
	Version   string `json:"version,omitempty"`
}
