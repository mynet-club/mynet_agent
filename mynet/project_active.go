package mynet

// ApplyFromActive —— 按 mode.json 的激活源生成 driver 投影。
//
//	local  → conf/local/intent.json + keys → GnbIntent/Wg → WriteIntent
//	online → conf/online/{t}/bundle.json  → envelope.Gnb/WG → WriteIntent
//
// 投影只进 driver/**；last-good 落 state/last-good/。这里不碰 OS 路由
// （那是 ApplyPlan），只负责「意图 → 驱动原生配置」。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/driver"
	"github.com/mynet-club/mynet_agent/mynet/keys"
	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// ApplyFromActive 投影当前激活配置到 driver/。dry 只打印不落盘。
func ApplyFromActive(dry bool) error {
	store := &config.Store{}
	src, mode, err := store.ActiveSource()
	if err != nil {
		return err
	}
	if mode == nil {
		return fmt.Errorf("尚未初始化：先 mynet setup --local 或 --online")
	}

	switch mode.Mode {
	case domain.ModeLocal:
		li, err := store.LoadLocalIntent()
		if err != nil {
			return fmt.Errorf("load local intent: %w", err)
		}
		return applyLocal(store, src, li, dry)
	case domain.ModeOnline:
		return applyOnline(store, src, mode, dry)
	default:
		return fmt.Errorf("未知 mode: %s", mode.Mode)
	}
}

func applyLocal(store *config.Store, src string, li *domain.LocalIntent, dry bool) error {
	priv, pub, algo, keyNode, err := keys.LoadNodeKeys(utils.ResolveKeysDir(src, li.Node.ID))
	if err != nil {
		return fmt.Errorf("load keys: %w", err)
	}
	if keyNode != "" && li.Node.ID != "" && keyNode != li.Node.ID {
		return fmt.Errorf("keys.meta.node_id=%s 与 intent.node.id=%s 不一致", keyNode, li.Node.ID)
	}
	gi, err := LocalToGnbIntent(li, priv, pub, algo)
	if err != nil {
		return err
	}
	if err := writeGnbProjection(gi, dry); err != nil {
		return err
	}
	// 插件投影（proxy 等）
	return ApplyPlugins(dry)
}

func applyOnline(store *config.Store, src string, mode *domain.ModeFile, dry bool) error {
	// mynetd 拉包落 var/bundle.json；conf/**/bundle.json 只是备份。
	// 优先 var/，否则 apply --active 与 apply --auto 会各吃一份、路由对不上。
	raw, err := os.ReadFile(filepath.Join(utils.VarDir(), "bundle.json"))
	if err != nil {
		raw, err = os.ReadFile(filepath.Join(src, "bundle.json"))
	}
	if err != nil {
		return fmt.Errorf("online 配置缺 bundle.json（先拉包）: %w", err)
	}
	// OnlineBundleFile 包一层；也容忍直接是 envelope
	var wrap domain.OnlineBundleFile
	var env *domain.BundleEnvelope
	if err := jsonUnmarshal(raw, &wrap); err == nil && wrap.Envelope != nil {
		env = wrap.Envelope
	} else {
		env, err = domain.DecodeBundleEnvelope(raw)
		if err != nil {
			return fmt.Errorf("decode bundle: %w", err)
		}
	}
	if env == nil {
		return fmt.Errorf("bundle 无驱动 intent")
	}
	// 双驱动：Envelope 互斥（A-8）——gnb 段与 wireguard 段只会有一个。
	if env.Gnb == nil && env.WireGuard == nil {
		return fmt.Errorf("bundle 无驱动 intent")
	}
	if mode.Active.NodeID != "" && env.Node.ID != "" && mode.Active.NodeID != env.Node.ID {
		// 以 bundle 为准校正 active（拉包后的节点才是真身份），
		// 并继续投影 —— 否则「启动失败」会卡在不一致上。
		fmt.Printf("⚠ active.node_id=%s 与 bundle.node.id=%s 不一致，以 bundle 为准\n",
			mode.Active.NodeID, env.Node.ID)
		mode.Active.NodeID = env.Node.ID
		store.SaveMode(mode)
	}

	// wireguard 投影（A-5）：WriteIntent → /etc/wireguard/<iface>.conf
	if env.WireGuard != nil {
		// dry-run 也走 writeWgProjection：那里的打印用的是**解析后的**接口名，
		// 在这里自己打 envelope 原始字段会让 dry 与真跑给出不同结论
		// （接口名解析失败时 dry 会显示空值，而 apply 其实直接报错）。
		if err := writeWgProjection(env, dry); err != nil {
			return err
		}
		fmt.Println("✓ driver 投影 wireguard")

		return nil
	}

	// 服务端不存 private —— 从本机 keys/ 注入，否则 security/ 只有 public、gnb 起不来
	kd := utils.ResolveKeysDir(src, env.Node.ID)
	if priv, pub, _, _, err := keys.LoadNodeKeys(kd); err == nil && priv != "" {
		if env.Gnb.Identity.NodeID == "" {
			env.Gnb.Identity.NodeID = env.Node.ID
		}
		env.Gnb.Identity.KeyAlgorithm = "ed25519"
		env.Gnb.Identity.PrivateKey = priv
		if env.Gnb.Identity.PublicKey == "" {
			env.Gnb.Identity.PublicKey = pub
		}
	}
	// 接口名唯一权威是 node.interface（back 侧同源：node_configs.config.ifname）。
	// 落到驱动投影前必须用权威值覆盖 gnb.options.ifname —— 那份是**过渡期别名**：
	// back 一旦停发它（见 mynet_ctl 计划 B 的第二步），只读 Options.Ifname 的
	// 渲染路径就会让 gnb 用它自己的默认名静默建口，而默认名在一台多 zone 的机器上
	// 往往是**别人正在用的接口**。名字解析不出来就是配置错误，直接失败。
	dev, err := env.TunDevice()
	if err != nil {
		return err
	}
	if env.Gnb.Options == nil {
		env.Gnb.Options = &domain.GnbOptions{}
	}
	env.Gnb.Options.Ifname = dev

	if err := writeGnbProjection(env.Gnb, dry); err != nil {
		return err
	}
	return ApplyPlugins(dry)
}

// LocalToGnbIntent 本地意图 + 密钥 → GnbIntent（WriteIntent 的入口形状）。
func LocalToGnbIntent(li *domain.LocalIntent, privHex, pubHex, algorithm string) (*domain.GnbIntent, error) {
	if li == nil {
		return nil, fmt.Errorf("intent 为空")
	}
	if li.Driver != domain.DriverTypeGNB {
		return nil, fmt.Errorf("LocalToGnbIntent 只支持 gnb，实际 %s", li.Driver)
	}
	if algorithm == "" {
		algorithm = "ed25519"
	}
	g := &domain.GnbIntent{
		Identity: domain.GnbIdentity{
			NodeID:       li.Node.ID,
			KeyAlgorithm: "ed25519",
			PrivateKey:   privHex,
			PublicKey:    pubHex,
		},
	}
	if li.Listen != nil {
		if li.Listen.UDPPort > 0 {
			// **裸端口**，不是 `0.0.0.0:<port>`：gnb 按 listen 值的形态决定绑哪个地址族
			// （gnb_conf.c:71-81 check_listen_string + gnb_conf_file.c:300-333）——
			// 纯数字同时设 [:::port] 和 0.0.0.0:port，含 ':' 只设 IPv4（IPv6 留在出厂
			// 默认 9001），'[' 开头只设 IPv6。写死 v4 前缀等于单方面放弃 IPv6 侧的
			// 发现与打洞，而 back 下发的 index/对端地址都是同一个端口号。
			g.Listeners = []string{strconv.Itoa(li.Listen.UDPPort)}
		}
		g.Forwarding = &domain.GnbForwarding{
			Direct: true,
		}
	}
	peerPub := map[string]string{}
	for _, p := range li.Peers {
		if p.ID == "" {
			continue
		}
		g.Nodes = append(g.Nodes, domain.GnbNode{
			NodeID:     p.ID,
			PublicKey:  p.PubKey,
			TunAddress: p.Overlay,
		})
		if p.PubKey != "" {
			peerPub[p.ID] = p.PubKey
		}
		if p.Endpoint != "" {
			if g.Discovery == nil {
				g.Discovery = &domain.GnbDiscovery{}
			}
			// 不写 f 角色：address.conf 一律默认 n。proxy/relay 语义由
			// node.conf 与业务意图表达，不在 address.conf 打 f（与 spec/proxy-split.md 一致）。
			g.Discovery.StaticNodes = append(g.Discovery.StaticNodes, domain.GnbEndpointHint{
				NodeID:   p.ID,
				Endpoint: p.Endpoint,
				Role:     "",
			})
		}
	}
	g.Identity.PeerPublicKeys = peerPub

	for _, r := range li.Routes {
		nid := r.Via
		if nid == "" {
			// 默认给第一个对端；没有对端的路由没有意义
			if len(li.Peers) > 0 {
				nid = li.Peers[0].ID
			}
		}
		g.Routes = append(g.Routes, domain.GnbRoute{
			NodeID: nid,
			Prefix: r.CIDR,
		})
	}
	// 本节点 overlay 也要进 route（/32）
	if li.Node.Overlay != "" {
		g.Routes = append([]domain.GnbRoute{{
			NodeID: li.Node.ID,
			Prefix: li.Node.Overlay,
		}}, g.Routes...)
	}
	if li.Discover != nil && len(li.Discover.IndexServers) > 0 {
		if g.Discovery == nil {
			g.Discovery = &domain.GnbDiscovery{}
		}
		g.Discovery.IndexServers = li.Discover.IndexServers
	}
	if li.MTU > 0 {
		g.Options = &domain.GnbOptions{MTU: li.MTU}
	}
	return g, nil
}

func writeGnbProjection(gi *domain.GnbIntent, dry bool) error {
	nodeID := gi.Identity.NodeID
	if nodeID == "" {
		return fmt.Errorf("intent 缺 node id")
	}
	// proxy 插件启用时：写出口对端 + 打开 proxy on，并准备写 proxy.conf
	pp := enrichIntentWithProxy(gi)
	confDir := filepath.Join(utils.GnbConfDir(), nodeID)
	// C5：driver/** 是投影，手改会在本次被覆盖 —— 说出来，不静默。
	warnHandEdits(confDir)
	if dry {
		fmt.Printf("dry-run: 将写入 driver/gnb/conf/%s/（node/route/address/keys）\n", nodeID)
		return writeProxyProjection(nodeID, pp, true)
	}
	d := &driver.GNBDriver{ConfDir: confDir}
	if err := utils.SafeCreateDir(d.ConfDir, 0o700); err != nil {
		return err
	}
	if err := d.WriteIntent(gi); err != nil {
		return fmt.Errorf("WriteIntent: %w", err)
	}
	if err := writeProxyProjection(nodeID, pp, false); err != nil {
		return err
	}
	// last-good 快照
	snap := filepath.Join(utils.LastGoodDir(), time.Now().UTC().Format("20060102T150405Z"))
	if err := utils.SafeCreateDir(snap, 0o700); err == nil {
		_ = copyDirFiles(d.ConfDir, snap)
	}
	fmt.Printf("✓ driver 投影 %s\n", d.ConfDir)
	return nil
}

// writeWgProjection — wireguard 投影（A-5）。
// conf 落 /etc/wireguard/<iface>.conf（wg-quick 原生位）；
// 同步一份到 $MYNET_HOME/var/wg.conf 供 init 回退查找。
func writeWgProjection(env *domain.BundleEnvelope, dry bool) error {
	if env.WireGuard == nil {
		return fmt.Errorf("bundle 无 wireguard intent")
	}
	// 不回落 "wg0"：接口名是阻断性配置，猜出来的名字会建到另一个口上且不报错
	// （见 domain.BundleEnvelope.TunDevice）。back 对 wg 节点同样发 node.interface。
	iface, err := env.TunDevice()
	if err != nil {
		return err
	}
	// L5 驱动侧放行（wg 方言）：proxy client 时把出口目的集并进出口 peer 的 AllowedIPs。
	// 只打标不放行 → wg 按 AllowedIPs 做 cryptokey routing，包被静默丢弃
	// （spec/proxy-split.md §15.1）。fail-loud：出口选点对不上 wg peer 即报错，不写盘。
	if p := EffectiveProxyPolicy(); p != nil && p.Enabled && p.IsClientLike() && len(p.Exits) > 0 {
		dest, derr := WGProxyDestCIDRs(p)
		if derr != nil {
			return fmt.Errorf("wg proxy 放行: %w", derr)
		}
		if err := ProjectProxyToWG(env.WireGuard, p, dest); err != nil {
			return fmt.Errorf("wg proxy 放行: %w", err)
		}
		fmt.Printf("✓ wg proxy 放行：出口 AllowedIPs ∪ 目的集（%d 条）\n", len(dest))
	}
	d := driver.NewWireGuardDriver(iface, 0)
	if dry {
		fmt.Printf("dry-run: 将写入 /etc/wireguard/%s.conf\n", iface)
		return nil
	}
	if err := d.WriteIntent(env.WireGuard); err != nil {
		return fmt.Errorf("WriteIntent: %w", err)
	}
	// 变量树副本：OpenWrt init 的 wg_conf_path 回退位
	if varConf := filepath.Join(varDir(), "wg.conf"); varConf != d.ConfPath {
		if b, err := os.ReadFile(d.ConfPath); err == nil {
			_ = os.WriteFile(varConf, b, 0o600)
		}
	}
	fmt.Printf("✓ driver 投影 %s\n", d.ConfPath)
	return nil
}

func copyDirFiles(src, dst string) error {
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range ents {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			_ = utils.SafeCreateDir(d, 0o700)
			_ = copyDirFiles(s, d)
			continue
		}
		b, err := os.ReadFile(s)
		if err != nil {
			continue
		}
		_ = os.WriteFile(d, b, 0o600)
	}
	return nil
}

func jsonUnmarshal(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

// warnHandEdits C5：driver/** 是 Apply 投影，手改会被覆盖。
// 只告警不阻断 —— 阻断会让「改了 intent 再 apply」变麻烦；真相源在 conf/**。
func warnHandEdits(confDir string) {
	for _, name := range []string{"node.conf", "route.conf", "address.conf", "proxy.conf"} {
		p := filepath.Join(confDir, name)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		// 投影文件 mtime 若比 last-good 新很多且非本次写入，可能是手改。
		// 简化判据：文件存在即提示一次（避免复杂 diff）。
		_ = st
		fmt.Printf("⚠ %s 是 Apply 投影，本次将覆盖 —— 要改请改 conf/** 后再 apply（spec/paths.md §5）\n", p)
		return
	}
}
