package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readConfKV 读 setup 写出的 mynet.conf。
func readConfKV(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(os.Getenv("MYNET_HOME"), "conf", "mynet.conf"))
	if err != nil {
		t.Fatalf("读 mynet.conf: %v", err)
	}
	kv := map[string]string{}
	for _, ln := range strings.Split(string(raw), "\n") {
		if ln == "" {
			continue
		}
		k, v, ok := strings.Cut(ln, "=")
		if ok {
			kv[k] = v
		}
	}

	return kv
}

// setupInTempHome 在临时 MYNET_HOME 里跑 cmdSetup，返回退出码。
func setupInTempHome(t *testing.T, args ...string) int {
	t.Helper()
	t.Setenv("MYNET_HOME", t.TempDir())

	return cmdSetup(args)
}

// TestSetupGnbWritesEd25519_128Hex 钉住 CPI-61：gnb 节点必须拿到 Ed25519 的
// **128 hex** 私钥，而不是此前无条件生成的 X25519（64 hex）。
//
// 为什么这是硬要求而不只是"更合适"：gnb 读 security/<uuid>.private 时
// 用裸 read() 要求**恰好 128 字节**（gnb_keys.c:68-73，读不满就 exit(0)），
// 且读进来后**不再 clamp**（全仓 clamp 只在 keypair.c 与 key_exchange.c）。
// 喂它一个 64 hex 的 X25519 私钥，症状是 gnb 起来就退、且退出码是 0。
func TestSetupGnbWritesEd25519_128Hex(t *testing.T) {
	if rc := setupInTempHome(t, "--vpn-type", "gnb", "--node-id", "3001",
		"--back-pub", strings.Repeat("ab", 32)); rc != 0 {
		t.Fatalf("cmdSetup 退出码 %d", rc)
	}
	kv := readConfKV(t)

	if len(kv["NODE_PRIV_HEX"]) != 128 {
		t.Fatalf("gnb 节点私钥 hex 长度 %d，必须 128（此前无条件生成 X25519 → 64）",
			len(kv["NODE_PRIV_HEX"]))
	}
	if len(kv["NODE_PUB_HEX"]) != 64 {
		t.Fatalf("gnb 节点公钥 hex 长度 %d，必须 64", len(kv["NODE_PUB_HEX"]))
	}
	if kv["NODE_ID"] != "3001" {
		t.Fatalf("NODE_ID = %q", kv["NODE_ID"])
	}

	// 私钥必须已是 GNB 落盘布局（前 32 字节 clamp 过）——
	// 细粒度的布局断言在 keys/gnb_ed25519_clamp_test.go，这里只做一个粗检，
	// 确保 setup 走的确实是那条路径而不是别的
	for _, c := range kv["NODE_PRIV_HEX"] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("NODE_PRIV_HEX 含非小写 hex 字符 %q", c)
		}
	}
}

// TestSetupDefaultsToGnb 默认必须是 gnb —— back 侧判定也是
// 「非 wireguard 即 gnb」（BundleEnvelopePublisher 的 $isGnb）。
// 默认值搞反的话，绝大多数节点会被发一个用不了的密钥。
func TestSetupDefaultsToGnb(t *testing.T) {
	if rc := setupInTempHome(t, "--node-id", "3002", "--back-pub", strings.Repeat("cd", 32)); rc != 0 {
		t.Fatalf("cmdSetup 退出码 %d", rc)
	}
	if n := len(readConfKV(t)["NODE_PRIV_HEX"]); n != 128 {
		t.Fatalf("不带 --vpn-type 时应默认 gnb（128 hex），实际 %d hex", n)
	}
}

func TestSetupWireguardWritesX25519_64Hex(t *testing.T) {
	if rc := setupInTempHome(t, "--vpn-type", "wireguard", "--node-id", "3003",
		"--back-pub", strings.Repeat("ef", 32)); rc != 0 {
		t.Fatalf("cmdSetup 退出码 %d", rc)
	}
	kv := readConfKV(t)
	if len(kv["NODE_PRIV_HEX"]) != 64 {
		t.Fatalf("wireguard 节点私钥 hex 长度 %d，必须 64（Curve25519）", len(kv["NODE_PRIV_HEX"]))
	}
}

func TestSetupRejectsUnknownVpnType(t *testing.T) {
	if rc := setupInTempHome(t, "--vpn-type", "openvpn"); rc != 2 {
		t.Fatalf("未知 --vpn-type 应退出码 2（用法错），实际 %d", rc)
	}
	// 不该留下半成品配置
	if _, err := os.Stat(filepath.Join(os.Getenv("MYNET_HOME"), "conf", "mynet.conf")); !os.IsNotExist(err) {
		t.Fatal("参数非法时不该写出 mynet.conf")
	}
}

func TestSetupConfIs0600(t *testing.T) {
	if rc := setupInTempHome(t, "--node-id", "3004", "--back-pub", strings.Repeat("11", 32)); rc != 0 {
		t.Fatalf("cmdSetup 退出码 %d", rc)
	}
	st, err := os.Stat(filepath.Join(os.Getenv("MYNET_HOME"), "conf", "mynet.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mynet.conf 含节点私钥，权限必须 0600，实际 %v", st.Mode().Perm())
	}
}

func TestGnbKeyTypeMapping(t *testing.T) {
	// back 的 putKeys 白名单是 gnbkey,ed25519,wireguard,x25519,curve25519
	// （NodeController.php:974）。gnb 节点要填 **gnbkey** 而不是 ed25519 ——
	// 填错会落到 CPI-37 那条「algorithm 标错 → 节点面全 401」的坑里。
	if got := gnbKeyType("gnb"); got != "gnbkey" {
		t.Fatalf("gnb 节点的 key_type 应为 gnbkey，got %q", got)
	}
	if got := gnbKeyType("wireguard"); got != "wireguard" {
		t.Fatalf("wireguard 节点的 key_type 应为 wireguard，got %q", got)
	}
}
