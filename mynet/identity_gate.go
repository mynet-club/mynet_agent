package mynet

// 身份门禁（口径 B，2026-10-14 用户裁定）：
//
//  1. **私钥是节点身份**；云端只存公钥。未确立本地私钥前，不得拉配置合同/起隧道。
//  2. 导入私钥 → 本地推导公钥 → 与云端比对：
//     · 一致 → 恢复身份
//     · 不一致 → 拒绝：「私钥不对」，提示找正确私钥或走换钥
//     · 云端空 → 首建（登录已授权绑定）
//  3. **替换已有公钥 = 换钥**：登录之外必须**新鲜确认**（step-up），
//     防会话被偷后静默改嫁活节点身份。首建/导入匹配不加 step-up。
//  4. 判据是「云端是否已有该节点公钥」，不是用户点了哪个按钮。

import (
	"fmt"
	"strings"

	"github.com/mynet-club/mynet_agent/mynet/keys"
	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// IdentityGateErr 身份门禁拒绝原因（UI/CLI 统一话术）。
type IdentityGateErr struct {
	Code    string // need_private | need_rekey_confirm | key_mismatch
	Message string
}

func (e *IdentityGateErr) Error() string { return e.Message }

// RequireLocalIdentity 本地必须已有节点私钥（可推导/可签名）。
// 调用方：config pull/sync、apply、service start 等「拿到配置就能跑」的路径。
func RequireLocalIdentity() error {
	keysDir, mode, err := configFaceKeysDir()
	if err != nil {
		return &IdentityGateErr{Code: "need_private", Message: "无法定位密钥目录：" + err.Error()}
	}
	if priv, err := keys.LoadNodePrivateKey(keysDir); err == nil && strings.TrimSpace(priv) != "" {
		return nil
	}
	// 只有公钥不算身份确立（公钥可被任意写入）
	nodeID := ""
	if mode != nil {
		nodeID = mode.Active.NodeID
	}
	return &IdentityGateErr{
		Code: "need_private",
		Message: fmt.Sprintf(
			"未确立节点身份：必须先提供私钥（node_id=%s）。"+
				"私钥是身份源，云端只有公钥；正确私钥请导入，新身份请 generate 后上传公钥。"+
				"在此之前不拉取配置合同、不启动隧道。", nodeID),
	}
}

// CloudPubState 云端该节点公钥状态。
type CloudPubState struct {
	State    string // empty | match | mismatch | unreachable | no_login
	CloudPub string
}

// InspectCloudPub 查云端公钥（登录后）。不写盘。
func InspectCloudPub(nodeID string) CloudPubState {
	if nodeID == "" {
		return CloudPubState{State: "unreachable"}
	}
	cred := LoadCredential()
	if cred == nil || cred.Token == "" {
		return CloudPubState{State: "no_login"}
	}
	c := newBackClientFor(cred)
	var node struct {
		CustomPublicKey string `json:"custom_public_key"`
		PublicKey       string `json:"public_key"`
	}
	if err := c.getEnveloped("/api/v1/nodes/"+nodeID, &node); err != nil {
		return CloudPubState{State: "unreachable"}
	}
	cloudPub := strings.TrimSpace(node.CustomPublicKey)
	if cloudPub == "" {
		cloudPub = strings.TrimSpace(node.PublicKey)
	}
	return CloudPubState{State: "empty", CloudPub: cloudPub} // state filled by Compare
}

// DecideKeyWrite 决定本次密钥写入是否放行（口径 B）。
//
//	derivedPub = 由私钥推导的公钥（身份源派生）
//	confirmRekey = 是否已做新鲜确认（换钥 step-up）
//
// 返回 nil 表示可写；否则 *IdentityGateErr。
func DecideKeyWrite(nodeID, derivedPub string, confirmRekey bool) (CloudPubState, error) {
	// local 模式没有云端身份面，不走云端门禁
	if st := (&config.Store{}); func() bool {
		if _, m, err := st.ActiveSource(); err == nil && m != nil && m.Mode == domain.ModeLocal {
			return true
		}
		return false
	}() {
		return CloudPubState{State: "skip"}, nil
	}
	st := InspectCloudPub(nodeID)
	switch st.State {
	case "no_login":
		// 未登录：无法比对云端。允许写入（离线恢复/单测），但标状态；
		// 真正的换钥拦截发生在「已登录且能读到云端公钥」时。
		return st, nil
	case "unreachable":
		return st, nil
	}
	if st.State == "empty" || st.CloudPub == "" {
		// 首建：登录即可绑定
		st.State = "empty"
		return st, nil
	}
	if strings.EqualFold(strings.TrimSpace(st.CloudPub), strings.TrimSpace(derivedPub)) {
		st.State = "match"
		return st, nil // 恢复身份
	}
	// 不一致 = 换钥（或拿错私钥）
	st.State = "mismatch"
	if !confirmRekey {
		return st, &IdentityGateErr{
			Code: "key_mismatch",
			Message: "私钥与云端公钥不一致。请二选一：" +
				"① 改用与云端公钥匹配的正确私钥重新导入；" +
				"② 确认要换新身份（换钥）后携带 --confirm-rekey 重试。" +
				"换钥会替换云端公钥、对端需重新拉配置，且要求新鲜确认（step-up）。",
		}
	}
	// step-up 已确认 → 允许替换
	return st, nil
}

// activeNodeIDForGate 当前激活节点 id。
func activeNodeIDForGate() string {
	_, mode, err := (&config.Store{}).ActiveSource()
	if err != nil || mode == nil {
		return ""
	}
	return mode.Active.NodeID
}

// keysDirForGate 诊断用。
func keysDirForGate() string {
	d, _, _ := configFaceKeysDir()
	return d
}

var _ = utils.GetMyNetHome
