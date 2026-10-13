// genvec — NodeFace 联测向量生成器。
//
// 用途：为 mynet/keys/testdata/node_face_vectors.json 产出 Go/PHP 双端同签名
// 的固定测试向量（C-2 合同）。改密钥布局或签名算法后应重跑本工具并提交新向量。
// 用法: go run ./mynet/keys/genvec > mynet/keys/testdata/node_face_vectors.json
//
// 实验用的私钥→公钥推导验证原在 cmd/keyprove，已于 G1 删除（零消费者）。
package main

import (
	"encoding/hex"
	"encoding/json"
	"os"

	"github.com/mynet-club/mynet_agent/mynet/keys"
)

func main() {
	nodePriv := "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	backPriv := "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
	back, err := keys.GenerateX25519FromPrivateHex(backPriv)
	if err != nil {
		panic(err)
	}
	s, err := keys.NewNodeFaceSigner(nodePriv, hex.EncodeToString(back.Public))
	if err != nil {
		panic(err)
	}
	method, path, ts, body := "POST", "/api/v1/node/heartbeat", "1700000000", `{"ok":1}`
	out := map[string]any{
		"comment": "C-2 双端联测向量：Go/PHP 必须同签名",
		"cases": []map[string]string{{
			"name":          "heartbeat",
			"node_priv_hex": nodePriv,
			"back_priv_hex": backPriv,
			"back_pub_hex":  hex.EncodeToString(back.Public),
			"method":        method,
			"path":          path,
			"ts":            ts,
			"body":          body,
			"shared_hex":    hex.EncodeToString(s.Shared()),
			"signature":     s.Sign(method, path, ts, body),
		}},
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
