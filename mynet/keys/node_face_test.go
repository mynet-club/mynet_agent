package keys

import "testing"

// 对齐 PHP NodeFaceAuthService::sign —— message = METHOD\npath\nts\nsha256(body)
func TestNodeFaceSignFormat(t *testing.T) {
	a, _ := GenerateX25519()
	b, _ := GenerateX25519()
	s, err := NewNodeFaceSigner(hexOf(a.Private), hexOf(b.Public))
	if err != nil {
		t.Fatal(err)
	}
	// 与对端一致
	peer, _ := NewNodeFaceSigner(hexOf(b.Private), hexOf(a.Public))
	if string(s.Shared()) != string(peer.Shared()) {
		t.Fatal("shared mismatch")
	}

	body := `{"ok":1}`
	ts := "1700000000"
	sig := s.Sign("POST", "/api/v1/monitor/heartbeat", ts, body)
	if len(sig) < 40 {
		t.Fatalf("sig=%q", sig)
	}
	if !s.Verify("POST", "/api/v1/monitor/heartbeat", ts, body, sig) {
		t.Fatal("verify self")
	}
	if s.Verify("POST", "/api/v1/monitor/heartbeat", ts, `{"ok":2}`, sig) {
		t.Fatal("tampered body must fail")
	}
	peerSig := peer.Sign("POST", "/api/v1/monitor/heartbeat", ts, body)
	if peerSig != sig {
		t.Fatal("both sides must produce identical signature")
	}
}

func hexOf(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}
