package mynet

// 插件子系统 —— 配置在 conf/**/plugins/，包体在 plugins/，运行态在 var/plugins/。
//
//	mynet plugin list
//	mynet plugin enable|disable <name>
//	mynet plugin install <name> [--from DIR]
//	mynet plugin status [name]
//	mynet plugin exits list|add|rm|weight …
//
// ── proxy 单一入口（C6 修订，见 spec/paths.md §5）────────────
//
//	plugins/proxy.json → driver/gnb/conf/<id>/proxy.conf
//	   = `proxy on` + 出口选点 / 权重 / 顺序（role/exit/w/order）
//	   「开不开」与「走谁」都在这一份
//
// **proxy 插件是 proxy 的唯一入口**：是否启用、从哪些 peer 出去、权重/顺序。
// enable 后 apply 会：
//   1. 探测 gnb 是否支持 proxy（P2）—— 不支持则高声提醒、不写配置
//   2. 写 driver/gnb/conf/<id>/proxy.conf（proxy on + 选点/权重/顺序投影）
//   3. 出口对端进投影（不写 address.conf 角色）
//
// gnb 的 proxy 开关在 proxy.conf 里，**不进 node.conf**。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// PluginPackage plugins/{name}/manifest.json
type PluginPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Entry   string `json:"entry"` // bin/xxx 或 main.js
	OS      string `json:"os,omitempty"`
	Arch    string `json:"arch,omitempty"`
	Desc    string `json:"desc,omitempty"`
}

func cmdPlugin(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet plugin list|enable|disable|install|status|apply|exits|groups …")
		return 2
	}
	switch args[0] {
	case "list":
		return pluginList()
	case "enable":
		return pluginSetEnabled(args[1:], true)
	case "disable":
		return pluginSetEnabled(args[1:], false)
	case "install":
		return pluginInstall(args[1:])
	case "status":
		return pluginStatus(args[1:])
	case "apply":
		// plugin apply [name] —— 重投影已启用插件（proxy → proxy.conf + proxy on；出口=下一跳）
		return pluginApply(args[1:])
	case "exits":
		return pluginExits(args[1:])
	case "groups":
		// D7：P2 [group] + target 分流（与 exits 的 [default] 互补）
		return pluginGroups(args[1:])
	case "domains":
		// D5：清单 + 白名单 + 自定义叠加
		return pluginDomains(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知 plugin 子命令 %q\n", args[0])
		return 2
	}
}

// pluginApply 重跑插件投影。name 为空则全部；proxy 会走 enrichIntentWithProxy + writeProxyProjection。
func pluginApply(args []string) int {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	if name != "" && name != domain.PluginNameProxy {
		fmt.Fprintf(os.Stderr, "plugin apply 暂只支持 proxy，收到 %q\n", name)
		return 2
	}
	EnsurePluginSynced()
	if err := ApplyPlugins(false); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("✓ plugin apply 完成")
	return 0
}

// pluginExits proxy 出站选点：list | add | rm | weight
//
//	mynet plugin exits list
//	mynet plugin exits add <node_id> [--weight N] [--order N]
//	mynet plugin exits rm <node_id>
//	mynet plugin exits weight <node_id> --weight N
func pluginExits(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet plugin exits list|add|rm|weight …")
		return 2
	}
	base, store, err := activePluginBase()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pc, err := loadPluginAt(base, domain.PluginNameProxy)
	if err != nil {
		// 无配置则给默认骨架
		np := domain.NewPluginProxy()
		pc = &np
	}
	pp, err := domain.DecodePluginProxy(pc)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	switch args[0] {
	case "list":
		fmt.Printf("proxy exits（enabled=%v max=%d）\n", pc.Enabled, pp.MaxExits)
		eff := pp.EffectiveExits()
		for i, e := range eff {
			fmt.Printf("  %d. %s  weight=%d order=%d  ← 生效\n", i+1, e.NodeID, e.Weight, e.Order)
		}
		for _, e := range pp.Exits {
			if !e.Enabled {
				fmt.Printf("  -  %s  weight=%d  （未启用）\n", e.NodeID, e.Weight)
			}
		}
		if len(eff) == 0 {
			fmt.Println("  （空 —— mynet plugin exits add <node_id>）")
		}
		return 0
	case "add":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "用法: mynet plugin exits add <node_id> [--weight N] [--order N]")
			return 2
		}
		e := domain.ProxyExit{NodeID: args[1], Weight: 10, Enabled: true}
		for i := 2; i < len(args); i++ {
			if args[i] == "--weight" && i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &e.Weight)
			}
			if args[i] == "--order" && i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &e.Order)
			}
		}
		// upsert
		found := false
		for i := range pp.Exits {
			if pp.Exits[i].NodeID == e.NodeID {
				pp.Exits[i] = e
				found = true
			}
		}
		if !found {
			pp.Exits = append(pp.Exits, e)
		}
	case "rm":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "用法: mynet plugin exits rm <node_id>")
			return 2
		}
		var keep []domain.ProxyExit
		for _, e := range pp.Exits {
			if e.NodeID != args[1] {
				keep = append(keep, e)
			}
		}
		pp.Exits = keep
	case "weight":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "用法: mynet plugin exits weight <node_id> --weight N")
			return 2
		}
		w := 10
		for i := 2; i < len(args); i++ {
			if args[i] == "--weight" && i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &w)
			}
		}
		ok := false
		for i := range pp.Exits {
			if pp.Exits[i].NodeID == args[1] {
				pp.Exits[i].Weight = w
				ok = true
			}
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "无出口 %s\n", args[1])
			return 1
		}
	default:
		fmt.Fprintln(os.Stderr, "用法: mynet plugin exits list|add|rm|weight …")
		return 2
	}

	// 写回
	raw, err := json.Marshal(pp)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pc.Config = raw
	if err := store.SavePlugin(base, pc); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// 双轨合一：出口写入 intent
	if err := SetProxyExits(pp.Exits); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("✓ 已更新 %s/proxy.json（%d 个出口）\n", base, len(pp.Exits))
	return 0
}

// pluginGroups P2 [group] + target 分流（D7）。
//
//	mynet plugin groups list
//	mynet plugin groups add <name> --target <cidr> [--target …]
//	mynet plugin groups target <name> --target <cidr> --add|--rm
//	mynet plugin groups exit <name> <node_id> [--weight N] [--order N] [--rm]
//	mynet plugin groups rm <name>
//
// 语义（P2）：组名是地区/业务标签；组内最长前缀匹配 target；
// 组内无出口 = DROP，不回落 [default]。
func pluginGroups(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet plugin groups list|add|target|exit|rm …")
		return 2
	}
	base, store, err := activePluginBase()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pc, err := loadPluginAt(base, domain.PluginNameProxy)
	if err != nil {
		np := domain.NewPluginProxy()
		pc = &np
	}
	pp, err := domain.DecodePluginProxy(pc)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	save := func() int {
		raw, err := json.Marshal(pp)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		pc.Config = raw
		if err := store.SavePlugin(base, pc); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("✓ 已更新 %s/proxy.json（%d 组）\n", base, len(pp.Groups))
		return 0
	}
	find := func(name string) *domain.ProxyGroup {
		for i := range pp.Groups {
			if pp.Groups[i].Name == name {
				return &pp.Groups[i]
			}
		}
		return nil
	}

	switch args[0] {
	case "list":
		fmt.Printf("proxy groups（%d）\n", len(pp.Groups))
		for _, g := range pp.Groups {
			fmt.Printf("  [%s]\n", g.Name)
			for _, t := range g.Targets {
				fmt.Printf("    target %s\n", t)
			}
			if len(g.Exits) == 0 {
				fmt.Println("    （无出口 —— 组内无出口 = DROP）")
			}
			for _, e := range g.Exits {
				mark := ""
				if e.Enabled {
					mark = " ← 生效"
				}
				fmt.Printf("    exit %s w=%d order=%d%s\n", e.NodeID, e.Weight, e.Order, mark)
			}
		}
		if len(pp.Groups) == 0 {
			fmt.Println("  （空 —— mynet plugin groups add <name> --target <cidr>）")
		}
		return 0

	case "add":
		// groups add <name> --target <cidr> [--target …] [--exit <node>] [--weight N]
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "用法: mynet plugin groups add <name> --target <cidr> [--target …]")
			return 2
		}
		name := args[1]
		if find(name) != nil {
			fmt.Fprintf(os.Stderr, "组已存在: %s（用 groups target/exit 追加）\n", name)
			return 1
		}
		g := domain.ProxyGroup{Name: name}
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--target":
				if i+1 < len(args) {
					i++
					g.Targets = append(g.Targets, args[i])
				}
			case "--exit":
				if i+1 < len(args) {
					i++
					g.Exits = append(g.Exits, domain.ProxyExit{NodeID: args[i], Weight: 10, Enabled: true})
				}
			}
		}
		pp.Groups = append(pp.Groups, g)
		return save()

	case "target":
		// groups target <name> --target <cidr> --add|--rm
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "用法: mynet plugin groups target <name> --target <cidr> --add|--rm")
			return 2
		}
		g := find(args[1])
		if g == nil {
			fmt.Fprintf(os.Stderr, "无组 %s\n", args[1])
			return 1
		}
		t, mode := "", "add"
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--target":
				if i+1 < len(args) {
					i++
					t = args[i]
				}
			case "--add":
				mode = "add"
			case "--rm":
				mode = "rm"
			}
		}
		if t == "" {
			fmt.Fprintln(os.Stderr, "需要 --target")
			return 2
		}
		if t == "0.0.0.0/0" || t == "0/0" {
			fmt.Fprintln(os.Stderr, "target /0 被 P2 拒载，跳过")
			return 1
		}
		if mode == "rm" {
			var keep []string
			for _, x := range g.Targets {
				if x != t {
					keep = append(keep, x)
				}
			}
			g.Targets = keep
		} else {
			g.Targets = append(g.Targets, t)
		}
		return save()

	case "exit":
		// groups exit <name> <node_id> [--weight N] [--order N] [--rm]
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "用法: mynet plugin groups exit <name> <node_id> [--weight N] [--order N] [--rm]")
			return 2
		}
		g := find(args[1])
		if g == nil {
			fmt.Fprintf(os.Stderr, "无组 %s\n", args[1])
			return 1
		}
		nodeID := args[2]
		rm := false
		w, order := 10, 0
		for i := 3; i < len(args); i++ {
			switch args[i] {
			case "--weight":
				if i+1 < len(args) {
					i++
					fmt.Sscanf(args[i], "%d", &w)
				}
			case "--order":
				if i+1 < len(args) {
					i++
					fmt.Sscanf(args[i], "%d", &order)
				}
			case "--rm":
				rm = true
			}
		}
		if rm {
			var keep []domain.ProxyExit
			for _, e := range g.Exits {
				if e.NodeID != nodeID {
					keep = append(keep, e)
				}
			}
			g.Exits = keep
			return save()
		}
		upserted := false
		for i := range g.Exits {
			if g.Exits[i].NodeID == nodeID {
				g.Exits[i].Weight = w
				g.Exits[i].Order = order
				g.Exits[i].Enabled = true
				upserted = true
			}
		}
		if !upserted {
			g.Exits = append(g.Exits, domain.ProxyExit{NodeID: nodeID, Weight: w, Order: order, Enabled: true})
		}
		return save()

	case "rm":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "用法: mynet plugin groups rm <name>")
			return 2
		}
		var keep []domain.ProxyGroup
		for _, g := range pp.Groups {
			if g.Name != args[1] {
				keep = append(keep, g)
			}
		}
		pp.Groups = keep
		return save()

	default:
		fmt.Fprintln(os.Stderr, "用法: mynet plugin groups list|add|target|exit|rm …")
		return 2
	}
}

// pluginDomains D5：清单 + 白名单 + 自定义叠加。
//
//	mynet plugin domains list
//	mynet plugin domains add <domain>       # 加自定义代理
//	mynet plugin domains rm <domain>
//	mynet plugin domains direct add <domain>
//	mynet plugin domains direct rm <domain>
//
// 合并优先级：自定义直连 > 白名单 > 自定义代理 > 基础清单。
// 自定义只在本机（proxy.json），不回写云端。
func pluginDomains(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet plugin domains list|add|rm|direct …")
		return 2
	}
	base, _, err := activePluginBase()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, _ = loadPluginAt(base, domain.PluginNameProxy) // 确保插件壳存在；失败则下面用空规则

	// 读 intent 上的 DomainRules（基础清单）+ 插件自定义
	rules := &domain.ProxyDomainRules{}
	if p := EffectiveProxyPolicy(); p != nil && p.DomainRules != nil {
		rules = p.DomainRules
	}

	saveCustoms := func(customP, customD, customIPs []string) int {
		if err := SetProxyCustomAllowlist(customP, customD, customIPs); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("✓ 自定义白名单已更新（domain proxy %d / direct %d / ip %d）\n",
			len(customP), len(customD), len(customIPs))
		return 0
	}

	switch args[0] {
	case "list":
		fmt.Println("域名规则（D5 合并优先级：自定义直连 > 白名单 > 自定义代理 > 基础清单）")
		fmt.Printf("  基础代理清单: %d 条\n", len(rules.Proxied))
		fmt.Printf("  直连白名单  : %d 条\n", len(rules.Direct))
		fmt.Printf("  自定义代理  : %d 条\n", len(rules.CustomDomains))
		fmt.Printf("  自定义直连  : %d 条\n", len(rules.CustomDirect))
		fmt.Printf("  自定义 IP   : %d 条\n", len(rules.CustomIPs))
		fmt.Println("  —— 合并结果 ——")
		effP := rules.EffectiveProxied()
		effD := rules.EffectiveDirect()
		fmt.Printf("  最终代理: %d 条\n", len(effP))
		for i, d := range effP {
			if i >= 20 {
				fmt.Printf("    … 共 %d 条\n", len(effP))
				break
			}
			fmt.Printf("    %s\n", d)
		}
		fmt.Printf("  最终直连: %d 条\n", len(effD))
		for i, d := range effD {
			if i >= 10 {
				fmt.Printf("    … 共 %d 条\n", len(effD))
				break
			}
			fmt.Printf("    %s\n", d)
		}
		if len(rules.CustomIPs) > 0 {
			fmt.Println("  —— IP 钉扎白名单（不经 DNS，直接 /32）——")
			for i, ip := range rules.CustomIPs {
				if i >= 10 {
					fmt.Printf("    … 共 %d 条\n", len(rules.CustomIPs))
					break
				}
				fmt.Printf("    %s\n", ip)
			}
		}
		return 0

	case "add":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "用法: mynet plugin domains add <domain>")
			return 2
		}
		cp := append([]string{}, rules.CustomDomains...)
		cp = append(cp, args[1])
		return saveCustoms(uniqStrings(cp), rules.CustomDirect, rules.CustomIPs)

	case "rm":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "用法: mynet plugin domains rm <domain>")
			return 2
		}
		var cp []string
		for _, d := range rules.CustomDomains {
			if normalizeDomainName(d) != normalizeDomainName(args[1]) {
				cp = append(cp, d)
			}
		}
		return saveCustoms(cp, rules.CustomDirect, rules.CustomIPs)

	case "ip":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "用法: mynet plugin domains ip add|rm <ip>")
			return 2
		}
		ci := append([]string{}, rules.CustomIPs...)
		switch args[1] {
		case "add":
			ci = append(ci, args[2])
		case "rm":
			var keep []string
			for _, ip := range ci {
				if ip != args[2] {
					keep = append(keep, ip)
				}
			}
			ci = keep
		default:
			fmt.Fprintln(os.Stderr, "用法: mynet plugin domains ip add|rm <ip>")
			return 2
		}
		// IP 列表不走 normalizeDomainName（那是域名语义）；去重按字面。
		return saveCustoms(rules.CustomDomains, rules.CustomDirect, uniqIPs(ci))

	case "direct":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "用法: mynet plugin domains direct add|rm <domain>")
			return 2
		}
		cd := append([]string{}, rules.CustomDirect...)
		switch args[1] {
		case "add":
			cd = append(cd, args[2])
		case "rm":
			var keep []string
			for _, d := range cd {
				if normalizeDomainName(d) != normalizeDomainName(args[2]) {
					keep = append(keep, d)
				}
			}
			cd = keep
		default:
			fmt.Fprintln(os.Stderr, "用法: mynet plugin domains direct add|rm <domain>")
			return 2
		}
		return saveCustoms(rules.CustomDomains, uniqStrings(cd), rules.CustomIPs)

	default:
		fmt.Fprintln(os.Stderr, "用法: mynet plugin domains list|add|rm|ip|direct …")
		return 2
	}
}

// uniqIPs IP 列表字面去重（不做域名 normalize）。
func uniqIPs(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func uniqStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		k := normalizeDomainName(s)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// activePluginBase 当前激活源下的插件配置目录。
func activePluginBase() (string, *config.Store, error) {
	store := &config.Store{}
	src, _, err := store.ActiveSource()
	if err != nil {
		return "", nil, err
	}
	return utils.PluginsConfDir(src), store, nil
}

func pluginList() int {
	base, _, err := activePluginBase()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ents, _ := os.ReadDir(base)
	fmt.Printf("插件配置目录 %s\n", base)
	if len(ents) == 0 {
		fmt.Println("（空 —— mynet plugin install proxy）")
		return 0
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		pc, err := loadPluginAt(base, name)
		state := "?"
		if err == nil {
			if pc.Enabled {
				state = "enabled"
			} else {
				state = "disabled"
			}
		}
		pkg := "未装包"
		if _, err := os.Stat(pluginManifestPath(name)); err == nil {
			pkg = "已装包"
		}
		fmt.Printf("  %-12s %-8s %s\n", name, state, pkg)
	}
	return 0
}

func pluginSetEnabled(args []string, on bool) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet plugin enable|disable <name>")
		return 2
	}
	name := args[0]
	// proxy：写双控意图 + 投影插件壳（双轨合一）
	if name == domain.PluginNameProxy {
		if err := SetProxyEnabled(on); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("✓ %s → %s（intent 已同步）\n", name, map[bool]string{true: "enabled", false: "disabled"}[on])
		return 0
	}
	base, store, err := activePluginBase()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pc, err := loadPluginAt(base, name)
	if err != nil {
		// 没有配置时 enable 用默认骨架
		if on && name == domain.PluginNameProxy {
			pc := domain.NewPluginProxy()
			if err := store.SavePlugin(base, &pc); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			fmt.Printf("✓ 已写入默认配置并 enable %s\n", name)
			return 0
		}
		fmt.Fprintf(os.Stderr, "无 %s 配置：%v\n", name, err)
		return 1
	}
	pc.Enabled = on
	if err := store.SavePlugin(base, pc); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("✓ %s → %s\n", name, map[bool]string{true: "enabled", false: "disabled"}[on])
	return 0
}

func pluginInstall(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet plugin install <name> [--from DIR]")
		return 2
	}
	name := args[0]
	from := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "--from" && i+1 < len(args) {
			i++
			from = args[i]
		}
	}
	if err := utils.ValidateDirName(name); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	pkgDir, err := utils.PluginPackageDir(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if from != "" {
		if err := copyDirFiles(from, pkgDir); err != nil {
			fmt.Fprintf(os.Stderr, "install %s: %v\n", name, err)
			return 1
		}
	}
	// 至少要有 manifest
	if _, err := os.Stat(filepath.Join(pkgDir, "manifest.json")); err != nil {
		// 写一份占位 manifest，方便本地开发
		m := PluginPackage{Name: name, Version: "0.0.0-dev", Entry: "bin/" + name}
		if err := writeJSON(filepath.Join(pkgDir, "manifest.json"), m, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	// 默认配置（proxy 有现成骨架）
	base, store, err := activePluginBase()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := loadPluginAt(base, name); err != nil {
		var pc domain.PluginConfig
		if name == domain.PluginNameProxy {
			pc = domain.NewPluginProxy()
		} else {
			pc = domain.PluginConfig{Schema: domain.ConfigSchemaVersion, Plugin: name, Enabled: true}
		}
		if err := store.SavePlugin(base, &pc); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	fmt.Printf("✓ 安装 %s → %s\n", name, pkgDir)
	return 0
}

func pluginStatus(args []string) int {
	name := domain.PluginNameProxy
	if len(args) > 0 {
		name = args[0]
	}
	base, _, err := activePluginBase()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pc, err := loadPluginAt(base, name)
	if err != nil {
		fmt.Printf("%s: 无配置（%v）\n", name, err)
		return 1
	}
	fmt.Printf("plugin=%s enabled=%v autostart=%v\n", pc.Plugin, pc.Enabled, pc.Autostart)
	if name == domain.PluginNameProxy {
		fmt.Printf("  intent: %s\n", DescribeProxyUnified())
		if c, err := domain.DecodePluginProxy(pc); err == nil {
			fmt.Printf("  listen.mixed=%s mode=%s via=%s\n", c.Listen.Mixed, c.Outbound.Mode, c.Outbound.Via)
		}
	}
	vd, err := utils.PluginVarDir(name)
	if err == nil {
		if st, err := os.Stat(filepath.Join(vd, "run.pid")); err == nil {
			fmt.Printf("  var=%s (pid file %d bytes)\n", vd, st.Size())
		} else {
			fmt.Printf("  var=%s（未运行）\n", vd)
		}
	}
	return 0
}

// ApplyPlugins 按 active 配置渲染 var/plugins/{name}/config.generated.json。
// 这是插件投影：真进程拉起仍归各插件 entry。
func ApplyPlugins(dry bool) error {
	base, _, err := activePluginBase()
	if err != nil {
		return err
	}
	ents, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		pc, err := loadPluginAt(base, name)
		if err != nil {
			continue
		}
		if !pc.Enabled {
			continue
		}
		vd, err := utils.PluginVarDir(name)
		if err != nil {
			return err
		}
		if dry {
			fmt.Printf("dry-run: %s → %s/config.generated.json\n", name, vd)
			continue
		}
		body, _ := domain.EncodePretty(pc)
		if err := os.WriteFile(filepath.Join(vd, "config.generated.json"), body, 0o600); err != nil {
			return err
		}
		fmt.Printf("✓ plugin 投影 %s\n", vd)
	}
	return nil
}

func loadPluginAt(base, name string) (*domain.PluginConfig, error) {
	raw, err := os.ReadFile(filepath.Join(base, name+".json"))
	if err != nil {
		return nil, err
	}
	return domain.DecodePluginConfig(raw)
}

func pluginManifestPath(name string) string {
	return filepath.Join(utils.PluginsRoot(), name, "manifest.json")
}

func writeJSON(path string, v any, perm os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := utils.SafeCreateDir(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), perm)
}
