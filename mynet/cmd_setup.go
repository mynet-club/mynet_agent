package mynet

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/mynet-club/mynet_agent-V2/mynet/keys"
)

// cmdSetup — R2-1：生成节点密钥 + 记录 back_pub + 写 mynet.conf。
//
// 用法:
//
//	mynet setup --print-keys                   # 只打印密钥（联测/调试）
//	mynet setup --back-pub HEX                 # 写入节点配置（含 back_pub）
//	mynet setup --vpn-type wireguard …         # wg 节点（默认 gnb）
//
// ⚠️ 密钥类型**必须与节点的驱动匹配**，此前这里无条件生成 X25519（CPI-61）：
//   - gnb 节点的身份是 **Ed25519**，落盘 128 hex（64 字节 = clamp(SHA512(seed)[0:32])
//     || SHA512(seed)[32:64]，见 mynet_gnb/src/libs/ed25519/keypair.c:8-12）。
//     gnb 读 security/<uuid>.private 时**不再 clamp**（gnb_keys.c:74），
//     所以写进去的必须已经是这个布局。
//   - wireguard 节点是 Curve25519，64 hex。
//
// 默认 gnb：back 侧判定也是「非 wireguard 即 gnb」
// （BundleEnvelopePublisher：`$isGnb = primaryVpnType() !== VPN_TYPE_WIREGUARD`）。
//
// 节点面签名对两种布局都成立（keys/node_face.go 的 decodeNodeFacePrivate 按
// hex 长度自动判：64→X25519 直接 DH，128→GNB，前 32 字节即 clamp 过的标量），
// 所以 BACK_PUB_HEX 与签名方式不随驱动变。
func cmdSetup(args []string) int {
	printKeys := false
	backPub := ""
	vpnType := "gnb"
	nodeID := os.Getenv("MYNET_NODE_ID")
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--print-keys":
			printKeys = true
		case "--back-pub":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--back-pub 需要 hex")
				return 2
			}
			i++
			backPub = args[i]
		case "--vpn-type":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--vpn-type 需要 gnb|wireguard")
				return 2
			}
			i++
			vpnType = args[i]
		case "--node-id":
			if i+1 >= len(args) {
				return 2
			}
			i++
			nodeID = args[i]
		}
	}

	var algorithm, privHex, pubHex string
	switch vpnType {
	case "gnb":
		// GNB：Ed25519，私钥 128 hex。ToHex() 给的就是 gnb 落盘格式（已 clamp），
		// 见 keys/gnb_ed25519.go 的 ToHex 注释与 gnb_ed25519_clamp_test.go。
		kp, err := keys.GenerateGNBKeyPair()
		if err != nil {
			fmt.Fprintf(os.Stderr, "generate gnb keypair: %v\n", err)
			return 1
		}
		privHex, pubHex = kp.ToHex()
		algorithm = "ed25519"
	case "wireguard", "wg":
		priv, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			fmt.Fprintf(os.Stderr, "generate: %v\n", err)
			return 1
		}
		privHex = hex.EncodeToString(priv.Bytes())
		pubHex = hex.EncodeToString(priv.PublicKey().Bytes())
		algorithm = "x25519"
	default:
		fmt.Fprintf(os.Stderr, "未知 --vpn-type %q（只支持 gnb|wireguard）\n", vpnType)
		return 2
	}

	out := map[string]string{
		"vpn_type":       vpnType,
		"algorithm":      algorithm,
		"private_hex":    privHex,
		"public_hex":     pubHex,
		"back_pub_hex":   backPub,
		"node_id":        nodeID,
		"sign_algorithm": "hmac-sha256-dh", // C-2
	}
	if printKeys {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		// 公钥要上传到 back（PUT /api/v1/nodes/{id}/keys，只传 custom_public_key，
		// **私钥不上传** —— back 侧 validateCustomGnbKey 允许只给公钥）。
		// 不上传的话对端的 ed25519/<uuid>.public 就是空的，隧道建不起来。
		fmt.Fprintf(os.Stderr, "\n下一步：把上面的 public_hex 用**用户 token** 上传到 back\n"+
			"  PUT /api/v1/nodes/%s/keys  {\"custom_public_key\":\"<public_hex>\",\"key_type\":\"%s\"}\n"+
			"  key_type 取值：gnb 节点填 gnbkey，wireguard 节点填 wireguard\n"+
			"  ⚠️ private_hex 不要上传、不要贴进 issue/日志\n", nodeID, gnbKeyType(vpnType))
		return 0
	}

	// 写 $MYNET_HOME/conf/mynet.conf（0600，私钥不进 API）
	confDir := confDir()
	if err := os.MkdirAll(confDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir: %v\n", err)
		return 1
	}
	path := confDir + "/mynet.conf"
	body := fmt.Sprintf("NODE_ID=%s\nNODE_PRIV_HEX=%s\nNODE_PUB_HEX=%s\nBACK_PUB_HEX=%s\n",
		nodeID, privHex, pubHex, backPub)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		return 1
	}
	fmt.Printf("wrote %s (0600, vpn_type=%s algorithm=%s priv=%d hex)\n",
		path, vpnType, algorithm, len(privHex))
	fmt.Printf("公钥（需上传到 back）：%s\n", pubHex)

	return 0
}

// gnbKeyType back 的 putKeys 白名单是 gnbkey,ed25519,wireguard,x25519,curve25519
// （NodeController.php:974）。gnb 节点要填 **gnbkey** 而不是 ed25519 ——
// 两套词表的翻译在 back 侧 NodeKeyDomainService::algorithmForKeyType()（CPI-37）。
func gnbKeyType(vpnType string) string {
	if vpnType == "gnb" {
		return "gnbkey"
	}

	return "wireguard"
}

func confDir() string {
	if v := os.Getenv("MYNET_HOME"); v != "" {
		return v + "/conf"
	}
	return "conf"
}

// cmdStatus / cmdCheck / cmdNodes / cmdPlatform — R2-2 只读面
func cmdStatus(_ []string) int {
	snap, err := readSystemSnapshot()
	if err != nil {
		fmt.Printf("system.json: %v（先 mynet detector）\n", err)
	} else {
		fmt.Printf("os=%s kernel=%s ifaces=%d fp=%s\n",
			snap.OS, snap.Kernel, len(snap.Interfaces), short(snap.Fingerprint))
	}
	m := collectMetricsBrief()
	fmt.Printf("cpu=%.1f%% mem=%dMB load1=%.2f vpn=%d\n",
		m["cpu"], m["mem_used"], m["load1"], m["vpn"])
	return 0
}

func cmdCheck(_ []string) int {
	fail := 0
	if _, err := readSystemSnapshot(); err != nil {
		fmt.Println("✗ system.json 缺失 — mynet detector")
		fail = 1
	} else {
		fmt.Println("✓ system.json")
	}
	if !cmdExists("ip") {
		fmt.Println("✗ ip 不可用")
		fail = 1
	} else {
		fmt.Println("✓ ip")
	}
	if cmdExists("wg") {
		fmt.Println("✓ wg")
	} else {
		fmt.Println("· wg 缺失（wireguard 驱动不可用）")
	}
	if cmdExists("gnb_ctl") {
		fmt.Println("✓ gnb_ctl")
	} else {
		fmt.Println("· gnb_ctl 缺失（gnb 驱动不可用）")
	}
	return fail
}

// cmdNodes 历史上是占位（只打印「本地列表依赖 back relay」）。
// 现在真去 back 取列表 —— 与 `mynet node list` 同一份实现，不留两份。
func cmdNodes(rest []string) int {
	return nodeList()
}

func cmdPlatform(_ []string) int {
	fmt.Printf("%s/%s\n", goos(), goarch())
	return 0
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
