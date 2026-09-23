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
	priv, err := hex.DecodeString(strings.TrimSpace(nodePrivHex))
	if err != nil || len(priv) != 32 {
		return nil, fmt.Errorf("node priv must be 32B hex")
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
