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
	"time"

	"github.com/mynet-club/mynet_agent-V2/mynet/driver"
	"github.com/mynet-club/mynet_agent-V2/mynet/keys"
	"github.com/mynet-club/mynet_agent-V2/shared/config"
	"github.com/mynet-club/mynet_agent-V2/shared/domain"
	"github.com/mynet-club/mynet_agent-V2/shared/utils"
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
		if dry {
			fmt.Printf("dry-run: 将写入 wireguard conf（iface=%s）\n",
				env.Node.Interface)
			return nil
		}
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
			g.Listeners = []string{fmt.Sprintf("0.0.0.0:%d", li.Listen.UDPPort)}
		}
		g.Forwarding = &domain.GnbForwarding{
			Direct: true,
			Proxy:  li.Listen.Proxy,
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
	if dry {
		fmt.Printf("dry-run: 将写入 driver/gnb/conf/%s/（node/route/address/keys）\n", nodeID)
		return writeProxyProjection(nodeID, pp, true)
	}
	d := &driver.GNBDriver{ConfDir: filepath.Join(utils.GnbConfDir(), nodeID)}
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
	iface := env.Node.Interface
	if iface == "" {
		iface = "wg0"
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
