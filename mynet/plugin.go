package mynet

// 插件子系统 —— 配置在 conf/**/plugins/，包体在 plugins/，运行态在 var/plugins/。
//
//	mynet plugin list
//	mynet plugin enable|disable <name>
//	mynet plugin install <name> [--from DIR]
//	mynet plugin status [name]
//	mynet plugin exits list|add|rm|weight …
//
// **proxy 插件是 proxy 的唯一入口**：是否启用、从哪些 peer 出去、权重/顺序。
// enable 后 apply 会：
//   1. 探测 gnb 是否支持 proxy（P2）—— 不支持则高声提醒、不写配置
//   2. 写 driver/gnb/conf/<id>/proxy.conf（选点/权重/顺序投影）
//   3. 出口对端进投影 + node.conf `proxy on`（不写 address.conf 角色）
//
// 与「intent.listen.proxy」的关系：插件是用户开关；listen.proxy 是意图里的
// 节点级位，插件 enable 时会被强制为 true。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent-V2/shared/config"
	"github.com/mynet-club/mynet_agent-V2/shared/domain"
	"github.com/mynet-club/mynet_agent-V2/shared/utils"
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
		fmt.Fprintln(os.Stderr, "用法: mynet plugin list|enable|disable|install|status|apply|exits …")
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
