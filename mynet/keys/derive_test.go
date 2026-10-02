package keys

import "testing"

// 配对验证：从私钥推导公钥必须与生成时一致。
func TestDerivePublicKeyFromPrivateHexGnb(t *testing.T) {
	kp, err := GenerateGNBKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	priv, pub := kp.ToHex()
	got, err := DerivePublicKeyFromPrivateHex(priv, "ed25519")
	if err != nil {
		t.Fatal(err)
	}
	if got != pub {
		t.Fatalf("推导不一致: got %s want %s", got, pub)
	}
	// 空算法按长度推断
	if got2, _ := DerivePublicKeyFromPrivateHex(priv, ""); got2 != pub {
		t.Fatalf("空算法推导不一致")
	}
}

func TestDerivePublicKeyFromPrivateHexX25519(t *testing.T) {
	priv, pub, algo, err := GenerateNodeKeys("wireguard")
	if err != nil {
		t.Fatal(err)
	}
	if algo != "x25519" {
		t.Fatalf("algo=%s", algo)
	}
	got, err := DerivePublicKeyFromPrivateHex(priv, "x25519")
	if err != nil {
		t.Fatal(err)
	}
	if got != pub {
		t.Fatalf("x25519 推导不一致: got %s want %s", got, pub)
	}
}

func TestDerivePublicKeyMismatch(t *testing.T) {
	kp, _ := GenerateGNBKeyPair()
	priv, _ := kp.ToHex()
	_, err := DerivePublicKeyFromPrivateHex("deadbeef", "ed25519")
	if err == nil {
		t.Fatal("错误长度应报错")
	}
	// 错误算法
	if _, err := DerivePublicKeyFromPrivateHex(priv, "rsa"); err == nil {
		t.Fatal("未知算法应报错")
	}
}

func TestInferAlgorithmFromPrivateHex(t *testing.T) {
	if got := InferAlgorithmFromPrivateHex("aabb"); got != "" {
		t.Fatalf("短 hex 应返回空: %s", got)
	}
}
