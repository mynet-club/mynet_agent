package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent-V2/mynet/keys"
	"github.com/mynet-club/mynet_agent-V2/mynetd/commands"
	"github.com/mynet-club/mynet_agent-V2/shared/utils"
)

// 节点面（node ↔ back）签名与命令结果上行帧。
//
// 为什么放在 cmd/ 而不是 mynetd/：transport 是**零密码学**的纯搬运层（C-3），
// 而 mynetd/ 目前对 mynet/ 零依赖（guard B2 的分层意图）。cmd/ 是组合根，
// 同时依赖两棵树本就是它的职责所在。

// resultMethod / resultPathFmt 对应 back 的
// `PATCH /api/v1/node/commands/{id}/result`（NodeHeartbeatController::reportResult）。
const (
	resultMethod  = "PATCH"
	resultPathFmt = "/api/v1/node/commands/%s/result"
)

// nodeIdentity 节点身份：从配置树 v2 的 keys/ 读出的 node_id + 节点面签名器。
type nodeIdentity struct {
	nodeID string
	signer *keys.NodeFaceSigner
}

// resultFrame 命令结果的上行帧。
//
// gateway 收到后**逐字重放** method/path/body 与三个签名头到 back，自身不做任何
// 密码学运算（C-3：gateway 不鉴权）。签名覆盖的是 back 将看到的那个 HTTP 请求
// （`METHOD\npath\nts\nsha256(body)`，见 mynet/keys/node_face.go:66），
// 所以 path 与 body 必须与实际转发的一致，不能在中途改写。
type resultFrame struct {
	T      string `json:"t"`
	NodeID string `json:"node_id"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   string `json:"body"`
	TS     string `json:"ts"`
	Sig    string `json:"sig"`
}

// nodeKeysDir 密钥挂**节点**：conf/{local|online/t}/nodes/{node_id}/keys
// 旧布局 conf/**/keys 由 ResolveKeysDir 回退读取。
func nodeKeysDir() (string, error) {
	home := os.Getenv("MYNET_HOME")
	if home == "" {
		home = "."
	}
	nodeID := ""
	space := filepath.Join(home, "conf", "local")
	if raw, err := os.ReadFile(filepath.Join(home, "mode.json")); err == nil {
		var mode struct {
			Mode   string `json:"mode"`
			Active struct {
				TenantID string `json:"tenant_id"`
				NodeID   string `json:"node_id"`
			} `json:"active"`
		}
		if json.Unmarshal(raw, &mode) == nil {
			nodeID = mode.Active.NodeID
			if mode.Mode == "online" && mode.Active.TenantID != "" {
				space = filepath.Join(home, "conf", "online", mode.Active.TenantID)
			}
		}
	}
	return utils.ResolveKeysDir(space, nodeID), nil
}

// loadNodeIdentity 读 keys/ 并构造签名器。
//
// 缺文件或缺字段一律**明确报错**，不静默降级：静默降级会导致「结果发出去了但
// back 验签失败」，症状是命令永远停在「已下发」，极难定位。
func loadNodeIdentity(_ string) (*nodeIdentity, error) {
	dir, err := nodeKeysDir()
	if err != nil {
		return nil, err
	}
	// 节点面只需私钥；公钥可缺（LoadNodePrivateKey 认两套文件名）
	priv, err := keys.LoadNodePrivateKey(dir)
	if err != nil {
		return nil, fmt.Errorf("读节点密钥失败（先 mynet setup --local|--online）: %w", err)
	}
	nodeID := ""
	backPub := ""
	if metaB, err := os.ReadFile(filepath.Join(dir, "meta.json")); err == nil {
		var meta struct {
			NodeID     string `json:"node_id"`
			BackPubHex string `json:"back_pub_hex"`
		}
		if json.Unmarshal(metaB, &meta) == nil {
			nodeID = meta.NodeID
			backPub = meta.BackPubHex
		}
	}
	if nodeID == "" {
		// 退回 mode.json 的 active.node_id
		if home := os.Getenv("MYNET_HOME"); home != "" {
			if raw, err := os.ReadFile(filepath.Join(home, "mode.json")); err == nil {
				var m struct {
					Active struct {
						NodeID string `json:"node_id"`
					} `json:"active"`
				}
				if json.Unmarshal(raw, &m) == nil {
					nodeID = m.Active.NodeID
				}
			}
		}
	}

	var missing []string
	if nodeID == "" {
		missing = append(missing, "node_id")
	}
	if priv == "" {
		missing = append(missing, "node.private")
	}
	if backPub == "" {
		missing = append(missing, "back_pub_hex")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%s 缺少: %s", dir, strings.Join(missing, ", "))
	}

	signer, err := keys.NewNodeFaceSigner(priv, backPub)
	if err != nil {
		return nil, fmt.Errorf("构造节点面签名器失败: %w", err)
	}
	return &nodeIdentity{nodeID: nodeID, signer: signer}, nil
}

// resultBody 是 back `reportResult` 校验的载荷：
// `{success: bool, message: ?string(≤10240), result: ?array}`。
type resultBody struct {
	Success bool           `json:"success"`
	Message string         `json:"message,omitempty"`
	Result  map[string]any `json:"result,omitempty"`
}

// encodeResult 把命令结果编成签名后的上行帧。
func (id *nodeIdentity) encodeResult(res commands.Result) ([]byte, error) {
	path := fmt.Sprintf(resultPathFmt, res.ID)

	// message 给人类看的摘要；完整 stdout/stderr 放 result，
	// 由 back 侧的 10240 字符上限自行截断（NodeHeartbeatController:295）。
	msg := "命令执行成功"
	if !res.OK {
		msg = "命令执行失败"
		if res.Error != "" {
			msg = res.Error
		}
	}

	body := resultBody{
		Success: res.OK,
		Message: msg,
		Result: map[string]any{
			"stdout":     res.Stdout,
			"stderr":     res.Stderr,
			"elapsed_ms": res.Elapsed,
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("编码结果 body: %w", err)
	}

	ts, sig := id.signer.SignRequest(resultMethod, path, string(raw))

	frame := resultFrame{
		T:      "result",
		NodeID: id.nodeID,
		Method: resultMethod,
		Path:   path,
		Body:   string(raw),
		TS:     ts,
		Sig:    sig,
	}
	out, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("编码结果帧: %w", err)
	}
	return out, nil
}
