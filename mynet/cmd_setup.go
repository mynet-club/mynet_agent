package mynet

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// cmdSetup — R2-1：生成节点密钥 + 记录 back_pub + 写 mynet.conf。
//
// 用法:
//
//	mynet setup --print-keys              # 只打印密钥（联测/调试）
//	mynet setup --back-pub HEX            # 写入节点配置（含 back_pub）
func cmdSetup(args []string) int {
	printKeys := false
	backPub := ""
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
		case "--node-id":
			if i+1 >= len(args) {
				return 2
			}
			i++
			nodeID = args[i]
		}
	}

	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate: %v\n", err)
		return 1
	}
	out := map[string]string{
		"algorithm":      "x25519",
		"private_hex":    hex.EncodeToString(priv.Bytes()),
		"public_hex":     hex.EncodeToString(priv.PublicKey().Bytes()),
		"back_pub_hex":   backPub,
		"node_id":        nodeID,
		"sign_algorithm": "hmac-sha256-dh", // C-2
	}
	if printKeys {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
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
		nodeID, out["private_hex"], out["public_hex"], backPub)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		return 1
	}
	fmt.Printf("wrote %s (0600)\n", path)
	return 0
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

func cmdNodes(_ []string) int {
	fmt.Println("nodes: 本地列表依赖 back relay（平台态）；本机仅显示驱动 peer")
	if ms := probeWireGuard(); len(ms) > 0 {
		for _, x := range ms {
			fmt.Printf("  wireguard up=%v peers=%d\n", x.Up, x.Peers)
		}
	}
	if ms := probeGNB(); len(ms) > 0 {
		for _, x := range ms {
			fmt.Printf("  gnb up=%v peers=%d\n", x.Up, x.Peers)
		}
	}
	return 0
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
