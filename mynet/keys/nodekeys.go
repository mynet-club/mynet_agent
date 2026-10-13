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

// UpdateNodeMetaBackPub 回填 meta.json 的后端节点面公钥（服务器侧公开参数，非秘密）。
//
// 背景：早期 keys set/generate 把 back_pub_hex 硬写成空串，节点面因此永远装配
// 不了（mynetd transport 报「NodeID 必填」，隧道只能靠手工 start 活着）。
// meta 不存在或解析失败时按已知信息重建 —— 不覆盖 node_id（传空则保留原值）。
func UpdateNodeMetaBackPub(keysDir, nodeID, backPubHex string) error {
	if backPubHex == "" {
		return nil
	}
	if err := os.MkdirAll(keysDir, 0o700); err != nil {
		return err
	}
	meta, err := LoadNodeMeta(keysDir)
	if err != nil {
		meta = &NodeKeyMeta{SignAlgorithm: "hmac-sha256-dh"}
	}
	if nodeID != "" {
		meta.NodeID = nodeID
	}
	if meta.BackPubHex == backPubHex {
		return nil
	}
	meta.BackPubHex = backPubHex
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(keysDir, "meta.json"), append(raw, '\n'), 0o600)
}

// DerivePublicKeyFromPrivateHex 从私钥推导公钥（按算法）。
//   - ed25519/gnb：私钥 128 hex（processed）→ GNBPublicKeyFromPrivateHex
//   - x25519/wireguard：私钥 64 hex → ecdh X25519
//
// 导入密钥时用于**配对验证**（spec：公私钥必须一对，不一致以私钥推导为准）。
func DerivePublicKeyFromPrivateHex(privHex, algorithm string) (string, error) {
	privHex = strings.TrimSpace(privHex)
	switch algorithm {
	case "ed25519", "gnbkey", "gnb", "":
		// 128 hex = gnb processed key；32 字节 hex（64 chars）也按 ed25519 seed 试
		if len(privHex) == 64 {
			// 64 hex = 32 字节 seed → 按 gnb 处理后再推
			pub, err := GNBPublicKeyFromPrivateHex(privHex)
			if err != nil {
				return "", err
			}
			return hex.EncodeToString(pub), nil
		}
		if len(privHex) == 128 {
			pub, err := GNBPublicKeyFromPrivateHex(privHex)
			if err != nil {
				return "", err
			}
			return hex.EncodeToString(pub), nil
		}
		return "", fmt.Errorf("ed25519 私钥长度应为 64/128 hex，得到 %d", len(privHex))
	case "x25519", "curve25519", "wireguard", "wg":
		if len(privHex) != 64 {
			return "", fmt.Errorf("x25519 私钥长度应为 64 hex，得到 %d", len(privHex))
		}
		b, err := hex.DecodeString(privHex)
		if err != nil {
			return "", err
		}
		var sk [32]byte
		copy(sk[:], b)
		priv, err := ecdh.X25519().NewPrivateKey(sk[:])
		if err != nil {
			return "", err
		}
		return hex.EncodeToString(priv.PublicKey().Bytes()), nil
	default:
		return "", fmt.Errorf("未知算法 %q", algorithm)
	}
}

// InferAlgorithmFromPrivateHex 从私钥长度推断算法（导入时算法未知的场景）。
func InferAlgorithmFromPrivateHex(privHex string) string {
	privHex = strings.TrimSpace(privHex)
	switch len(privHex) {
	case 128:
		return "ed25519" // gnb processed
	case 64:
		return "x25519" // wg 或 gnb seed，先按 x25519 试；失败再按 ed25519
	default:
		return ""
	}
}
