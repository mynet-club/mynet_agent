package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fixtureDir 解析共享 fixtures 目录（ctl SSOT）。
// 覆盖顺序：环境变量 → 从本包向上找 mynet_ctl。
func fixtureDir(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("MYNET_BUNDLE_FIXTURES"); p != "" {
		return p
	}
	candidates := []string{
		filepath.Join("..", "..", "..", "mynet_ctl", "docs", "knowledge-base", "spec", "fixtures", "bundle-envelope-v2"),
		filepath.Join("..", "..", "..", "..", "mynet_ctl", "docs", "knowledge-base", "spec", "fixtures", "bundle-envelope-v2"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	t.Fatalf("找不到 bundle-envelope-v2 fixtures；请设 MYNET_BUNDLE_FIXTURES 或从 mynet_agent 仓库运行")
	return ""
}

func loadFixtures(t *testing.T) map[string][]byte {
	t.Helper()
	dir := fixtureDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 fixtures 目录: %v", err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("读 %s: %v", e.Name(), err)
		}
		out[e.Name()] = raw
	}
	if len(out) < 6 {
		t.Fatalf("fixtures 至少 6 个场景，实际 %d", len(out))
	}
	return out
}

func TestBundleEnvelopeFixturesDecode(t *testing.T) {
	for name, raw := range loadFixtures(t) {
		raw := raw
		t.Run(name, func(t *testing.T) {
			if err := VerifyBundleChecksum(raw); err != nil {
				t.Fatalf("checksum: %v", err)
			}
			env, err := DecodeBundleEnvelope(raw)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if env.SchemaVersion != 2 {
				t.Errorf("schema_version = %d, 期望 2", env.SchemaVersion)
			}
			if env.Generation == "" || env.Node.ID == "" || env.Driver == "" {
				t.Errorf("缺少必填外壳字段: %+v", env)
			}

			var generic map[string]any
			if err := json.Unmarshal(raw, &generic); err != nil {
				t.Fatalf("generic unmarshal: %v", err)
			}

			switch env.Driver {
			case DriverTypeGNB:
				if env.Gnb == nil {
					t.Fatal("driver=gnb 时 gnb 必须有值")
				}
				if env.WireGuard != nil {
					t.Fatal("driver=gnb 时 wireguard 必须为 null")
				}
				if env.Gnb.Identity.KeyAlgorithm != "ed25519" {
					t.Errorf("gnb key_algorithm = %q", env.Gnb.Identity.KeyAlgorithm)
				}
				if env.Gnb.Identity.PublicKey == "" || env.Gnb.Identity.PrivateKey == "" {
					t.Error("gnb identity 密钥缺失")
				}
				if rawWG, ok := generic["wireguard"]; ok && rawWG != nil {
					t.Errorf("原始 JSON 的 wireguard 应为 null，实际 %v", rawWG)
				}
			case "wireguard":
				if env.WireGuard == nil {
					t.Fatal("driver=wireguard 时 wireguard 必须有值")
				}
				if env.Gnb != nil {
					t.Fatal("driver=wireguard 时 gnb 必须为 null")
				}
				if env.WireGuard.Interface.KeyAlgorithm != "curve25519" {
					t.Errorf("wg key_algorithm = %q", env.WireGuard.Interface.KeyAlgorithm)
				}
				if len(env.WireGuard.Peers) == 0 {
					t.Error("wg peers 不应为空（fixtures 约定）")
				}
				for i, p := range env.WireGuard.Peers {
					if p.PublicKey == "" {
						t.Errorf("peers[%d].public_key 为空", i)
					}
					if len(p.AllowedIPs) == 0 {
						t.Errorf("peers[%d].allowed_ips 为空", i)
					}
				}
			default:
				t.Fatalf("未知 driver %q", env.Driver)
			}
		})
	}
}

func TestBundleEnvelopeFixturesChecksumRoundTrip(t *testing.T) {
	for name, raw := range loadFixtures(t) {
		raw := raw
		t.Run(name, func(t *testing.T) {
			got, err := ComputeBundleChecksum(raw)
			if err != nil {
				t.Fatalf("compute: %v", err)
			}
			var generic map[string]any
			if err := json.Unmarshal(raw, &generic); err != nil {
				t.Fatal(err)
			}
			want, _ := generic["checksum"].(string)
			if got != want {
				t.Fatalf("checksum 不一致\n want %s\n got  %s", want, got)
			}
		})
	}
}

func TestBundleEnvelopeCompatExtraFieldsIgnored(t *testing.T) {
	raw := loadFixtures(t)["compat-extra-fields.json"]
	env, err := DecodeBundleEnvelope(raw)
	if err != nil {
		t.Fatalf("未知字段不应导致解码失败: %v", err)
	}
	if env.Gnb == nil || env.Gnb.Identity.NodeID != "1001" {
		t.Fatalf("已知字段应正常解出: %+v", env.Gnb)
	}
	// 重新编码不应把未知字段带回来（typed 视图）
	re, err := EncodeBundleEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(re, &generic); err != nil {
		t.Fatal(err)
	}
	if _, ok := generic["future_envelope_field"]; ok {
		t.Error("typed 重编码不应保留 future_envelope_field")
	}
}

func TestBundleEnvelopeDriverMismatchShape(t *testing.T) {
	// gnb 驱动不得携带 wireguard intent
	raw := loadFixtures(t)["gnb-minimal.json"]
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	gnb := generic["gnb"].(map[string]any)
	wg := map[string]any{
		"interface": map[string]any{
			"private_key":   "dMOaQsOSHnfVak9JG5EyGEkGUaVwKIN3m/jq7xyNqD4=",
			"public_key":    "dMOaQsOSHnfVak9JG5EyGEkGUaVwKIN3m/jq7xyNqD4=",
			"key_algorithm": "curve25519",
		},
		"peers": []any{},
	}
	generic["wireguard"] = wg
	mutated, err := json.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	env, err := DecodeBundleEnvelope(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if env.WireGuard == nil {
		t.Fatal("解码层不拦截 driver 错配（由 Validate/R4 负责）；此处只锁字段能被读出")
	}
	_ = gnb
}

func TestComputeBundleChecksumMissingChecksumOK(t *testing.T) {
	raw := loadFixtures(t)["gnb-minimal.json"]
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	want := generic["checksum"].(string)
	delete(generic, "checksum")
	stripped, err := json.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ComputeBundleChecksum(stripped)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("去掉 checksum 字段后重算应一致\n want %s\n got  %s", want, got)
	}
}
