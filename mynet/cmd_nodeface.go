package mynet

// nodeface —— G5 薄桥：OpenWrt Lua 无 X25519 时委托本执行器做 C-2 签名。
//
//	mynet nodeface sign --method POST --path /api/v1/node/heartbeat --ts 1700000000 [--body-stdin]
//
// 输出一行 base64 签名（X-Node-Signature），退出码非 0 表示失败。
// 私钥只在本进程内使用，不进 argv/stdout。

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mynet-club/mynet_agent/mynet/keys"
)

func cmdNodeFace(args []string) int {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	if sub != "sign" {
		fmt.Fprintln(os.Stderr, "用法: mynet nodeface sign --method M --path P --ts T [--body-stdin]")
		return 2
	}
	return nodeFaceSign(args)
}

func nodeFaceSign(args []string) int {
	method, path, ts := "", "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--method":
			if i+1 < len(args) {
				method, i = args[i+1], i+1
			}
		case "--path":
			if i+1 < len(args) {
				path, i = args[i+1], i+1
			}
		case "--ts":
			if i+1 < len(args) {
				ts, i = args[i+1], i+1
			}
		}
	}
	if method == "" || path == "" || ts == "" {
		fmt.Fprintln(os.Stderr, "缺 --method/--path/--ts")
		return 2
	}
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读 body 失败:", err)
		return 1
	}

	keysDir, _, err := configFaceKeysDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "定位 keys 目录失败:", err)
		return 1
	}
	privHex, err := keys.LoadNodePrivateKey(keysDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读节点私钥失败:", err)
		return 1
	}
	meta, err := keys.LoadNodeMeta(keysDir)
	if err != nil || meta == nil || meta.BackPubHex == "" {
		fmt.Fprintln(os.Stderr, "缺 back_pub_hex（先 mynet config keys sync）")
		return 1
	}
	// back_pub 可能是 hex 或 base64；keys.NewNodeFaceSigner 要 64 hex
	backPub := strings.TrimSpace(meta.BackPubHex)
	if _, err := hex.DecodeString(backPub); err != nil {
		fmt.Fprintln(os.Stderr, "back_pub_hex 不是 hex")
		return 1
	}

	s, err := keys.NewNodeFaceSigner(privHex, backPub)
	if err != nil {
		fmt.Fprintln(os.Stderr, "构造 signer 失败:", err)
		return 1
	}
	sig := s.Sign(method, path, ts, string(body))
	fmt.Println(sig)
	return 0
}
