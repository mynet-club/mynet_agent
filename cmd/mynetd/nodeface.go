package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent-V2/mynet/keys"
	"github.com/mynet-club/mynet_agent-V2/mynetd/commands"
)

// 节点面（node ↔ back）签名与命令结果上行帧。
//
// 为什么放在 cmd/ 而不是 mynetd/：transport 是**零密码学**的纯搬运层（C-3），
// 而 mynetd/ 目前对 mynet/ 零依赖（guard B2 的分层意图）。cmd/ 是组合根，
// 同时依赖两棵树本就是它的职责所在。

// resultMethod / resultPathFmt 对应 back 的
// `PATCH /api/v1/monitor/commands/{id}/result`（NodeHeartbeatController::reportResult）。
const (
	resultMethod  = "PATCH"
	resultPathFmt = "/api/v1/monitor/commands/%s/result"
)

// nodeIdentity 节点身份：从 mynet.conf 读出的 node_id + 节点面签名器。
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

// mynetConfPath 返回 $MYNET_HOME/conf/mynet.conf。
//
// ⚠️ 必须与 `mynet/cmd_setup.go:79-84` 的 confDir() 保持一致——那是写入方。
// 注意 `shared/utils.ConfigDir()` 是**另一套约定**（MYNET_CONFIG_HOME / .mynet），
// 用它找不到 setup 写出的文件。两套约定并存已登记为待收敛项。
func mynetConfPath() string {
	if v := os.Getenv("MYNET_HOME"); v != "" {
		return filepath.Join(v, "conf", "mynet.conf")
	}
	return filepath.Join("conf", "mynet.conf")
}

// loadNodeIdentity 读 mynet.conf 并构造签名器。
//
// 缺文件或缺字段一律**明确报错**，不静默降级：静默降级会导致「结果发出去了但
// back 验签失败」，症状是命令永远停在「已下发」，极难定位。
func loadNodeIdentity(path string) (*nodeIdentity, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读节点配置失败（先运行 mynet setup）: %w", err)
	}
	defer f.Close()

	kv := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}

	nodeID := kv["NODE_ID"]
	priv := kv["NODE_PRIV_HEX"]
	backPub := kv["BACK_PUB_HEX"]

	var missing []string
	if nodeID == "" {
		missing = append(missing, "NODE_ID")
	}
	if priv == "" {
		missing = append(missing, "NODE_PRIV_HEX")
	}
	if backPub == "" {
		missing = append(missing, "BACK_PUB_HEX")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%s 缺少字段: %s", path, strings.Join(missing, ", "))
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
