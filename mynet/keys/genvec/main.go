package main

import (
	"encoding/hex"
	"encoding/json"
	"os"

	"github.com/mynet-club/mynet_agent-V2/mynet/keys"
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
