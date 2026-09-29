package domain

// ProxyACL 出口授权：对谁开放（proxy-split.md §2 云端能力② · T11）。
//
// 语义分两层：
//   1. **业务授权**（云端）：哪些消费端节点/租户可以使用本出口
//   2. **准入名单**（出口侧）：L6 只放行指定 overlay 地址/网段
//
// 云端把解算结果写进 envelope；出口节点只执行，不在现算「谁行谁不行」。

import "strings"

// ACL 模式。
const (
	ProxyACLOpen      = "open"      // 不限制（默认，组织内）
	ProxyACLAllowlist = "allowlist" // 仅列内
	ProxyACLDenylist  = "denylist"  // 列外全放行
)

type ProxyACL struct {
	// Mode open|allowlist|denylist。空 = open。
	Mode string `json:"mode,omitempty"`
	// AllowedNodeIDs 允许使用本出口的消费端节点 ID。
	AllowedNodeIDs []string `json:"allowed_node_ids,omitempty"`
	// DeniedNodeIDs 拒绝列表（denylist / allowlist 内的例外）。
	DeniedNodeIDs []string `json:"denied_node_ids,omitempty"`
	// AllowedCIDRs 出口侧准入：允许来源 overlay 网段（如 10.20.0.0/24）。
	AllowedCIDRs []string `json:"allowed_cidrs,omitempty"`
	// AllowedUserIDs 可选：用户维度（租户内）。
	AllowedUserIDs []string `json:"allowed_user_ids,omitempty"`
}

// IsAllowed 节点是否可使用本出口。
func (a *ProxyACL) IsAllowed(nodeID string) bool {
	if a == nil {
		return true
	}
	n := strings.TrimSpace(nodeID)
	if n == "" {
		return false
	}
	if containsFold(a.DeniedNodeIDs, n) {
		return false
	}
	switch a.Mode {
	case "", ProxyACLOpen:
		return true
	case ProxyACLAllowlist:
		return containsFold(a.AllowedNodeIDs, n)
	case ProxyACLDenylist:
		return true // 已过 Denied 检查
	default:
		return containsFold(a.AllowedNodeIDs, n)
	}
}

// EffectiveMode 空 = open。
func (a *ProxyACL) EffectiveMode() string {
	if a == nil || a.Mode == "" {
		return ProxyACLOpen
	}
	return a.Mode
}

// Validate 形状校验。allowlist 必须非空 AllowedNodeIDs。
func (a *ProxyACL) Validate() error {
	if a == nil {
		return nil
	}
	if a.Mode == ProxyACLAllowlist && len(a.AllowedNodeIDs) == 0 {
		return &ProxySplitError{Region: "acl", Policy: "allowlist 需要 allowed_node_ids"}
	}
	if a.Mode != "" && a.Mode != ProxyACLOpen && a.Mode != ProxyACLAllowlist && a.Mode != ProxyACLDenylist {
		return &ProxySplitError{Region: "acl", Policy: a.Mode}
	}
	return nil
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), want) {
			return true
		}
	}
	return false
}

// FilterAllowed 按 ACL 过滤候选消费端。
func (a *ProxyACL) FilterAllowed(nodeIDs []string) []string {
	var out []string
	for _, id := range nodeIDs {
		if a.IsAllowed(id) {
			out = append(out, id)
		}
	}
	return out
}
