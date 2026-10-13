package mynet

// setup --local / --online —— 配置树 v2。
//
//	mynet setup --local  --node-id ID [--overlay CIDR] [--vpn-type gnb|wireguard]
//	mynet setup --online --tenant T --node-id ID [--vpn-type …]
//
// 旧 mynet.conf 单文件写盘已删除：身份进 keys/，意图进 intent.json / profile.json。
//
// ⚠️ setup **不生成密钥**（用户旅程：有私钥则导入，没有才显式生成）。
//   导入：mynet config keys set --private … --public … --node-id …
//   新建：mynet config keys generate --vpn gnb --node-id …
// setup 只负责 profile/intent + 激活；密钥是独立一步，禁止顺手 new。

import (
	"fmt"
	"os"

	"github.com/mynet-club/mynet_agent/mynet/keys"
	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// cmdSetupV2 覆盖原 cmdSetup 的落盘面；密钥生成逻辑与旧版一致。
func cmdSetupV2(args []string) int {
	// C3：setup 写 keys/mode.json，必须显式 MYNET_HOME
	if _, code := requireHome(); code != 0 {
		return code
	}
	var (
		printKeys = false
		local     = false
		online    = false
		tenant    = ""
		nodeID    = os.Getenv("MYNET_NODE_ID")
		overlay   = ""
		vpnType   = "gnb"
		backPub   = ""
		nodeName  = ""
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		need := func() (string, bool) {
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "%s 需要参数\n", a)
				return "", false
			}
			i++
			return args[i], true
		}
		switch a {
		case "--print-keys":
			printKeys = true
		case "--local":
			local = true
		case "--online":
			online = true
		case "--tenant":
			v, ok := need()
			if !ok {
				return 2
			}
			tenant = v
		case "--node-id":
			v, ok := need()
			if !ok {
				return 2
			}
			nodeID = v
		case "--overlay":
			v, ok := need()
			if !ok {
				return 2
			}
			overlay = v
		case "--vpn-type":
			v, ok := need()
			if !ok {
				return 2
			}
			vpnType = v
		case "--back-pub":
			v, ok := need()
			if !ok {
				return 2
			}
			backPub = v
		case "--api-base": // 已取消（2026-10-12）：后端地址不入参，指因报错
			// 2026-10-12 全面取消：后端地址不下发、不入参、不落配置。
			fmt.Fprintln(os.Stderr, "--api-base 已取消：云通信统一经 gateway 中继 —— 请去掉该参数")
			return 2
		case "--name":
			v, ok := need()
			if !ok {
				return 2
			}
			nodeName = v
		case "--help", "-h":
			fmt.Fprint(os.Stderr, usageSetupV2)
			return 0
		default:
			fmt.Fprintf(os.Stderr, "setup 未知参数 %q\n%s", a, usageSetupV2)
			return 2
		}
	}

	if local == online {
		fmt.Fprintln(os.Stderr, "setup 必须二选一：--local 或 --online --tenant …")
		return 2
	}
	if online && tenant == "" {
		fmt.Fprintln(os.Stderr, "--online 需要 --tenant")
		return 2
	}
	if nodeID == "" {
		fmt.Fprintln(os.Stderr, "需要 --node-id（或环境 MYNET_NODE_ID）")
		return 2
	}

	driverType := domain.DriverTypeGNB
	if vpnType == "wireguard" || vpnType == "wg" {
		driverType = domain.DriverTypeWireGuard
	}

	// 不生成密钥。profile/intent 与 keys 分离：
	// 有私钥 → config keys set 导入；没有 → config keys generate 显式新建。
	store := &config.Store{}

	if local {
		li := domain.NewLocalIntent(driverType, nodeID, overlay)
		if err := store.SaveLocalIntent(&li); err != nil {
			fmt.Fprintf(os.Stderr, "write intent: %v\n", err)
			return 1
		}
	} else {
		if _, err := utils.OnlineTenantDir(tenant); err != nil {
			fmt.Fprintf(os.Stderr, "tenant: %v\n", err)
			return 2
		}
		prof := &domain.OnlineProfile{
			Schema:   domain.ConfigSchemaVersion,
			Mode:     domain.ModeOnline,
			TenantID: tenant,
			Driver:   driverType,
			NodeID:   nodeID,
			NodeName: nodeName,
		}
		if err := store.SaveOnlineProfile(prof); err != nil {
			fmt.Fprintf(os.Stderr, "write profile: %v\n", err)
			return 1
		}
	}

	// 激活
	var mode domain.ModeFile
	var err error
	if local {
		mode, err = store.UseLocal()
	} else {
		mode, err = store.UseOnline(tenant, nodeID)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "activate: %v\n", err)
		return 1
	}
	// D4-E：nodeID 全局唯一、写进 mode.json 的 active.node_id（tenant 维度也收在这里）。
	// UseLocal 不带 node（历史形状）—— 这里补写，否则 ActiveNodeInfo()/gnbConfDir() 只能
	// 退化到 default 兜底，投影与密钥会落错目录。
	if mode.Active.NodeID != nodeID {
		mode.Active.NodeID = nodeID
		if err := store.SaveMode(&mode); err != nil {
			fmt.Fprintf(os.Stderr, "mode.json 回写 node_id: %v\n", err)
			return 1
		}
	}

	// 密钥目录位置（不写入）—— D4-E：节点唯一存放位 conf/<form>/<nodeId>/
	// （form 跟 setup 的 --local/--online，不按 keys/ 旧树报）
	form := utils.NodeFormLocal
	if online {
		form = utils.NodeFormOnline
	}
	keysDir, kerr := utils.NodeRuntimeDir(form, nodeID)
	if kerr != nil {
		fmt.Fprintf(os.Stderr, "keys dir: %v\n", kerr)
		return 1
	}
	hasKeys := false
	if _, _, _, _, err := keys.LoadNodeKeys(keysDir); err == nil {
		hasKeys = true
	}

	fmt.Printf("✓ profile/intent 已就绪（mode=%s driver=%s node=%s）\n",
		mode.Mode, driverType, nodeID)
	fmt.Printf("  keys_dir: %s\n", keysDir)
	if hasKeys {
		fmt.Println("  密钥: 已存在（未改动）")
	} else {
		fmt.Println("  密钥: 未设置 —— setup 不生成密钥，请二选一：")
		fmt.Println("    导入: mynet config keys set --private <HEX> --public <HEX> --node-id", nodeID, "--algorithm ed25519")
		fmt.Println("    新建: mynet config keys generate --vpn", vpnType, "--node-id", nodeID)
		fmt.Println("  之后: mynet config keys sync   # 只上传 public")
	}
	if local {
		fmt.Println("下一步：mynet apply --active   # 按 intent 生成 driver 投影")
	} else {
		fmt.Println("下一步：mynet config pull     # 或 login 后由 mynetd 拉 bundle")
	}
	_ = printKeys
	// 后端节点面公钥是服务器侧公开参数（非秘密），不违反「setup 不生成密钥」：
	// node claim 取到后经此回填 meta.json，mynetd 节点面才装配得了。
	if backPub != "" {
		if err := keys.UpdateNodeMetaBackPub(keysDir, nodeID, backPub); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ back_pub_hex 回填失败（不影响 setup 结果）: %v\n", err)
		} else {
			fmt.Println("  后端节点面公钥已回填: keys/meta.json")
		}
	}
	return 0
}

const usageSetupV2 = `用法:
  mynet setup --local  --node-id ID [--overlay CIDR] [--vpn-type gnb|wireguard] [--name N]
  mynet setup --online --tenant T --node-id ID [--vpn-type …]

setup 只写 profile/intent 并激活，**不生成密钥**：
  导入私钥: mynet config keys set --private … --public … --node-id …
  新建密钥: mynet config keys generate --vpn gnb --node-id …
  上传公钥: mynet config keys sync
`
