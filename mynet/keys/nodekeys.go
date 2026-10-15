package keys

// nodekeys —— 节点身份读写与生成（D4-E：节点唯一存放位 conf/<form>/<nodeId>/）。
//
// 密钥按**驱动原生格式**同居节点目录（不再留 hex/base64 双形态副本）：
//
//	gnb: security/<nodeID>.private  128 hex，**无换行**（gnb_keys.c:68-73）
//	     security/<nodeID>.public    64 hex，**无换行**（gnb_keys.c:84-89）
//	wg:  wg.conf 的 [Interface] PrivateKey = base64 44（wg 原生格式，即密钥存放本身）
//
// meta.json（node_id / algorithm / back_pub_hex / sign_algorithm）同居节点目录，
// 是身份元数据、不是密钥材料。hex↔base64 只在 DB/上报边界转换。
// 旧布局（{space}/nodes/{id}/keys/node.{private,public}）只作**读**回退（未迁移节点）。

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
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

// WriteNodeKeys 落盘节点身份 —— **驱动原生格式**（D4-E，dir = 节点唯一存放位）：
//
//	ed25519/gnb → security/<nodeID>.{private,public}（128/64 hex 无换行）+ meta.json
//	x25519/wg   → wg.conf 的 PrivateKey（base64 44）+ meta.json
//
// 这是**唯一写面**；旧 node.{private,public} 形态不再写。
func WriteNodeKeys(dir, privHex, pubHex, algorithm, nodeID, backPub string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if nodeID == "" {
		if meta, e := LoadNodeMeta(dir); e == nil {
			nodeID = meta.NodeID
		}
	}
	switch algorithm {
	case "x25519", "curve25519", "wireguard", "wg":
		if err := writeWGPrivateKey(dir, privHex); err != nil {
			return err
		}
	default:
		// gnb 形态：security/ 布局（gnb 协议内布局不动）
		if nodeID == "" {
			return fmt.Errorf("gnb 密钥需要 node_id（security/<nodeID>.private）")
		}
		if err := os.MkdirAll(filepath.Join(dir, "security"), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "security", nodeID+".private"),
			[]byte(strings.TrimSpace(privHex)), 0o600); err != nil {
			return err
		}
		if pubHex != "" {
			if err := os.WriteFile(filepath.Join(dir, "security", nodeID+".public"),
				[]byte(strings.TrimSpace(pubHex)), 0o644); err != nil {
				return err
			}
		}
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

// HexPrivToWGBase64 hex 私钥 → wg 原生 base64 44（DB/上报边界转换）。
func HexPrivToWGBase64(privHex string) (string, error) {
	privHex = strings.TrimSpace(privHex)
	b, err := hex.DecodeString(privHex)
	if err != nil {
		return "", fmt.Errorf("私钥非 hex: %w", err)
	}
	if len(b) != 32 {
		return "", fmt.Errorf("x25519 私钥应为 32 字节，得到 %d", len(b))
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// Base64WGToHexPriv wg 原生 base64 → hex（DB/上报边界转换）。
func Base64WGToHexPriv(b64 string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", fmt.Errorf("wg 私钥非 base64: %w", err)
	}
	if len(b) != 32 {
		return "", fmt.Errorf("wg 私钥应为 32 字节，得到 %d", len(b))
	}
	return hex.EncodeToString(b), nil
}

// ReadWGPrivateKeyHex 从 wg.conf 的 [Interface] PrivateKey 读私钥（返回 hex）。
func ReadWGPrivateKeyHex(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "wg.conf"))
	if err != nil {
		return "", err
	}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			section = strings.ToLower(t)
			continue
		}
		if section != "[interface]" {
			continue
		}
		if i := strings.IndexByte(t, '='); i >= 0 && strings.EqualFold(strings.TrimSpace(t[:i]), "PrivateKey") {
			return Base64WGToHexPriv(t[i+1:])
		}
	}
	return "", fmt.Errorf("wg.conf 的 [Interface] 没有 PrivateKey")
}

// writeWGPrivateKey 把私钥写进 wg.conf 的 [Interface] PrivateKey（base64）。
// wg.conf 是 wg 原生形态 —— 密钥存放本身；已有文件只改这一行，不动其余内容。
func writeWGPrivateKey(dir, privHex string) error {
	b64, err := HexPrivToWGBase64(privHex)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "wg.conf")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	out := make([]string, 0, len(lines)+2)
	section := ""
	done := false
	hasKey := containsWGPrivateKey(lines)
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			section = strings.ToLower(t)
			out = append(out, line)
			if section == "[interface]" && !done && !hasKey {
				out = append(out, "PrivateKey = "+b64)
				done = true
			}
			continue
		}
		if section == "[interface]" {
			if i := strings.IndexByte(t, '='); i >= 0 && strings.EqualFold(strings.TrimSpace(t[:i]), "PrivateKey") {
				out = append(out, "PrivateKey = "+b64)
				done = true
				continue
			}
		}
		out = append(out, line)
	}
	if !done {
		// 文件不存在 / 没有 [Interface] 段：建最小可用形态
		out = append([]string{"[Interface]", "PrivateKey = " + b64}, out...)
	}
	return os.WriteFile(path, []byte(strings.TrimRight(strings.Join(out, "\n"), "\n")+"\n"), 0o600)
}

func containsWGPrivateKey(lines []string) bool {
	section := ""
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			section = strings.ToLower(t)
			continue
		}
		if section == "[interface]" {
			if i := strings.IndexByte(t, '='); i >= 0 && strings.EqualFold(strings.TrimSpace(t[:i]), "PrivateKey") {
				return true
			}
		}
	}
	return false
}

// LoadNodeKeys 读节点身份。meta.json 可选（algorithm/nodeID 可空）。
//
// dir = 节点唯一存放位（D4-E）。认三种形态，按序：
//  1. gnb 原生：security/<nodeID>.{private,public}（无换行 hex）
//  2. wg 原生：wg.conf 的 PrivateKey（base64 → hex，公钥按 x25519 推导）
//  3. 旧布局：node.private / node.public（未迁移节点只读兼容）
func LoadNodeKeys(keysDir string) (privHex, pubHex, algorithm, nodeID string, err error) {
	privHex, pubHex, err = readKeyPair(keysDir)
	if err != nil {
		// 无 private 时 public 仍可能在（先传公钥）——诊断/展示要能报出 has_public。
		if pub, perr := LoadNodePublicKey(keysDir); perr == nil && pub != "" {
			algorithm, nodeID = "", ""
			if meta, e := LoadNodeMeta(keysDir); e == nil {
				algorithm, nodeID = meta.Algorithm, meta.NodeID
			}
			return "", pub, algorithm, nodeID, err
		}
		return "", "", "", "", err
	}
	algorithm, nodeID = "", ""
	if meta, e := LoadNodeMeta(keysDir); e == nil {
		algorithm, nodeID = meta.Algorithm, meta.NodeID
	}
	return privHex, pubHex, algorithm, nodeID, nil
}

// LoadNodePrivateKey 只读私钥（节点面签名用，返回 hex）。公钥可缺。
// 认 gnb security/、wg.conf 与旧 node.private 三套形态。
func LoadNodePrivateKey(keysDir string) (privHex string, err error) {
	if p := findSecurityKeyFile(keysDir, ".private"); p != "" {
		if b, rerr := os.ReadFile(p); rerr == nil {
			return strings.TrimSpace(string(b)), nil
		}
	}
	if hexPriv, rerr := ReadWGPrivateKeyHex(keysDir); rerr == nil && hexPriv != "" {
		return hexPriv, nil
	}
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

// LoadNodePublicKey 只读公钥（可单独存在：先传公钥、后导私钥）。
// 认 gnb security/*.public 与旧 node.public 两套形态。
func LoadNodePublicKey(keysDir string) (pubHex string, err error) {
	if p := findSecurityKeyFile(keysDir, ".public"); p != "" {
		if b, rerr := os.ReadFile(p); rerr == nil {
			return strings.TrimSpace(string(b)), nil
		}
	}
	b, err := os.ReadFile(filepath.Join(keysDir, "node.public"))
	if err != nil {
		if alt := findKeyFile(keysDir, ".public"); alt != "" {
			b, err = os.ReadFile(alt)
		}
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func readKeyPair(keysDir string) (priv, pub string, err error) {
	privHex, err := LoadNodePrivateKey(keysDir)
	if err != nil {
		return "", "", err
	}
	// gnb security/ 形态的公钥
	if p := findSecurityKeyFile(keysDir, ".public"); p != "" {
		if b, rerr := os.ReadFile(p); rerr == nil {
			return privHex, strings.TrimSpace(string(b)), nil
		}
	}
	// wg 形态：公钥从私钥推导（hex↔base64 只在边界转；不落盘双形态）
	if len(strings.TrimSpace(privHex)) == 64 {
		if pubHex, derr := DerivePublicKeyFromPrivateHex(privHex, "x25519"); derr == nil {
			return privHex, pubHex, nil
		}
	}
	pubB, err := os.ReadFile(filepath.Join(keysDir, "node.public"))
	if err != nil {
		if alt := findKeyFile(keysDir, ".public"); alt != "" {
			pubB, err = os.ReadFile(alt)
		}
	}
	if err != nil {
		return "", "", err
	}
	return privHex, strings.TrimSpace(string(pubB)), nil
}

// findSecurityKeyFile 找 dir/security/ 下唯一以 suffix 结尾的文件。
func findSecurityKeyFile(dir, suffix string) string {
	sec := filepath.Join(dir, "security")
	entries, err := os.ReadDir(sec)
	if err != nil {
		return ""
	}
	found := ""
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		if found != "" {
			return "" // 多个候选：不猜
		}
		found = filepath.Join(sec, e.Name())
	}
	return found
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
