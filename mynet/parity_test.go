package mynet

import "testing"

// TestCheckGBParityZipMismatch —— zip 不一致必须标为 critical 不匹配。
// 实测：缺 zip auto 时大包/TCP 静默丢（2026-09-27）。
func TestCheckGBParityZipMismatch(t *testing.T) {
	local := map[string]string{"zip": "off", "crypto": "xor", "safe-index": "on"}
	remote := map[string]any{"zip": "auto", "safe_index": true}
	items := CheckGNBParity(local, remote)
	var zip, crypto *ParityItem
	for i := range items {
		switch items[i].Key {
		case "zip":
			zip = &items[i]
		case "crypto":
			crypto = &items[i]
		}
	}
	if zip == nil || zip.Match {
		t.Fatalf("zip 应判不匹配: %+v", zip)
	}
	if !zip.Critical {
		t.Fatalf("zip 必须是 critical")
	}
	if crypto == nil || !crypto.Match {
		t.Fatalf("crypto xor 应匹配（远程缺省也是 xor）: %+v", crypto)
	}
}

// TestCheckGBParityFromFallback 云端没有 zip 时用工作基线（auto）。
func TestCheckGBParityFromFallback(t *testing.T) {
	local := map[string]string{"zip": "auto", "zip-level": "-1"}
	items := CheckGNBParity(local, map[string]any{"mtu": 1450})
	for _, it := range items {
		if it.Key == "zip" && !it.Match {
			t.Fatalf("zip auto 应匹配基线: %+v", it)
		}
		if it.Key == "zip" && it.Source != "expected" {
			t.Fatalf("无云端 zip 时 source 应为 expected: %+v", it)
		}
	}
}

// TestParseNodeConfKV 解析 node.conf 键值。
func TestParseNodeConfKV(t *testing.T) {
	kv := ParseNodeConfKV("nodeid 1\nzip auto\n# comment\nMTU 1450\n")
	if kv["zip"] != "auto" {
		t.Fatalf("zip=%q", kv["zip"])
	}
	if kv["mtu"] != "1450" {
		t.Fatalf("mtu 应 tolower: %q", kv["mtu"])
	}
	if _, ok := kv["comment"]; ok {
		t.Fatal("注释不该进 map")
	}
}
