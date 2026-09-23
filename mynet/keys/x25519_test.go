package keys

import (
	"bytes"
	"testing"
)

func TestGenerateX25519RoundTrip(t *testing.T) {
	k, err := GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	if len(k.Private) != 32 || len(k.Public) != 32 {
		t.Fatalf("sizes %d %d", len(k.Private), len(k.Public))
	}
	ph, pb := k.ToHex()
	if len(ph) != 64 || len(pb) != 64 {
		t.Fatalf("hex lens %d %d", len(ph), len(pb))
	}
	pb64, qb64 := k.ToBase64()
	if len(pb64) != 44 || len(qb64) != 44 {
		t.Fatalf("wg base64 must be 44 chars, got %d %d", len(pb64), len(qb64))
	}
	// hex → base64 与直接 base64 一致
	got, err := PrivateBase64FromHex(ph)
	if err != nil || got != pb64 {
		t.Fatalf("PrivateBase64FromHex %q vs %q err=%v", got, pb64, err)
	}
	// base64 → hex 与直接 hex 一致
	back, err := PublicHexFromBase64(qb64)
	if err != nil || back != pb {
		t.Fatalf("PublicHexFromBase64 %q vs %q", back, pb)
	}
}

func TestDHAgrees(t *testing.T) {
	a, _ := GenerateX25519()
	b, _ := GenerateX25519()
	s1, err := DH(a.Private, b.Public)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := DH(b.Private, a.Public)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s1, s2) {
		t.Fatal("DH shared secret mismatch")
	}
}

func TestRejectsWrongLength(t *testing.T) {
	if _, err := PrivateBase64FromHex("aabb"); err == nil {
		t.Fatal("short key must fail")
	}
	if _, err := DH(make([]byte, 16), make([]byte, 32)); err == nil {
		t.Fatal("short DH key must fail")
	}
}
