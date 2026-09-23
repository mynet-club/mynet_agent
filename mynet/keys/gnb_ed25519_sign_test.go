package keys

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"testing"
)

// TestSignGNB_MatchesStdlib 是本实现最强的正确性证据。
//
// GNB 的 sign.c 与 RFC 8032 同构：标量 = clamp(SHA512(seed)[0:32])，
// nonce = SHA512(SHA512(seed)[32:64] ‖ msg)。因此在**同一 seed**下，
// GNB 签名应当与 Go stdlib 的 ed25519.Sign **逐字节相同**。
// 这也解释了为什么 gateway 只做验签时可以用纯 stdlib（node-keying.md §2.1）。
func TestSignGNB_MatchesStdlib(t *testing.T) {
	for i := 0; i < 32; i++ {
		seed := make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			t.Fatalf("seed: %v", err)
		}

		// GNB 侧：sk = SHA512(seed)（64B），公钥由 clamp+scalarmult 派生
		kp, err := GenerateGNBKeyPairFromSeed(seed)
		if err != nil {
			t.Fatalf("GenerateGNBKeyPairFromSeed: %v", err)
		}
		got, err := SignGNB(hex.EncodeToString(kp.PrivateKey), []byte("hello mynet"))
		if err != nil {
			t.Fatalf("SignGNB: %v", err)
		}

		// stdlib 侧：同一 seed
		stdPriv := ed25519.NewKeyFromSeed(seed)
		want := ed25519.Sign(stdPriv, []byte("hello mynet"))

		if !bytes.Equal(got, want) {
			t.Fatalf("第 %d 轮签名与 stdlib 不一致\n got=%x\nwant=%x", i, got, want)
		}

		// 公钥也必须一致
		stdPub := stdPriv.Public().(ed25519.PublicKey)
		if !bytes.Equal(kp.PublicKey, stdPub) {
			t.Fatalf("第 %d 轮公钥与 stdlib 不一致", i)
		}
	}
}

// TestSignGNB_VerifiableByStdlib 验证「gateway 只做验签可用纯 stdlib」这一结论。
func TestSignGNB_VerifiableByStdlib(t *testing.T) {
	seed := make([]byte, 32)
	rand.Read(seed)
	kp, err := GenerateGNBKeyPairFromSeed(seed)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	msg := []byte("mynet-gateway-v1\n1001\n1790000000\nabcdef")
	sig, err := SignGNB(hex.EncodeToString(kp.PrivateKey), msg)
	if err != nil {
		t.Fatalf("SignGNB: %v", err)
	}

	if !ed25519.Verify(ed25519.PublicKey(kp.PublicKey), msg, sig) {
		t.Fatalf("stdlib 应能验证 GNB 签名")
	}
	// 消息被改 → 必须失败
	if ed25519.Verify(ed25519.PublicKey(kp.PublicKey), []byte("tampered"), sig) {
		t.Fatalf("篡改消息不应通过验证")
	}
	// 换公钥 → 必须失败
	other, _ := GenerateGNBKeyPair()
	if ed25519.Verify(ed25519.PublicKey(other.PublicKey), msg, sig) {
		t.Fatalf("错误公钥不应通过验证")
	}
}

// TestSignGNB_UsesSecondHalfForNonce 确认 nonce 取自私钥后半段（sign.c 的 private_key+32）。
//
// 若错误地只对前 32 字节做 nonce 派生，签名会与 stdlib 不同——本用例锁住这点。
func TestSignGNB_UsesSecondHalfForNonce(t *testing.T) {
	seed := make([]byte, 32)
	rand.Read(seed)
	kp, _ := GenerateGNBKeyPairFromSeed(seed)
	msg := []byte("nonce-probe")

	sig, err := SignGNB(hex.EncodeToString(kp.PrivateKey), msg)
	if err != nil {
		t.Fatalf("SignGNB: %v", err)
	}

	// 手工按 sign.c 复算 r 与 R
	h := sha512.New()
	h.Write(kp.PrivateKey[32:64])
	h.Write(msg)
	rBytes := h.Sum(nil)
	// stdlib 的签名首 32 字节即为 R，比对 r 的派生源是否正确
	stdSig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), msg)
	if !bytes.Equal(sig[:32], stdSig[:32]) {
		t.Fatalf("R 不一致：nonce 派生可能没有使用 private_key[32:64]（rBytes=%x）", rBytes[:8])
	}
}

func TestSignGNB_RejectsShortPrivateKey(t *testing.T) {
	// GNB 私钥必须是 64 字节；只给 32 字节无法重现 nonce 派生 → 明确报错而非猜
	if _, err := SignGNB(hex.EncodeToString(make([]byte, 32)), []byte("x")); err == nil {
		t.Fatalf("32 字节私钥应报错")
	}
	if _, err := SignGNB("zznothex", []byte("x")); err == nil {
		t.Fatalf("非法 hex 应报错")
	}
}

func TestSignGNBWithPublicKey(t *testing.T) {
	seed := make([]byte, 32)
	rand.Read(seed)
	kp, _ := GenerateGNBKeyPairFromSeed(seed)
	msg := []byte("with-pub")

	a, err := SignGNB(hex.EncodeToString(kp.PrivateKey), msg)
	if err != nil {
		t.Fatalf("SignGNB: %v", err)
	}
	b, err := SignGNBWithPublicKey(
		hex.EncodeToString(kp.PrivateKey),
		hex.EncodeToString(kp.PublicKey),
		msg,
	)
	if err != nil {
		t.Fatalf("SignGNBWithPublicKey: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("两条路径应产出相同签名")
	}

	// 配对错误的公钥 → 签名来自不同 hram，验证必然失败
	other, _ := GenerateGNBKeyPair()
	c, err := SignGNBWithPublicKey(
		hex.EncodeToString(kp.PrivateKey),
		hex.EncodeToString(other.PublicKey),
		msg,
	)
	if err != nil {
		t.Fatalf("SignGNBWithPublicKey: %v", err)
	}
	if ed25519.Verify(ed25519.PublicKey(kp.PublicKey), msg, c) {
		t.Fatalf("混用公钥产生的签名不应通过")
	}
}

func TestTrimHex(t *testing.T) {
	cases := map[string]string{
		"  abc123  ": "abc123",
		"0xDEAD":     "DEAD",
		"dead\n":     "dead",
		"":           "",
	}
	for in, want := range cases {
		if got := trimHex(in); got != want {
			t.Fatalf("trimHex(%q) = %q, want %q", in, got, want)
		}
	}
}
