package keys

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 锁定 C-2 双端签名向量（R1-4）。PHP 侧 NodeFaceAuthInteropTest 读同一文件。
func TestNodeFaceVectorLocked(t *testing.T) {
	path := filepath.Join("testdata", "node_face_vectors.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skip("vector file not found: " + path)
	}
	var v struct {
		Cases []struct {
			Name        string `json:"name"`
			NodePrivHex string `json:"node_priv_hex"`
			BackPrivHex string `json:"back_priv_hex"`
			Method      string `json:"method"`
			Path        string `json:"path"`
			Ts          string `json:"ts"`
			Body        string `json:"body"`
			BackPubHex  string `json:"back_pub_hex"`
			SharedHex   string `json:"shared_hex"`
			Signature   string `json:"signature"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Cases {
		// 由 back_priv 推 back_pub（X25519）
		back, err := x25519FromPrivateHex(c.BackPrivHex)
		if err != nil {
			t.Fatal(err)
		}
		if c.BackPubHex != "" && c.BackPubHex != hex.EncodeToString(back.Public) {
			t.Fatalf("back_pub mismatch %s vs %s", c.BackPubHex, hex.EncodeToString(back.Public))
		}
		s, err := NewNodeFaceSigner(c.NodePrivHex, hex.EncodeToString(back.Public))
		if err != nil {
			t.Fatal(err)
		}
		if c.SharedHex != "" && hex.EncodeToString(s.Shared()) != c.SharedHex {
			t.Fatalf("shared mismatch for %s", c.Name)
		}
		sig := s.Sign(c.Method, c.Path, c.Ts, c.Body)
		if c.Signature != "" && sig != c.Signature {
			t.Fatalf("sig mismatch for %s\n got %s\nwant %s", c.Name, sig, c.Signature)
		}
	}
}
