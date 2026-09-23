package keys

import (
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/hex"
	"fmt"

	"filippo.io/edwards25519"
)

// SignGNB 用 **GNB 格式**私钥对 message 签名，产出标准 Ed25519 签名（R‖S，64 字节）。
//
// 为什么不能直接用 crypto/ed25519.Sign：GNB 私钥是 `SHA512(seed)` 的 64 字节，
// **不是** stdlib 期望的 `seed‖SHA512(seed)`（见 spec/node-keying.md §1.2）。
// 反向则没有问题：GNB 的 verify 就是标准 RFC8032 方程，所以本函数产出的签名
// 可以被 `crypto/ed25519.Verify` 直接验（见 spec/node-keying.md §2.1）。
//
// 严格按 `mynet_gnb/src/libs/ed25519/sign.c` 实现：
//
//	r    = SHA512(private_key[32..64] ‖ message) ; mod L
//	R    = r·B                          ; signature[0..32]
//	hram = SHA512(R ‖ public_key ‖ message) ; mod L
//	S    = hram·private_key[0..32] + r  ; signature[32..64]
//
// 输入 privateKeyHex 必须是 64 字节（128 hex 字符）的 GNB 私钥——
// 只有前 32 字节无法重现 sign.c 的 nonce 派生，故明确拒绝而不是猜。
func SignGNB(privateKeyHex string, message []byte) ([]byte, error) {
	sk, err := hex.DecodeString(trimHex(privateKeyHex))
	if err != nil {
		return nil, fmt.Errorf("gnb sign: 私钥 hex 解析失败: %w", err)
	}
	if len(sk) != 64 {
		return nil, fmt.Errorf("gnb sign: 私钥须为 64 字节（GNB 格式 SHA512(seed)），实际 %d", len(sk))
	}

	// 公钥由私钥派生（同一套 GNB 算法），避免调用方传错配对
	pub, err := GNBPublicKeyFromPrivateKey(sk[:32])
	if err != nil {
		return nil, fmt.Errorf("gnb sign: 派生公钥失败: %w", err)
	}

	return signGNBRaw(sk, pub, message)
}

// SignGNBWithPublicKey 与 SignGNB 相同，但显式传入公钥（调用方已确认配对时省一次派生）。
func SignGNBWithPublicKey(privateKeyHex, publicKeyHex string, message []byte) ([]byte, error) {
	sk, err := hex.DecodeString(trimHex(privateKeyHex))
	if err != nil {
		return nil, fmt.Errorf("gnb sign: 私钥 hex 解析失败: %w", err)
	}
	if len(sk) != 64 {
		return nil, fmt.Errorf("gnb sign: 私钥须为 64 字节，实际 %d", len(sk))
	}
	pub, err := hex.DecodeString(trimHex(publicKeyHex))
	if err != nil {
		return nil, fmt.Errorf("gnb sign: 公钥 hex 解析失败: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("gnb sign: 公钥须为 %d 字节，实际 %d", ed25519.PublicKeySize, len(pub))
	}
	return signGNBRaw(sk, pub, message)
}

// signGNBRaw 是 sign.c 的逐行对应实现。
func signGNBRaw(sk, pub, message []byte) ([]byte, error) {
	// a = private_key[0..32]（clamp 后）mod L
	a, err := scalarFromGNBPrivate(sk[:32])
	if err != nil {
		return nil, fmt.Errorf("gnb sign: 标量归约失败: %w", err)
	}

	// r = SHA512(private_key[32..64] ‖ message) mod L
	rHash := sha512.New()
	rHash.Write(sk[32:64])
	rHash.Write(message)
	r, err := edwards25519.NewScalar().SetUniformBytes(rHash.Sum(nil))
	if err != nil {
		return nil, fmt.Errorf("gnb sign: nonce 归约失败: %w", err)
	}

	// R = r·B
	R := edwards25519.NewIdentityPoint().ScalarBaseMult(r)

	sig := make([]byte, ed25519.SignatureSize)
	copy(sig[:32], R.Bytes())

	// hram = SHA512(R ‖ public_key ‖ message) mod L
	hHash := sha512.New()
	hHash.Write(sig[:32])
	hHash.Write(pub)
	hHash.Write(message)
	hram, err := edwards25519.NewScalar().SetUniformBytes(hHash.Sum(nil))
	if err != nil {
		return nil, fmt.Errorf("gnb sign: hram 归约失败: %w", err)
	}

	// S = hram·a + r
	S := edwards25519.NewScalar().MultiplyAdd(hram, a, r)
	copy(sig[32:], S.Bytes())

	return sig, nil
}

// scalarFromGNBPrivate 把 GNB 私钥的前 32 字节还原为标量。
//
// 先按 keypair.c 的方式 clamp（&=248 / &=63 / |=64），再做 mod L 归约
// （用零扩展 64 字节 + SetUniformBytes，与 C 的 sc_reduce 同构）。
//
// 为什么要显式 clamp 而不是假定调用方已 clap：GNB 的 keypair.c 是**就地 clamp 后存储**，
// 而当前 agent 的 GenerateGNBKeyPairFromSeed 的 `PrivateKey` 字段保存的是**未 clamp**
// 的 SHA512(seed)（clamp 只落在 ProcessedPrivateKey 上）。显式 clamp 对两种输入都正确，
// 且 clamp 是幂等的。
func scalarFromGNBPrivate(first32 []byte) (*edwards25519.Scalar, error) {
	if len(first32) != 32 {
		return nil, fmt.Errorf("标量须为 32 字节，实际 %d", len(first32))
	}
	var wide [64]byte
	copy(wide[:], first32)
	wide[0] &= 248
	wide[31] &= 63
	wide[31] |= 64
	return edwards25519.NewScalar().SetUniformBytes(wide[:])
}

func trimHex(s string) string {
	// 容忍空白与 0x 前缀
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	if len(s) > 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		s = s[2:]
	}
	return s
}
