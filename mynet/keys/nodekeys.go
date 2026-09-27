package keys

// nodekeys —— keys/ 目录身份读写与生成（配置树 v2 的 conf/**/keys/）。
//
// 供 mynet setup 与 mynetd/localapi 共用，避免各写一套：
//   node.private  0600 hex
//   node.public   0644 hex
//   meta.json     0600 node_id / algorithm / back_pub_hex / sign_algorithm

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NodeKeyMeta keys/meta.json。
type NodeKeyMeta struct {
	NodeID        string `json:"node_id"`
	Algorithm     string `json:"algorithm"`
	BackPubHex    string `json:"back_pub_hex"`
	SignAlgorithm string `json:"sign_algorithm"`
}

// GenerateNodeKeys gnb=ed25519 128hex；wg=x25519 64hex。
func GenerateNodeKeys(vpnType string) (privHex, pubHex, algorithm string, err error) {
	switch vpnType {
	case "gnb":
		kp, err := GenerateGNBKeyPair()
		if err != nil {
			return "", "", "", err
		}
		privHex, pubHex = kp.ToHex()
		return privHex, pubHex, "ed25519", nil
	case "wireguard", "wg":
		priv, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return "", "", "", err
		}
		return hex.EncodeToString(priv.Bytes()), hex.EncodeToString(priv.PublicKey().Bytes()), "x25519", nil
	default:
		return "", "", "", fmt.Errorf("未知 vpn-type %q", vpnType)
	}
}

// WriteNodeKeys 落盘 node.private + node.public + meta.json。
func WriteNodeKeys(dir, privHex, pubHex, algorithm, nodeID, backPub string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "node.private"), []byte(privHex+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "node.public"), []byte(pubHex+"\n"), 0o644); err != nil {
		return err
	}
	meta := NodeKeyMeta{
		NodeID:        nodeID,
		Algorithm:     algorithm,
		BackPubHex:    backPub,
		SignAlgorithm: "hmac-sha256-dh",
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), append(raw, '\n'), 0o600)
}

// LoadNodeKeys 读 keys/ 下身份。meta.json 可选（algorithm/nodeID 可空）。
//
// 文件名兼容两套：
//   - 标准：node.private / node.public（配置树 v2）
//   - gnb security：<nodeid>.private / <nodeid>.public（从 C:\gnb-win 迁入时的原名）
//
// 183 实测：手迁 gnb 密钥时沿用 `<nodeid>.private`，mynetd 只认 node.private
// 直接报「读节点密钥失败」。这里自动认，避免再踩。
func LoadNodeKeys(keysDir string) (privHex, pubHex, algorithm, nodeID string, err error) {
	privB, pubB, err := readKeyPair(keysDir)
	if err != nil {
		return "", "", "", "", err
	}
	algorithm, nodeID = "", ""
	if meta, e := LoadNodeMeta(keysDir); e == nil {
		algorithm, nodeID = meta.Algorithm, meta.NodeID
	}
	return strings.TrimSpace(string(privB)), strings.TrimSpace(string(pubB)), algorithm, nodeID, nil
}

// LoadNodePrivateKey 只读私钥（节点面签名用）。公钥可缺。
// 兼容 node.private 与 gnb 的 <nodeid>.private。
func LoadNodePrivateKey(keysDir string) (privHex string, err error) {
	priv, err := os.ReadFile(filepath.Join(keysDir, "node.private"))
	if err != nil {
		if alt := findKeyFile(keysDir, ".private"); alt != "" {
			priv, err = os.ReadFile(alt)
		}
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(priv)), nil
}

func readKeyPair(keysDir string) (priv, pub []byte, err error) {
	privStr, err := LoadNodePrivateKey(keysDir)
	if err != nil {
		return nil, nil, err
	}
	pub, err = os.ReadFile(filepath.Join(keysDir, "node.public"))
	if err != nil {
		if alt := findKeyFile(keysDir, ".public"); alt != "" {
			pub, err = os.ReadFile(alt)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	return []byte(privStr), pub, nil
}

// findKeyFile 找 keys 目录下唯一以 suffix 结尾的文件（跳过 meta）。
func findKeyFile(dir, suffix string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var found string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		if found != "" {
			return "" // 多个候选：不猜
		}
		found = filepath.Join(dir, e.Name())
	}
	return found
}

// LoadNodeMeta 读 keys/meta.json。
func LoadNodeMeta(keysDir string) (*NodeKeyMeta, error) {
	raw, err := os.ReadFile(filepath.Join(keysDir, "meta.json"))
	if err != nil {
		return nil, err
	}
	var m NodeKeyMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
