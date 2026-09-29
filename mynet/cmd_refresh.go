package mynet

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mynet-club/mynet_agent-V2/mynet/driver"
	"github.com/mynet-club/mynet_agent-V2/mynet/keys"
	"github.com/mynet-club/mynet_agent-V2/shared"
	"github.com/mynet-club/mynet_agent-V2/shared/config"
	"github.com/mynet-club/mynet_agent-V2/shared/domain"
	"github.com/mynet-club/mynet_agent-V2/shared/utils"
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

// gnbConfDir gnb 原生配置目录 —— **投影目录** driver/gnb/conf/{nodeID}。
//
// 与 writeGnbProjection / ApplyFromActive 同一路径。此前这里是 var/gnb，
// 于是「apply 写了投影、service start 起了旧目录」—— 状态乱、启动失败的根因。
func gnbConfDir() string {
	nodeID := ""
	store := &config.Store{}
	if _, mode, err := store.ActiveSource(); err == nil && mode != nil {
		nodeID = mode.Active.NodeID
	}
	if nodeID == "" {
		// 兜底：扫投影目录唯一子目录
		base := utils.GnbConfDir()
		if ents, err := os.ReadDir(base); err == nil {
			for _, e := range ents {
				if e.IsDir() {
					nodeID = e.Name()
					break
				}
			}
		}
	}
	if nodeID != "" {
		return filepath.Join(utils.GnbConfDir(), nodeID)
	}
	return filepath.Join(utils.GnbConfDir(), "default")
}

// readNodeConf 从配置树 v2 的 keys/ 读身份，拼成 KEY=VALUE 形态
// （调用方仍吃 NODE_PRIV_HEX 等旧键名，避免 refresh 整条链改名）。
//
// 位置：
//
//	local  → conf/local/keys/node.{private,public,meta}
//	online → conf/online/{tenant}/keys/
//
// 不再读 conf/mynet.conf。
func readNodeConf() (map[string]string, error) {
	store := &config.Store{}
	src, mode, err := store.ActiveSource()
	if err != nil {
		return nil, err
	}
	if mode == nil {
		// 未初始化时退回 local
		src = filepath.Join(confDir(), "local")
	}
	priv, pub, algo, nodeID, err := keys.LoadNodeKeys(filepath.Join(src, "keys"))
	if err != nil {
		return nil, fmt.Errorf("读节点密钥失败（先 mynet setup --local|--online）: %w", err)
	}
	kv := map[string]string{
		"NODE_PRIV_HEX": priv,
		"NODE_PUB_HEX":  pub,
		"NODE_ID":       nodeID,
		"VPN_TYPE":      "gnb",
	}
	if algo != "" {
		kv["ALGORITHM"] = algo
	}
	// back_pub 在 meta.json 里
	if meta, err := keys.LoadNodeMeta(filepath.Join(src, "keys")); err == nil && meta.BackPubHex != "" {
		kv["BACK_PUB_HEX"] = meta.BackPubHex
	}
	return kv, nil
}

func sharedVarDir() string {
	return filepath.Dir(shared.DefaultSnapshotPath())
}

func refreshRoutes(env *domain.BundleEnvelope) int {
	return printPlan(planFromBundle(env))
}

// planFromBundle L6：NetworkIntent.Routes → apply 计划。
//
// refresh-routes（打印计划给人看/存盘）与 apply --auto（直接执行）共用这一份，
// 否则两条路径的翻译规则迟早漂移 —— 而漂移的症状是「打印的计划」和
// 「真执行的」不是同一件事，极难发现。
func planFromBundle(env *domain.BundleEnvelope) ApplyPlan {
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
	// Device 是逻辑名（gnb_tun）；darwin 的 gnb 实际拿到 utunN。
	// 把本机 overlay 地址带上，执行期才能反查真实接口（见 resolveDevice）。
	plan.LocalAddresses = append(plan.LocalAddresses, env.Network.Addresses...)

	// 代理分流（主线⑦）：back 解算好的 ProxyPolicy → L6 计划。
	if p := env.Network.Proxy; p != nil && p.Enabled {
		rt := ProxyRuntime{
			TunDevice: env.Node.Interface,
		}
		if ex := firstEnabledExit(p); ex != nil {
			rt.ExitVia = resolveExitVia(env, ex)
		}
		if p.Split != nil && p.Split.IPList != nil {
			// 缓存路径由 source 决定；URL 在执行期 FetchProxyIPList 使用
			src := p.Split.IPList.Source
			if src == "" {
				src = p.Split.ListSource
			}
			rt.IPSetFile = ProxyListCachePath(filepath.Join(varDir(), "proxy-lists"), src)
		}
		plan.Proxy = planProxy(p, rt)
	}

	return plan
}

func firstEnabledExit(p *domain.ProxyPolicy) *domain.ProxyExit {
	for i := range p.Exits {
		if p.Exits[i].Enabled && p.Exits[i].NodeID != "" {
			return &p.Exits[i]
		}
	}
	return nil
}

// resolveExitVia 出口 VIP：优先 ProxyExit.Via，否则查 gnb.nodes 的 tun_address 主机位。
func resolveExitVia(env *domain.BundleEnvelope, ex *domain.ProxyExit) string {
	if ex.Via != "" {
		return ex.Via
	}
	if env.Gnb == nil {
		return ""
	}
	for _, n := range env.Gnb.Nodes {
		if n.NodeID != ex.NodeID || n.TunAddress == "" {
			continue
		}
		if ip, _, err := net.ParseCIDR(n.TunAddress); err == nil {
			return ip.String()
		}
		return n.TunAddress
	}
	return ""
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
		if d.Interface == "" {
			d.Interface = "wg0"
		}
		// 与 WriteIntent / wg-quick 同位（见 wireguard.go）
		d.ConfPath = "/etc/wireguard/" + d.Interface + ".conf"
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
					fmt.Fprintln(os.Stderr, "refresh-config: 本机 NODE_PRIV_HEX 与 bundle 里的 public_key 不是一对。"+
						"两种可能：① mynet.conf 属于另一个节点；② bundle 是**旧修订**（换了公钥但没重新发布）。"+
						"② 就是 claim 撞到过的：GET /nodes/{id}/bundle 返回 config_revisions 里的旧版本。")
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
