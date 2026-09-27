package mynet

// setup --local / --online —— 配置树 v2。
//
//	mynet setup --local  --node-id ID [--overlay CIDR] [--vpn-type gnb|wireguard]
//	mynet setup --online --tenant T --node-id ID [--vpn-type …] [--api-base URL]
//
// 旧 mynet.conf 单文件写盘已删除：身份进 keys/，意图进 intent.json / profile.json。

import (
	"fmt"
	"os"

	"github.com/mynet-club/mynet_agent-V2/mynet/keys"
	"github.com/mynet-club/mynet_agent-V2/shared/config"
	"github.com/mynet-club/mynet_agent-V2/shared/domain"
	"github.com/mynet-club/mynet_agent-V2/shared/utils"
)

// cmdSetupV2 覆盖原 cmdSetup 的落盘面；密钥生成逻辑与旧版一致。
func cmdSetupV2(args []string) int {
	var (
		printKeys = false
		local     = false
		online    = false
		tenant    = ""
		nodeID    = os.Getenv("MYNET_NODE_ID")
		overlay   = ""
		vpnType   = "gnb"
		backPub   = ""
		apiBase   = ""
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
		case "--api-base":
			v, ok := need()
			if !ok {
				return 2
			}
			apiBase = v
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

	privHex, pubHex, algorithm, err := keys.GenerateNodeKeys(vpnType)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate keys: %v\n", err)
		return 1
	}

	store := &config.Store{}
	var sourceDir string

	if local {
		sourceDir = utils.LocalConfDir()
		li := domain.NewLocalIntent(driverType, nodeID, overlay)
		if err := store.SaveLocalIntent(&li); err != nil {
			fmt.Fprintf(os.Stderr, "write intent: %v\n", err)
			return 1
		}
	} else {
		td, err := utils.OnlineTenantDir(tenant)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tenant: %v\n", err)
			return 2
		}
		sourceDir = td
		prof := &domain.OnlineProfile{
			Schema:   domain.ConfigSchemaVersion,
			Mode:     domain.ModeOnline,
			TenantID: tenant,
			APIBase:  apiBase,
			Driver:   driverType,
			NodeID:   nodeID,
			NodeName: nodeName,
		}
		if err := store.SaveOnlineProfile(prof); err != nil {
			fmt.Fprintf(os.Stderr, "write profile: %v\n", err)
			return 1
		}
	}

	// 密钥：{space}/nodes/{nodeID}/keys（节点级，不挂租户）
	keysDir, kerr := utils.NodeKeysDir(sourceDir, nodeID)
	if kerr != nil {
		fmt.Fprintf(os.Stderr, "keys dir: %v\n", kerr)
		return 1
	}
	if err := keys.WriteNodeKeys(keysDir, privHex, pubHex, algorithm, nodeID, backPub); err != nil {
		fmt.Fprintf(os.Stderr, "write keys: %v\n", err)
		return 1
	}

	// 激活
	var mode domain.ModeFile
	if local {
		mode, err = store.UseLocal()
	} else {
		mode, err = store.UseOnline(tenant, nodeID)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "activate: %v\n", err)
		return 1
	}

	fmt.Printf("wrote %s (mode=%s driver=%s algorithm=%s)\n",
		keysDir, mode.Mode, driverType, algorithm)
	fmt.Printf("公钥（需上传到 back）：%s\n", pubHex)
	if local {
		fmt.Println("下一步：mynet apply --active   # 按 intent 生成 driver 投影")
	} else {
		fmt.Println("下一步：登录/claim 后由 mynetd 拉 bundle；或 mynet apply --active")
	}
	_ = printKeys
	_ = backPub
	return 0
}

const usageSetupV2 = `用法:
  mynet setup --local  --node-id ID [--overlay CIDR] [--vpn-type gnb|wireguard] [--name N]
  mynet setup --online --tenant T --node-id ID [--vpn-type …] [--api-base URL]

密钥写入 conf/local/keys 或 conf/online/<tenant>/keys（0600）。
私钥不上传；public 需 PUT 到 back（key_type: gnbkey|wireguard）。
`
