package mynet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

func runtimeModeForOS(osName string) (string, error) {
	return config.LoadRuntimeMode(osName, osName == "openwrt" || (osName == runtime.GOOS && (builtForOpenWrt || openWrtPrefix() != "")))
}
func pcRuntimeForOS(osName string) bool {
	mode, err := runtimeModeForOS(osName)
	return err == nil && mode == config.RuntimePC
}
func cmdConfigRuntime(args []string) int {
	if len(args) == 0 || (len(args) == 1 && args[0] == "--json") {
		mode, err := runtimeModeForOS(runtime.GOOS)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if len(args) == 1 {
			choices := []string{mode}
			if !builtForOpenWrt && (runtime.GOOS == "linux" && openWrtPrefix() == "" || runtime.GOOS == "freebsd" || runtime.GOOS == "openbsd" || runtime.GOOS == "netbsd") {
				choices = []string{"pc", "gw"}
			}
			return printJSON(map[string]any{"mode": mode, "choices": choices})
		}
		fmt.Println(mode)
		return 0
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: mynet config form [pc|gw]")
		return 2
	}
	if err := setRuntimeMode(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("runtime:", args[0])
	return 0
}

func setRuntimeMode(mode string) error {
	if mode != config.RuntimePC && mode != config.RuntimeGW {
		return fmt.Errorf("runtime mode must be pc or gw")
	}
	openwrt := builtForOpenWrt || openWrtPrefix() != ""
	if _, err := config.ResolveRuntimeMode(runtime.GOOS, openwrt, mode); err != nil {
		return err
	}
	old, err := runtimeModeForOS(runtime.GOOS)
	if err != nil {
		return err
	}
	if old == mode {
		return config.SaveRuntimeMode(runtime.GOOS, openwrt, mode)
	}
	// 先释放旧形态。没有运行资源的新安装仅写配置。
	if old == config.RuntimePC && (dnsHookRunning() || hookWatcherAlive() || DNSRestoreNeeded() || len(LoadDNSHookPins()) > 0) {
		if rc := dnsHookStop(); rc != 0 {
			return fmt.Errorf("cannot release pc proxy session")
		}
	}
	if old == config.RuntimeGW {
		snap, readErr := shared.ReadSnapshot(shared.DefaultSnapshotPath())
		if readErr == nil {
			if p := EffectiveProxyPolicy(); p != nil {
				for _, c := range runtimeGWCleanupCmds(snap, p) {
					if err := runOp(false, c.argv); err != nil && !c.optional {
						return err
					}
				}
			}
			if err := removeGatewayProxyArtifacts(snap); err != nil {
				return err
			}
		} else {
			for _, p := range []string{FirewallProxyNftPath(utils.GetMyNetHome()), FirewallProxySetNftPath(utils.GetMyNetHome())} {
				if _, err := os.Stat(p); err == nil {
					return readErr
				}
			}
		}
	}
	if err := config.SaveRuntimeMode(runtime.GOOS, openwrt, mode); err != nil {
		return err
	}
	if liveHookTunDevice() != "" {
		if rc := proxyApply(false); rc != 0 {
			return fmt.Errorf("runtime %s saved; proxy apply failed", mode)
		}
		if mode == config.RuntimePC && DNSHookWanted() {
			// 门禁未过只推迟 hook，不把已保存的 runtime 形态算失败。
			if err := dnsHookStartGate(); err != nil {
				fmt.Printf("DNS hook 暂不启动：%v\n", err)
			} else if rc := dnsHookStart(DNSHookListenDefault); rc != 0 {
				return fmt.Errorf("runtime pc saved; DNS hook start failed")
			}
		}
	}
	return nil
}

// 防止从 GW 切 PC 后，系统防火墙重载把已退役的分类器重新装回来。
func removeGatewayProxyArtifacts(snap *shared.SystemSnapshot) error {
	home := utils.GetMyNetHome()
	if snap.OS == "linux" && snap.Capabilities["nft"] {
		// 自有表存在才清理；规则文件交系统加载，不直接拼 nft 写命令。
		out, err := exec.Command("nft", "list", "table", FirewallNatFamily, FirewallProxyTable).CombinedOutput()
		if err != nil && !proxyFirewallMissing(string(out), err) {
			return fmt.Errorf("inspect gateway proxy table: %w: %s", err, out)
		}
		if err == nil {
			p := filepath.Join(varDir(), "proxy", "retire-gw.nft")
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				return err
			}
			if err := os.WriteFile(p, []byte("delete table "+FirewallNatFamily+" "+FirewallProxyTable+"\n"), 0600); err != nil {
				return err
			}
			defer os.Remove(p)
			if err := runOp(false, []string{"nft", "-f", p}); err != nil { // GUARD-OK L6 owns this retirement artifact; system loader applies it
				return err
			}
		}
	}
	for _, p := range []string{FirewallProxyNftPath(home), FirewallProxySetNftPath(home)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func runtimeGWCleanupCmds(snap *shared.SystemSnapshot, p *domain.ProxyPolicy) []routeCmd {
	if snap.OS != "linux" {
		return nil
	}
	fw := p.EffectiveFwmark()
	table := p.EffectiveTable()
	cmds := []routeCmd{
		{argv: []string{"ip", "rule", "del", "fwmark", fmt.Sprintf("%#x", fw), "lookup", fmt.Sprint(table)}, optional: true},
		{argv: []string{"ip", "route", "flush", "table", fmt.Sprint(table)}, optional: true},
	}
	cmds = append(cmds, DNSHijackCleanupCmds(snap, DNSHijackRuntime{})...)
	if snap.Capabilities["iptables"] && !snap.Capabilities["nft"] && p.Split != nil {
		args := []string{"iptables", "-t", "mangle", "-D", "PREROUTING"} // GUARD-OK exact owned mark rule removal; never flush shared chain
		if p.Split.MatchMode == domain.ProxyMatchInverted {
			args = append(args, "!")
		}
		args = append(args, "-m", "set", "--match-set", "mynet_proxy", "dst", "-j", "MARK", "--set-mark", fmt.Sprintf("%#x", fw))
		cmds = append(cmds, routeCmd{argv: args, optional: true})
	}
	return cmds
}
