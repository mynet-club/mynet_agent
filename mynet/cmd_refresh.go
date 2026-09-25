package mynet

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mynet-club/mynet_agent-V2/mynet/driver"
	"github.com/mynet-club/mynet_agent-V2/mynet/keys"
	"github.com/mynet-club/mynet_agent-V2/shared"
	"github.com/mynet-club/mynet_agent-V2/shared/domain"
)

// cmdRefresh — R2-3：refresh-{routes,address,config,keys}
//
//	mynet refresh-routes --auto --node-id N
//	mynet refresh-address --auto [--vpn-type wireguard]
//	mynet refresh-config --auto
//	mynet refresh-keys --auto
//
// 输入：$MYNET_HOME/var/bundle.json（BundleEnvelope v2）或 stdin。
func cmdRefresh(args []string) int {
	what := strings.TrimPrefix(args0(args), "refresh-")
	rest := args
	auto := false
	for i := 0; i < len(args); i++ {
		if args[i] == "--auto" {
			auto = true
			rest = append(args[:i], args[i+1:]...)
			break
		}
	}
	_ = auto
	_ = rest

	env, err := loadBundleEnvelope()
	if err != nil {
		fmt.Fprintf(os.Stderr, "refresh-%s: 读 bundle 失败: %v\n", what, err)
		return 1
	}

	switch what {
	case "routes":
		return refreshRoutes(env)
	case "address":
		return refreshAddress(env)
	case "config":
		return refreshConfig(env)
	case "keys":
		return refreshKeys(env)
	default:
		fmt.Fprintf(os.Stderr, "未知 refresh 域: %s\n", what)
		return 2
	}
}

func args0(args []string) string {
	// mynet refresh-routes … → Run 收到的是 ["refresh-routes", …]
	if len(args) > 0 && strings.HasPrefix(args[0], "refresh-") {
		return args[0]
	}
	if v := os.Getenv("MYNET_REFRESH"); v != "" {
		return "refresh-" + v
	}
	return "refresh-routes"
}

func loadBundleEnvelope() (*domain.BundleEnvelope, error) {
	p := filepath.Join(varDir(), "bundle.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return domain.DecodeBundleEnvelope(raw)
}

func varDir() string {
	if v := os.Getenv("MYNET_HOME"); v != "" {
		return v + "/var"
	}
	return sharedVarDir()
}

// gnbConfDir gnb 原生配置目录。
//
// 唯一口径：refresh-config 写它、service start 起它、metrics 读它的 gnb.map，
// 三处必须一致 —— 此前 GNBDriver 的默认值是 driver/gnb/conf/<nodeId>，
// 而 refresh-config 覆盖成 var/gnb，两边对不上。
func gnbConfDir() string {
	return filepath.Join(varDir(), "gnb")
}

// readNodeConf 读 $MYNET_HOME/conf/mynet.conf 的 KEY=VALUE。
//
// cmd/mynetd/nodeface.go 里有一份同形实现，但那是 package main，import 不到。
// 两处都只有十来行、且各自需要的字段不同（那边要 NODE_ID+PRIV+BACK_PUB 三个齐全
// 才能签名，这边只要 PRIV），暂不强行抽公共层。
func readNodeConf() (map[string]string, error) {
	path := filepath.Join(confDir(), "mynet.conf")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读节点配置失败（先运行 mynet setup）: %w", err)
	}
	defer f.Close()

	kv := make(map[string]string)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}

	return kv, nil
}

func sharedVarDir() string {
	return filepath.Dir(shared.DefaultSnapshotPath())
}

func refreshRoutes(env *domain.BundleEnvelope) int {
	// L6：NetworkIntent.Routes → apply 计划
	var plan ApplyPlan
	for _, r := range env.Network.Routes {
		plan.Routes = append(plan.Routes, RouteOp{
			Action:      "replace",
			Destination: r.Prefix,
			Gateway:     r.Via,
			Device:      r.Dev,
			Metric:      r.Metric,
		})
	}
	return printPlan(plan)
}

func refreshAddress(env *domain.BundleEnvelope) int {
	for _, a := range env.Network.Addresses {
		fmt.Println("addr", a)
	}
	return 0
}

func refreshConfig(env *domain.BundleEnvelope) int {
	f := driver.NewFactory()
	switch env.Driver {
	case domain.DriverTypeWireGuard:
		d := driver.NewWireGuardDriver(env.Node.Interface, 0)
		d.ConfPath = filepath.Join(varDir(), "wg.conf")
		if err := d.WriteIntent(env.WireGuard); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("wrote", d.ConfPath)
	case domain.DriverTypeGNB:
		if env.Gnb == nil {
			fmt.Fprintln(os.Stderr, "bundle 里没有 gnb 段")
			return 1
		}
		// ⚠️ NodeID 必须是真值：此前写死 NewGNBDriver(0)，于是 Status() 报的
		// node_id 恒为 0，按 pid/接口名做的任何关联都对不上。
		nodeID, err := strconv.ParseInt(env.Gnb.Identity.NodeID, 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bundle 的 gnb.identity.node_id 非法: %q\n", env.Gnb.Identity.NodeID)
			return 1
		}
		d := driver.NewGNBDriver(nodeID)
		d.ConfDir = gnbConfDir()

		// back **不下发**节点私钥（P0-4 收口，bundle 里 private_key 恒为空），
		// 而 gnb 必须有 security/<uuid>.private 才能起（gnb_keys.c:62-67 读不到就 exit）。
		// 私钥只从本机 conf/mynet.conf 来 —— 这正是「控制面集中、密钥不出节点」。
		if env.Gnb.Identity.PrivateKey == "" {
			kv, cerr := readNodeConf()
			if cerr != nil {
				fmt.Fprintf(os.Stderr, "refresh-config: %v\n", cerr)
				return 1
			}
			priv := kv["NODE_PRIV_HEX"]
			if priv == "" {
				fmt.Fprintln(os.Stderr, "refresh-config: conf/mynet.conf 缺 NODE_PRIV_HEX，gnb 起不来")
				return 1
			}
			// 校验本机私钥与 back 下发的公钥是否一对。不校验的话，
			// 「mynet.conf 是另一个节点的」这种错配会一路走到隧道握手才炸，
			// 症状是「对端 PONG 不上」，极难定位。
			if pub := env.Gnb.Identity.PublicKey; pub != "" {
				match, verr := keys.VerifyGNBKeyPair(priv, pub)
				if verr != nil {
					fmt.Fprintf(os.Stderr, "refresh-config: 校验节点密钥对失败: %v\n", verr)
					return 1
				}
				if !match {
					fmt.Fprintln(os.Stderr, "refresh-config: 本机 NODE_PRIV_HEX 与 bundle 里的 public_key 不是一对"+
						"（mynet.conf 可能属于另一个节点）")
					return 1
				}
			}
			env.Gnb.Identity.PrivateKey = priv
		}

		if err := d.WriteIntent(env.Gnb); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("wrote", d.ConfDir)
	default:
		_ = f
		fmt.Fprintf(os.Stderr, "unknown driver %s\n", env.Driver)
		return 1
	}
	return 0
}

func refreshKeys(env *domain.BundleEnvelope) int {
	// 密钥落盘归 WriteConfig；此处只报告意图中的公钥指纹
	if env.Gnb != nil {
		fmt.Println("gnb identity node", env.Gnb.Identity.NodeID, "alg", env.Gnb.Identity.KeyAlgorithm)
	}
	if env.WireGuard != nil {
		fmt.Println("wireguard listen", env.WireGuard.Interface.ListenPort, "peers", len(env.WireGuard.Peers))
	}
	return 0
}

func printPlan(p ApplyPlan) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(p)
	return 0
}
