package keys

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"filippo.io/edwards25519"
)

// 节点面 DH 的**跨端等价性**：后端（PHP）对 algorithm=ed25519 会先做
// edwards→montgomery 再 scalarmult，而 agent（Go）直接拿 GNB 私钥的前 32 字节做 X25519。
// 两条路必须算出同一个 shared，否则症状是「节点面全 401」而不是「密钥不对」。
//
// 这条等价性成立的充要条件是：
//
//	edwardsToMontgomery(A) == X25519_base(s)
//
// 其中 A 是 ed25519 公钥、s 是 GNB 私钥前 32 字节（clamp 过的标量）。
// 只要这条成立，back_priv·(s·G_mont) == s·(back_priv·G_mont) 就是 DH 的交换律，两端必然一致。
func TestGNBPrivateKeyScalarMatchesEdwardsToMontgomeryOfPublicKey(t *testing.T) {
	kp, err := GenerateGNBKeyPair()
	if err != nil {
		t.Fatalf("GenerateGNBKeyPair: %v", err)
	}
	privHex, pubHex := kp.ToHex()

	if len(privHex) != 128 {
		t.Fatalf("GNB 私钥应是 128 hex（clamp(SHA512(seed)) 布局），实际 %d", len(privHex))
	}
	if len(pubHex) != 64 {
		t.Fatalf("GNB 公钥应是 64 hex，实际 %d", len(pubHex))
	}

	// 先确认这对密钥本身是自洽的（否则后面的等价性证明没有意义）
	if ok, err := VerifyGNBKeyPair(privHex, pubHex); !ok || err != nil {
		t.Fatalf("VerifyGNBKeyPair = %v, %v", ok, err)
	}

	// 左：edwards→montgomery（后端侧 edwardsToMontgomeryBin 做的事）
	pubBytes, err := hex.DecodeString(pubHex)
	if err != nil {
		t.Fatal(err)
	}
	edPt, err := (&edwards25519.Point{}).SetBytes(pubBytes)
	if err != nil {
		t.Fatalf("公钥不是合法的 edwards 点: %v", err)
	}
	montFromPub := edPt.BytesMontgomery()

	// 右：X25519_base(s)，s = GNB 私钥前 32 字节
	privBytes, err := hex.DecodeString(privHex)
	if err != nil {
		t.Fatal(err)
	}
	scalar := privBytes[:32]
	montFromPriv, err := ecdh.X25519().NewPrivateKey(scalar)
	if err != nil {
		t.Fatalf("前 32 字节不能当 X25519 私钥: %v", err)
	}
	base := montFromPriv.PublicKey().Bytes()

	if !bytes.Equal(montFromPub, base) {
		t.Errorf("edwards→montgomery(公钥) != X25519_base(私钥前32字节)\n  左 %x\n  右 %x\n"+
			"这意味着两端会算出不同的 shared → 节点面全 401", montFromPub, base)
	}
}

// 128-hex 的 GNB 私钥必须能被节点面签名器接受（此前只收 32B，gnb 节点一律起不来）。
func TestNewNodeFaceSignerAcceptsGNBLayout(t *testing.T) {
	kp, err := GenerateGNBKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex, _ := kp.ToHex()

	backPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	backPubHex := hex.EncodeToString(backPriv.PublicKey().Bytes())

	s, err := NewNodeFaceSigner(privHex, backPubHex)
	if err != nil {
		t.Fatalf("128-hex 的 GNB 私钥被拒了: %v", err)
	}

	// 后端侧会算的那一份：scalarmult(back_priv, edwards→montgomery(node_pub))
	_, nodePubHex := kp.ToHex()
	nodePub, _ := hex.DecodeString(nodePubHex)
	edPt, err := (&edwards25519.Point{}).SetBytes(nodePub)
	if err != nil {
		t.Fatal(err)
	}
	peerMont := edPt.BytesMontgomery()
	peerKey, err := ecdh.X25519().NewPublicKey(peerMont)
	if err != nil {
		t.Fatalf("edwards→montgomery 的结果不是合法 X25519 公钥: %v", err)
	}
	want, err := backPriv.ECDH(peerKey)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(s.Shared(), want) {
		t.Errorf("两端 shared 不一致 —— 节点面必然 401\n  agent: %x\n  后端 : %x", s.Shared(), want)
	}

	// 签名/验签也要能用同一份 shared 走通
	sig := s.Sign("POST", "/api/v1/monitor/heartbeat", "1700000000", `{"ok":1}`)
	if sig == "" {
		t.Error("签名为空")
	}
}

// 32B 原生 X25519（wireguard 节点）仍要照常工作 —— 别为了兼容 GNB 把这条弄坏。
func TestNewNodeFaceSignerStillAcceptsX25519(t *testing.T) {
	nodeKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	backKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	s, err := NewNodeFaceSigner(hex.EncodeToString(nodeKey.Bytes()), hex.EncodeToString(backKey.PublicKey().Bytes()))
	if err != nil {
		t.Fatalf("32B X25519 私钥被拒: %v", err)
	}
	want, err := nodeKey.ECDH(backKey.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s.Shared(), want) {
		t.Error("x25519 路径的 shared 算错了")
	}
}

// 非法长度必须报错，而且要说清接受哪两种 —— 否则排查时看不出该给什么。
func TestNewNodeFaceSignerRejectsBadLengths(t *testing.T) {
	backPub := hex.EncodeToString(make([]byte, 32))
	for _, bad := range []string{"", "abcd", hex.EncodeToString(make([]byte, 31)), hex.EncodeToString(make([]byte, 33)), hex.EncodeToString(make([]byte, 48))} {
		if _, err := NewNodeFaceSigner(bad, backPub); err == nil {
			t.Errorf("长度 %d 的私钥竟被接受", len(bad)/2)
		}
	}
	_, err := NewNodeFaceSigner(hex.EncodeToString(make([]byte, 48)), backPub)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("32B")) {
		t.Errorf("错误信息应说明接受 32B/64B，实际: %v", err)
	}
}
