package mynet

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mynet-club/mynet_agent/mynet/driver"
	"github.com/mynet-club/mynet_agent/mynet/keys"
	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// cmdRefresh — R2-3：refresh-{routes,address,config,keys}
//
//	mynet refresh-routes --auto --node-id N
//	mynet refresh-address --auto [--vpn-type wireguard]
//	mynet refresh-config --auto
//	mynet refresh-keys --auto
//
// 输入：$MYNET_HOME/var/bundle.json（BundleEnvelope v2）或 stdin。
//
// ⚠️ 语义（U2）：refresh-* **只把本地 bundle 渲染成驱动配置**，不访问网络。
// 要拉新配置请 `mynet config pull`（下载 + 投影）或 `mynet config sync`（拉新 + 投影 + OS 路由）。
// 「改了 back 但本地还是旧的」九成是忘了 pull，不是 refresh 坏了。
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

// bundledProxyListPath 随包 IP 列表路径（旧 Lua `proxy_sources/` 的延续）。
// region+policy 经 domain.ResolveProxySplit 解出列表源（international/china），映射到
// 随包文件名；取不到 home 或文件不存在都返回 ""（不猜、不造）。
func bundledProxyListPath(p *domain.ProxyPolicy) string {
	src, _, err := domain.ResolveProxySplit(p.Region, p.Policy)
	if err != nil {
		return ""
	}
	var name string
	switch src {
	case domain.ProxyListInternational:
		name = "interip.txt"
	case domain.ProxyListChina:
		name = "chinaip.txt"
	}
	home := os.Getenv("MYNET_HOME")
	if name == "" || home == "" {
		return ""
	}
	f := filepath.Join(home, "conf", "proxy", "proxy_sources", name)
	if _, err := os.Stat(f); err != nil {
		return ""
	}
	return f
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
	// 密钥挂节点：conf/online/{tenant}/nodes/{id}/keys（ResolveKeysDir 兼容旧 {space}/keys）
	nodeIDHint := ""
	if mode != nil {
		nodeIDHint = mode.Active.NodeID
	}
	priv, pub, algo, nodeID, err := keys.LoadNodeKeys(utils.ResolveKeysDir(src, nodeIDHint))
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
	if meta, err := keys.LoadNodeMeta(utils.ResolveKeysDir(src, nodeIDHint)); err == nil && meta.BackPubHex != "" {
		kv["BACK_PUB_HEX"] = meta.BackPubHex
	}
	return kv, nil
}

func sharedVarDir() string {
	return filepath.Dir(shared.DefaultSnapshotPath())
}

func refreshRoutes(env *domain.BundleEnvelope) int {
	plan, err := planFromBundle(env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "refresh-routes: %v\n", err)
		return 1
	}

	return printPlan(plan)
}

// planFromBundle L6：NetworkIntent.Routes → apply 计划。
//
// refresh-routes（打印计划给人看/存盘）与 apply --auto（直接执行）共用这一份，
// 否则两条路径的翻译规则迟早漂移 —— 而漂移的症状是「打印的计划」和
// 「真执行的」不是同一件事，极难发现。
//
// 返回错误 = **这份 bundle 不许翻译成计划**：接口名解析不出来，或某条 OS 路由的
// dev 指向的不是本节点隧道口。后者是 182 事故的形态（`ip route … dev gnb_tun`
// 命中另一个 zone 的接口，命令执行成功、路由"装上了"，流量却进了别人的隧道），
// 所以这里必须在装之前拒掉，而不是等 resolveDevice 按名字找接口。
func planFromBundle(env *domain.BundleEnvelope) (ApplyPlan, error) {
	var plan ApplyPlan

	dev, err := env.TunDevice()
	if err != nil {
		return plan, err
	}

	for _, r := range env.Network.Routes {
		if err := env.DeviceForRoute(r.Dev); err != nil {
			return plan, fmt.Errorf("路由 %s: %w", r.Prefix, err)
		}
		plan.Routes = append(plan.Routes, RouteOp{
			Action:      "replace",
			Destination: r.Prefix,
			Gateway:     r.Via,
			Device:      r.Dev,
			Metric:      r.Metric,
		})
	}
	// Device 是逻辑名（如 gnb_tun_16）；darwin 的 gnb 实际拿到 utunN。
	// 把本机 overlay 地址带上，执行期才能反查真实接口（见 resolveDevice）。
	plan.LocalAddresses = append(plan.LocalAddresses, env.Network.Addresses...)

	// 代理分流（主线⑦）：intent 是唯一真相。
	//
	// D1 收窄后 wire 的 network.proxy 只带**出口许可**（enabled + acl）；客户端策略
	// （role/region/split/exits/dns…）归 Local 轨。先把云端许可 stamp 进 Cloud 轨，
	// 再由 Effective() 按职责合成：server 侧←许可、client 侧←Local。
	// intent 无任何记录时才回落 bundle 原许可（升级路径兼容）。
	plan.ProxyExitDevice = dev
	if previous, err := LoadProxyState(); err == nil && previous != nil {
		plan.LegacyProxyExit = previous.Cloud != nil
	}
	// generation 是 `rev-{YmdHis}-{nodeId}-{seq}`，必须映射成单调 revision，
	// 否则 ParseInt 恒 0、旧 generation 重放会被当成最新。
	StampCloudIntent(env.Network.Proxy, domain.GenerationRevision(env.Generation))
	var proxyPolicy *domain.ProxyPolicy
	if st, err := LoadProxyState(); err == nil && st != nil && (st.Local != nil || st.Cloud != nil) {
		if eff := st.Effective(); eff != nil {
			proxyPolicy = &eff.Policy
		}
	} else if env.Network.Proxy != nil && env.Network.Proxy.Enabled {
		p := env.Network.Proxy.AsCloudPolicy()
		proxyPolicy = &p
	}
	if proxyPolicy != nil {
		rt := ProxyRuntime{
			TunDevice: dev,
		}
		if ex := firstEnabledExit(proxyPolicy); ex != nil {
			rt.ExitVia = resolveExitVia(env, ex)
		}
		// 「按目的地并存」：策略里若有 wg 出口（分组 targets），补一套 wg 路径。
		// gnb 路径保持不变；两组目的按 nft 链 priority 先后分流。
		if pc := loadPluginProxyConfig(); pc != nil && pc.HasWireGuardExit() {
			rt.WGIface = firstWGExitIface(pc)
			if tg, all := pc.WireGuardTargets(); !all {
				rt.WGTargets = tg
			}
			// all（顶层 wg 出口 = 整份 split 集合都走 wg）暂不在此展开：
			// 那等价于「gnb 路径让位」，需从集合文件灌 wg set，属下一步。
		}
		if proxyPolicy.Split != nil && proxyPolicy.Split.IPList != nil {
			// 缓存路径由 source 决定；URL 在执行期 FetchProxyIPList 使用
			src := proxyPolicy.Split.IPList.Source
			if src == "" {
				src = proxyPolicy.Split.ListSource
			}
			rt.IPSetFile = ProxyListCachePath(filepath.Join(varDir(), "proxy-lists"), src)
		} else if f := bundledProxyListPath(proxyPolicy); f != "" {
			// 本地 client 策略没有云端解算的 split（例：纯本机 Local 轨、或云侧无许可）→
			// 回落到**随包列表**（旧 Lua 行为：domestic→interip.txt / international→chinaip.txt）。
			// 不回落的后果：集合空 → 打标规则匹配不到任何目的 → 代理形同没开
			// （2026-10-11 .182 实测）。
			rt.IPSetFile = f
		}
		plan.Proxy = planProxy(proxyPolicy, rt)
	}

	return plan, nil
}

// firstWGExitIface 第一个启用 wg 出口声明的隧道接口名（顶层 Exits 或任一分组内）。
// 空名不返回 —— 没接口名就不写 wg 路径（宁可退回纯 gnb，也不猜接口）。
func firstWGExitIface(c *domain.PluginProxyConfig) string {
	if c == nil {
		return ""
	}
	for i := range c.Exits {
		if e := &c.Exits[i]; e.Enabled && e.IsWireGuard() && e.Iface != "" {
			return e.Iface
		}
	}
	for gi := range c.Groups {
		for ei := range c.Groups[gi].Exits {
			e := &c.Groups[gi].Exits[ei]
			if e.Enabled && e.IsWireGuard() && e.Iface != "" {
				return e.Iface
			}
		}
	}
	return ""
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
	// 接口名的唯一权威是 node.interface（back 侧 = node_configs.config.ifname）。
	// gnb 的渲染只读 Options.Ifname，所以投影前用权威值覆盖它：back 一旦停发那份
	// 过渡期别名，这条路径也不会退回 gnb 自己的默认名（在多 zone 主机上那是别人
	// 正在用的接口）。解析不出来就是配置错误 —— 报错退出，不猜。
	dev, err := env.TunDevice()
	if err != nil {
		fmt.Fprintf(os.Stderr, "refresh-config: %v\n", err)
		return 1
	}
	if env.Gnb != nil {
		if env.Gnb.Options == nil {
			env.Gnb.Options = &domain.GnbOptions{}
		}
		env.Gnb.Options.Ifname = dev
	}

	f := driver.NewFactory()
	switch env.Driver {
	case domain.DriverTypeWireGuard:
		d := driver.NewWireGuardDriver(dev, 0)
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
