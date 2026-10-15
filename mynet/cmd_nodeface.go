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
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent/mynet/keys"
	"github.com/mynet-club/mynet_agent/shared/utils"
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

	// 优先 Active(mode) 的 keys；无 mode/无 node-id 时扫 nodes/*/keys（Mac/首次 setup 实测）
	nodeIDHint := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--node-id" && i+1 < len(args) {
			nodeIDHint, i = args[i+1], i+1
		}
	}
	// 门闩认「能不能读出私钥」，不认文件名 node.private ——
	// D4-E 起 gnb 密钥在 security/<id>.private，只 stat 旧文件名会把
	// 已迁移节点误判成「无密钥」（2026-10-15 实测：CLI 报定位失败，
	// mynetd 用 LoadNodePrivateKey 却正常）。
	keysDir, _, err := configFaceKeysDir()
	if err != nil || keysDir == "" || !keysDirHasPrivate(keysDir) {
		keysDir = findKeysDirWithPrivate(nodeIDHint)
	}
	if keysDir == "" {
		fmt.Fprintln(os.Stderr, "定位 keys 目录失败（无节点私钥：security/*.private 或 node.private）")
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

// keysDirHasPrivate 该目录下能不能读出节点私钥（三套形态都认）。
// gnb: security/<id>.private · wg: wg.conf PrivateKey · 旧布局: node.private。
func keysDirHasPrivate(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := keys.LoadNodePrivateKey(dir)
	return err == nil
}

// findKeysDirWithPrivate 扫可读出节点私钥的 keys 目录。
// 新布局 conf/<form>/<nodeId>/（security/ 或 wg.conf）与旧布局
// {space}/nodes/{id}/keys/node.private 都认。
func findKeysDirWithPrivate(nodeIDHint string) string {
	home := utils.GetMyNetHome()
	// 1) 新布局：conf/<form>/<nodeId>/（D4-E 唯一存放位）
	for _, form := range []string{"local", "online"} {
		base := filepath.Join(home, "conf", form)
		if nodeIDHint != "" {
			if p := filepath.Join(base, nodeIDHint); keysDirHasPrivate(p) {
				return p
			}
		}
		ents, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			// online/ 下还有一层 tenant 目录，nodeId 直接挂在 form/ 下
			p := filepath.Join(base, e.Name())
			if keysDirHasPrivate(p) {
				return p
			}
		}
	}
	// 2) 旧布局：{space}/nodes/{id}/keys/node.private
	spaces := []string{
		filepath.Join(home, "conf", "local"),
		filepath.Join(home, "conf", "online"),
	}
	for _, sp := range spaces {
		if nodeIDHint != "" {
			p := filepath.Join(sp, "nodes", nodeIDHint, "keys")
			if keysDirHasPrivate(p) {
				return p
			}
		}
		ents, err := os.ReadDir(filepath.Join(sp, "nodes"))
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(sp, "nodes", e.Name(), "keys")
			if keysDirHasPrivate(p) {
				return p
			}
		}
	}
	return ""
}
