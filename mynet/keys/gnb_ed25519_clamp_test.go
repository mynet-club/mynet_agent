package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"testing"
)

// TestGNBToHexMatchesKeypairCLayout 钉住 GNB 私钥的**落盘字节布局**。
//
// 权威依据是 GNB 自己的生成代码 mynet_gnb/src/libs/ed25519/keypair.c:8-12：
//
//	sha512(seed, 32, private_key);
//	private_key[0] &= 248;      ← clamp 就地作用在要写盘的缓冲区上
//	private_key[31] &= 63;
//	private_key[31] |= 64;
//
// 即 64 字节 = clamp(SHA512(seed)[0:32]) || SHA512(seed)[32:64]。
// gnb_keys.c:74 读盘后原样使用、**不再 clamp**，所以写盘时必须已经是 clamp 过的。
//
// 此前 ToHex() 编码的是 kp.PrivateKey（未 clamp 的原始 SHA512），注释还写着
// 「as GNB expects」。之所以一直没暴露：ToHex 在生产代码里零调用者，
// 而 Go 侧签名走 scalarFromGNBPrivate() 会显式再 clamp（幂等），对两种输入都自洽。
func TestGNBToHexMatchesKeypairCLayout(t *testing.T) {
	for i := 0; i < 16; i++ {
		seed := make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
		kp, err := GenerateGNBKeyPairFromSeed(seed)
		if err != nil {
			t.Fatal(err)
		}
		privHex, pubHex := kp.ToHex()

		if len(privHex) != 128 {
			t.Fatalf("第 %d 轮：私钥 hex 长度 %d，GNB 要求恰好 128（gnb_keys.c:68-73 裸 read() 读不满就 exit）", i, len(privHex))
		}
		if len(pubHex) != 64 {
			t.Fatalf("第 %d 轮：公钥 hex 长度 %d，应为 64", i, len(pubHex))
		}

		raw, err := hex.DecodeString(privHex)
		if err != nil {
			t.Fatal(err)
		}

		// ① 前 32 字节必须已 clamp（keypair.c:10-12 的三个位运算）
		if raw[0]&7 != 0 {
			t.Fatalf("第 %d 轮：byte[0] 低 3 位未清（&= 248 没做），byte[0]&7=%d", i, raw[0]&7)
		}
		if raw[31]&0xC0 != 0x40 {
			t.Fatalf("第 %d 轮：byte[31] 高 2 位不是 0b01（&= 63 / |= 64 没做），got %#x", i, raw[31]&0xC0)
		}

		// ② 完整对照：把 keypair.c 那四行在 Go 里独立复算一遍
		h := sha512.Sum512(seed)
		want := make([]byte, 64)
		copy(want, h[:])
		want[0] &= 248
		want[31] &= 63
		want[31] |= 64
		if hex.EncodeToString(want) != privHex {
			t.Fatalf("第 %d 轮：与 keypair.c 的布局不一致\n got=%s\nwant=%s", i, privHex, hex.EncodeToString(want))
		}

		// ③ 后 32 字节必须是 SHA512(seed) 的后半（nonce 派生源），
		//    不能是 SHA512(clamped_scalar) 的后半 —— 被删掉的 ToGNBFormat 就错在这
		if hex.EncodeToString(h[32:]) != privHex[64:] {
			t.Fatalf("第 %d 轮：后 32 字节不是 SHA512(seed)[32:64]", i)
		}
	}
}

// TestGNBToHexPrivateKeySignsLikeStdlib 交叉验证：用 ToHex 的产物去签名，
// 必须与 Go stdlib 从同一 seed 得到的签名逐字节一致。
// 这条能同时抓住「标量错」与「nonce 半错」两类问题。
func TestGNBToHexPrivateKeySignsLikeStdlib(t *testing.T) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	kp, err := GenerateGNBKeyPairFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	privHex, pubHex := kp.ToHex()
	msg := []byte("clamp-layout-probe")

	got, err := SignGNB(privHex, msg)
	if err != nil {
		t.Fatal(err)
	}
	want := ed25519.Sign(ed25519.NewKeyFromSeed(seed), msg)
	if string(got) != string(want) {
		t.Fatalf("用 ToHex 产物签出的结果与 stdlib 不一致\n got=%x\nwant=%x", got, want)
	}
	// 公钥也要与 stdlib 一致（证明标量对）
	stdPub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if pubHex != hex.EncodeToString(stdPub) {
		t.Fatalf("公钥与 stdlib 不一致\n got=%s\nwant=%s", pubHex, hex.EncodeToString(stdPub))
	}
	// 且能被纯 stdlib 验签（gateway 侧就是只做验签）
	if !ed25519.Verify(stdPub, msg, got) {
		t.Fatal("stdlib 验签失败")
	}
}
