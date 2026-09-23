// Package keys — A-1 密钥生成与编码边界。
//
// 编码两层（**不可混**）：
//   - node_keys / key_exchanges 存 **hex**（B-3 统一）
//   - 写 wg 配置 / `wg set` 时转 **base64 44 字符**（wg 原生）
//
// 布局（与编码正交）：
//   - WireGuard/X25519：32 字节标量
//   - GNB ed25519：私钥是 `clamp(SHA512(seed))` 128 hex，**不是** `seed‖pub`
package keys

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// X25519KeyPair 节点面 DH + WireGuard 数据面共用（一套密钥通吃）。
type X25519KeyPair struct {
	Private []byte // 32B
	Public  []byte // 32B
}

// GenerateX25519 生成密钥对（与 `wg genkey | wg pubkey` 同构）。
func GenerateX25519() (*X25519KeyPair, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("x25519 generate: %w", err)
	}
	return &X25519KeyPair{
		Private: priv.Bytes(),
		Public:  priv.PublicKey().Bytes(),
	}, nil
}

// ToHex 落库编码（B-3）：private 64 hex，public 64 hex。
func (k *X25519KeyPair) ToHex() (privateHex, publicHex string) {
	return hex.EncodeToString(k.Private), hex.EncodeToString(k.Public)
}

// ToBase64 写 wg 配置的编码（wg 原生 44 字符）。
func (k *X25519KeyPair) ToBase64() (privateB64, publicB64 string) {
	return base64.StdEncoding.EncodeToString(k.Private),
		base64.StdEncoding.EncodeToString(k.Public)
}

// PrivateBase64FromHex 从库里的 hex 转 wg 用的 base64（A-1 边界转换）。
func PrivateBase64FromHex(privateHex string) (string, error) {
	raw, err := hex.DecodeString(trimHex(privateHex))
	if err != nil {
		return "", fmt.Errorf("private hex: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("x25519 private must be 32 bytes, got %d", len(raw))
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// PublicBase64FromHex 同上，公钥。
func PublicBase64FromHex(publicHex string) (string, error) {
	raw, err := hex.DecodeString(trimHex(publicHex))
	if err != nil {
		return "", fmt.Errorf("public hex: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("x25519 public must be 32 bytes, got %d", len(raw))
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// PublicHexFromBase64 wg show 读出的 base64 → 库内 hex。
func PublicHexFromBase64(publicB64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(publicB64)
	if err != nil {
		return "", fmt.Errorf("public b64: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("x25519 public must be 32 bytes, got %d", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

// DH 节点面共享密钥（X25519）——C-2 的基元。
func DH(localPrivate, peerPublic []byte) ([]byte, error) {
	if len(localPrivate) != 32 || len(peerPublic) != 32 {
		return nil, fmt.Errorf("dh: need 32-byte keys")
	}
	priv, err := ecdh.X25519().NewPrivateKey(localPrivate)
	if err != nil {
		return nil, err
	}
	pub, err := ecdh.X25519().NewPublicKey(peerPublic)
	if err != nil {
		return nil, err
	}
	return priv.ECDH(pub)
}

// x25519FromPrivateHex 由私钥 hex 还原密钥对（联测用）
// GenerateX25519FromPrivateHex 由私钥 hex 还原密钥对
func GenerateX25519FromPrivateHex(privateHex string) (*X25519KeyPair, error) {
	return x25519FromPrivateHex(privateHex)
}

func x25519FromPrivateHex(privateHex string) (*X25519KeyPair, error) {
	raw, err := hex.DecodeString(trimHex(privateHex))
	if err != nil {
		return nil, err
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("need 32B")
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return nil, err
	}
	return &X25519KeyPair{Private: priv.Bytes(), Public: priv.PublicKey().Bytes()}, nil
}
