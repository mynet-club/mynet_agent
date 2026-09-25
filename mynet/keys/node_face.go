package keys

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// NodeFaceSigner — C-2 节点侧签名，与 back 的 NodeFaceAuthService 对称。
//
//	shared    = X25519(node_priv, back_pub)
//	message   = METHOD \n path \n ts \n sha256(body)
//	signature = base64(HMAC-SHA256(shared, message))
//
// 公钥可公开；强度在 shared。X25519 签不了名故走 DH 不走签名。
type NodeFaceSigner struct {
	nodePriv []byte // 32B
	backPub  []byte // 32B（已 montgomery）
	shared   []byte
}

// NewNodeFaceSigner nodePrivHex / backPubHex 为 64 hex（B-3）。
// algorithm: x25519|curve25519 直接 DH；ed25519 时 backPub 需已是 montgomery u
// （由调用方完成 edwards→montgomery，或用 back 的转换结果）。
func NewNodeFaceSigner(nodePrivHex, backPubHex string) (*NodeFaceSigner, error) {
	priv, err := decodeNodeFacePrivate(nodePrivHex)
	if err != nil {
		return nil, err
	}
	pub, err := hex.DecodeString(strings.TrimSpace(backPubHex))
	if err != nil || len(pub) != 32 {
		return nil, fmt.Errorf("back pub must be 32B hex")
	}
	shared, err := DH(priv, pub)
	if err != nil {
		return nil, err
	}
	return &NodeFaceSigner{nodePriv: priv, backPub: pub, shared: shared}, nil
}

// decodeNodeFacePrivate 接受两种节点私钥布局，统一取出用于 X25519 DH 的 32 字节标量。
//
//	64 hex（32B）—— 原生 X25519，wireguard 节点走这条
//	128 hex（64B）—— **GNB 布局** `clamp(SHA512(seed))`，gnb 节点走这条
//
// 为什么 GNB 的能直接拿来用：GNB 落盘的 64 字节里，**前 32 字节就是 clamp 过的标量 s**
// （见 libs/ed25519/keypair.c：sha512(seed) 后 clamp，再取前 32 字节做 ge_scalarmult_base）。
// 而 ed25519 公钥 A = s·G_edwards，back 侧对 algorithm=ed25519 会先做
// edwards→montgomery 得到 s·G_mont，再 scalarmult(back_priv, s·G_mont) = back_priv·s·G；
// 本侧 X25519(s, back_pub) = s·back_priv·G —— 两者相等。这就是 §1.4 说的
// 「gnb 走 edwards→montgomery、wg 直接 DH，只差一个公钥格式转换」。
//
// ⚠️ Go 的 X25519 会自己再 clamp 一次，而 GNB 的标量已经 clamp 过 —— 同样的位运算
// 幂等，结果不变。但**不能**因此就把后 32 字节丢掉不管：它们参与 ed25519 的 nonce 派生
// （签名那边要用），只是不参与 DH。
//
// 此前这里只接受 32B，于是 gnb 节点一律「node priv must be 32B hex」——
// 节点面根本无法启动，而 mynet.conf 里的私钥恰恰就是 gnb 生成出来的 128 hex。
func decodeNodeFacePrivate(nodePrivHex string) ([]byte, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(nodePrivHex))
	if err != nil {
		return nil, fmt.Errorf("node priv must be hex: %w", err)
	}
	switch len(raw) {
	case 32:
		return raw, nil
	case 64:
		// GNB 布局：前 32 字节是 clamp 过的标量
		return raw[:32], nil
	default:
		return nil, fmt.Errorf("node priv must be 32B (x25519) or 64B (gnb clamp(SHA512(seed))) hex, got %dB", len(raw))
	}
}

// Shared 共享密钥（调试/对测）
func (s *NodeFaceSigner) Shared() []byte { return s.shared }

// Sign 生成 X-Node-Signature
func (s *NodeFaceSigner) Sign(method, path, ts, body string) string {
	return signShared(s.shared, method, path, ts, body)
}

// SignRequest 便捷：用当前时间
func (s *NodeFaceSigner) SignRequest(method, path, body string) (ts, sig string) {
	ts = fmt.Sprintf("%d", time.Now().Unix())
	return ts, s.Sign(method, path, ts, body)
}

// Verify 校验（对称，便于单测）
func (s *NodeFaceSigner) Verify(method, path, ts, body, sig string) bool {
	want := s.Sign(method, path, ts, body)
	return hmac.Equal([]byte(want), []byte(sig))
}

func signShared(shared []byte, method, path, ts, body string) string {
	msg := strings.ToUpper(method) + "\n" + path + "\n" + ts + "\n" + hex.EncodeToString(sha256sum(body))
	mac := hmac.New(sha256.New, shared)
	mac.Write([]byte(msg))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func sha256sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}
