package mynet

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// runCapture 跑命令取 stdout（不拿 stderr，避免把告警当证据）。
func runCapture(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

// cmdFirewallDoctor — N5：防火墙两层检查（云侧清单 + 机内 nft/iptables）。
//
//	mynet firewall doctor [--json] [--port N]
//
// 机内层可机测：UDP listen 端口是否有 accept 规则（nft 优先，iptables 兜底）。
// 云侧层只能出清单（安全组/Lightsail 在云 API，本机看不到）——
// 只开一侧都不通，见 packaging/linux/INSTALL.md「云主机必须两层」。
//
// 归属见 route-firewall-split.md：UDP listen 放行是 L6 + 云，不是驱动。
func cmdFirewallDoctor(args []string) int {
	asJSON := false
	portOverride := 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--port":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "firewall doctor: --port 需要端口号")
				return 2
			}
			fmt.Sscanf(args[i+1], "%d", &portOverride)
			i++
		default:
			fmt.Fprintln(os.Stderr, "用法: mynet firewall doctor [--json] [--port N]")
			return 2
		}
	}

	subj, subjErr := utils.ResolveSubject(utils.ResolveOpts{})
	rep := firewallDoctorReport{Subject: subj}
	if subjErr != nil {
		rep.Warnings = append(rep.Warnings, "subject: "+subjErr.Error())
	}

	// 监听端口：显式 > node.conf listen > 默认 9016
	port := portOverride
	if port == 0 && subj != nil && subj.ConfDir != "" {
		p, err := confListenPortE(subj.ConfDir)
		if err != nil {
			rep.Warnings = append(rep.Warnings,
				fmt.Sprintf("读 %s/node.conf: %v", subj.ConfDir, err))
		}
		port = p
	}
	if port == 0 {
		port = 9016
		rep.Warnings = append(rep.Warnings,
			fmt.Sprintf("未取到 listen 端口，按默认 %d 检查（可用 --port 覆盖）", port))
	}
	rep.UDPPort = port

	// 机内层
	rep.Machine = checkMachinePort(port)

	// 云侧层：只出清单
	rep.Cloud = cloudPortChecklist(port)

	// 防火墙持久化检查：判据必须按**本机实际的持久化面**分流。
	// 在通用 Linux 上照抄 fw4 的判据（只查 /etc/nftables.d/）会指着一个
	// 可能根本没人 include 的目录说「没问题」—— 本机实测（Ubuntu 22.04）：
	// /etc/nftables.conf 不含该目录的 include、netfilter-persistent 未装，
	// 两条路都无效，而旧判据只会打印 fw4 的文案。
	if be := currentFirewallBackend(); be == FirewallBackendFw4 {
		rep.Persistence = checkFw4Persistence(port)
	} else {
		rep.Persistence = checkPersistByBackend(port, be)
	}

	// 存活态：产物 ↔ 内核 一致性（被 reset 了要看得见）
	rep.Alive = checkRulesAlive()

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		printFirewallDoctor(rep)
	}

	// 退出码：只对「机测到未放行」「规则被 reset 了」亮红灯（route doctor 同口径）。
	// pf / 无工具属「测不了」，不算失败。
	if !rep.Machine.Accepted && (rep.Machine.Backend == "nft" || rep.Machine.Backend == "iptables") {
		return 1
	}
	if rep.Alive != nil && rep.Alive.Lost {
		return 1
	}
	return 0
}

type firewallDoctorReport struct {
	Subject     *utils.Subject   `json:"subject,omitempty"`
	UDPPort     int              `json:"udp_port"`
	Machine     machinePortCheck `json:"machine"`
	Cloud       cloudPortCheck   `json:"cloud"`
	Persistence *persistCheck    `json:"persistence,omitempty"`
	Alive       *aliveCheck      `json:"alive,omitempty"`
	Warnings    []string         `json:"warnings,omitempty"`
}

// aliveCheck 存活态 —— 你第 4 条「不追求 100% 恢复，但必须可感知」的那半。
//
// 不查「防火墙健不健康」，只查一件最要命的事：**产物还在、隧道也在跑，
// 而内核里已经没有我们那批规则了** —— 那就是被人 flush/reset 掉了，
// 网关 NAT 静默失效（表象像路由故障，根因在防火墙，最难自己发现）。
type aliveCheck struct {
	ArtifactPath   string `json:"artifact_path,omitempty"`
	ArtifactExists bool   `json:"artifact_exists"`
	RulesInKernel  bool   `json:"rules_in_kernel"`
	TunnelUp       bool   `json:"tunnel_up"`
	// Lost = 该有规则而没有（产物在 + 隧道在跑 + 内核无）。
	Lost  bool     `json:"lost"`
	Notes []string `json:"notes,omitempty"`
}

type machinePortCheck struct {
	Accepted bool     `json:"accepted"`
	Backend  string   `json:"backend"` // nft | iptables | none
	Evidence []string `json:"evidence,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

type cloudPortCheck struct {
	Verified bool     `json:"verified"` // 永远 false：本机看不到云安全组
	Items    []string `json:"items"`
}

type persistCheck struct {
	// Backend 本机实际的持久化面（fw4 / nftables-d / iptables-persistent / …）。
	// 判据按它分流 —— 在通用 Linux 上照抄 fw4 的判据会给出错误的「没问题」。
	Backend    string   `json:"backend,omitempty"`
	Fw4NftFile string   `json:"fw4_nft_file,omitempty"`
	Exists     bool     `json:"exists"`
	HasRule    bool     `json:"has_rule"`
	Notes      []string `json:"notes,omitempty"`
}

// confListenPort 从 node.conf 读 listen 端口（支持 `listen 9016` / `listen 0.0.0.0:9016`）。
func confListenPort(confDir string) int {
	p, _ := confListenPortE(confDir)
	return p
}

func confListenPortE(confDir string) (int, error) {
	b, err := os.ReadFile(filepath.Join(confDir, "node.conf"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "listen ") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return 0, fmt.Errorf("listen 行缺端口")
		}
		s := f[1]
		if i := strings.LastIndex(s, ":"); i >= 0 {
			s = s[i+1:]
		}
		var p int
		fmt.Sscanf(s, "%d", &p)
		if p == 0 {
			return 0, fmt.Errorf("listen 端口解析失败: %q", f[1])
		}
		return p, nil
	}
	return 0, fmt.Errorf("node.conf 无 listen 行")
}

// checkMachinePort 查内核里 UDP 端口是否有 accept。nft 优先（fw4/Linux 现代路径）。
func checkMachinePort(port int) machinePortCheck {
	c := machinePortCheck{Backend: "none"}
	want := fmt.Sprintf("%d", port)

	// nft：扫 inet/ip 表的 input 链
	if cmdExists("nft") {
		out, err := runCapture("nft", "list", "ruleset")
		if err == nil {
			c.Backend = "nft"
			for _, line := range strings.Split(out, "\n") {
				l := strings.ToLower(strings.TrimSpace(line))
				if strings.Contains(l, "udp") && strings.Contains(l, want) &&
					(strings.Contains(l, "accept") || strings.Contains(l, "dport "+want)) {
					c.Accepted = true
					c.Evidence = append(c.Evidence, strings.TrimSpace(line))
				}
			}
			if !c.Accepted {
				// 宽松匹配：只看 dport 行（有些规则 accept 在下一跳链）
				for _, line := range strings.Split(out, "\n") {
					l := strings.ToLower(line)
					if strings.Contains(l, "udp") && strings.Contains(l, "dport") && strings.Contains(l, want) {
						c.Accepted = true
						c.Evidence = append(c.Evidence, strings.TrimSpace(line))
						break
					}
				}
			}
			if c.Accepted {
				return c
			}
			c.Notes = append(c.Notes, "nft ruleset 无 udp dport "+want+" accept")
		}
	}

	// iptables 兜底（fw3 / 旧主机）
	if cmdExists("iptables") {
		out, err := runCapture("iptables", "-L", "INPUT", "-n", "--line-numbers")
		if err == nil {
			if c.Backend == "none" {
				c.Backend = "iptables"
			}
			for _, line := range strings.Split(out, "\n") {
				l := strings.ToLower(line)
				if strings.Contains(l, want) && strings.Contains(l, "accept") {
					c.Accepted = true
					c.Evidence = append(c.Evidence, strings.TrimSpace(line))
					return c
				}
			}
			c.Notes = append(c.Notes, "iptables INPUT 无 udp dport "+want+" accept")
		}
	}

	if c.Backend == "none" {
		if runtime.GOOS == "darwin" {
			c.Notes = append(c.Notes, "macOS 走 pf，不在 nft/iptables 机测范围（防火墙由 pf/应用层负责）")
			c.Backend = "pf"
		} else {
			c.Notes = append(c.Notes, "nft/iptables 都不可用 —— 无法机测机内层")
		}
	}
	return c
}

// cloudPortChecklist —— 云侧永远不能机测，只出清单。
func cloudPortChecklist(port int) cloudPortCheck {
	return cloudPortCheck{
		Verified: false,
		Items: []string{
			fmt.Sprintf("安全组/防火墙放行 UDP %d（入站 0.0.0.0/0 或对端网段）", port),
			fmt.Sprintf("Lightsail: aws lightsail open-instance-public-ports --port-info fromPort=%d,toPort=%d,protocol=udp", port, port),
			"阿里云/腾讯云：安全组入方向加 UDP " + fmt.Sprint(port),
			"云主机自身还有机内防火墙（ufw/firewalld/nft）——两层都要开",
		},
	}
}

// checkPersistByBackend 非 OpenWrt 的持久化判据：按 selectFirewallBackend 选出的
// 面查它的承载物，查不到就**如实说规则活不过防火墙重启**（skip ≠ pass）。
func checkPersistByBackend(port int, backend FirewallBackend) *persistCheck {
	p := &persistCheck{Backend: string(backend)}
	portStr := fmt.Sprint(port)

	switch backend {
	case FirewallBackendNftablesD:
		p.Fw4NftFile = FirewallNftDropInPath()
		if b, err := os.ReadFile(p.Fw4NftFile); err == nil {
			p.Exists = true
			p.HasRule = strings.Contains(string(b), "masquerade")
		} else {
			p.Notes = append(p.Notes,
				p.Fw4NftFile+" 不存在（nftables.service reload 后无 MYNET 规则）—— 跑 `mynet firewall sync`")
		}
	case FirewallBackendIPtablesPersistent:
		p.Fw4NftFile = "/etc/iptables/rules.v4"
		if b, err := os.ReadFile(p.Fw4NftFile); err == nil {
			p.Exists = true
			p.HasRule = strings.Contains(string(b), "MASQUERADE")
		} else {
			p.Notes = append(p.Notes,
				p.Fw4NftFile+" 不存在（重启后无 MYNET 规则）—— 跑 `mynet firewall sync`")
		}
		p.Notes = append(p.Notes,
			"运行期规则由 apply 装、由 `mynet firewall sync` 走 netfilter-persistent save 落盘")
	case FirewallBackendUfw, FirewallBackendFirewalld:
		p.Notes = append(p.Notes,
			fmt.Sprintf("防火墙由 %s 管理，mynet 尚未接入它的持久化面 —— 规则只在运行期有效", backend))
	case FirewallBackendNone:
		p.Notes = append(p.Notes,
			"无可用的防火墙持久化面 —— 规则只在运行期有效，重启后需重新 `mynet firewall sync`")
		p.Notes = append(p.Notes,
			"可选修复：安装 iptables-persistent；或让 /etc/nftables.conf include /etc/nftables.d/ 后启用 nftables.service")
	}
	_ = portStr
	return p
}

// checkFw4Persistence —— N5 验收：fw4 reload 后规则仍在。
func checkFw4Persistence(port int) *persistCheck {
	p := &persistCheck{Backend: string(FirewallBackendFw4)}
	candidates := []string{
		"/etc/nftables.d/mynet.nft",
		"/etc/nftables.d/mynet-firewall.nft",
	}
	for _, f := range candidates {
		if b, err := os.ReadFile(f); err == nil {
			p.Fw4NftFile = f
			p.Exists = true
			if strings.Contains(string(b), fmt.Sprint(port)) {
				p.HasRule = true
			}
			break
		}
	}
	if !p.Exists {
		p.Notes = append(p.Notes,
			"/etc/nftables.d/mynet.nft 不存在 —— fw4 reload 会丢掉 nft add 的规则")
		p.Notes = append(p.Notes,
			"持久化: echo 'add rule inet fw4 input udp dport "+fmt.Sprint(port)+" accept' > /etc/nftables.d/mynet.nft")
	} else if !p.HasRule {
		p.Notes = append(p.Notes,
			fmt.Sprintf("%s 存在但未含端口 %d", p.Fw4NftFile, port))
	}
	return p
}

// checkRulesAlive 比对「产物文件」与「内核 nft 规则」。
// 判据故意窄：只认 mynet 自有表（FirewallNatFamily/Table）+ masquerade，
// 不扫别人的表 —— 别人的规则丢不丢不是我们的事，扫进来只会误报。
func checkRulesAlive() *aliveCheck {
	home := utils.GetMyNetHome()
	a := &aliveCheck{}
	for _, p := range []string{FirewallNftPath(home), FirewallIncludePath(home)} {
		if p == "" {
			continue
		}
		if fileExists(p) {
			a.ArtifactPath = p
			a.ArtifactExists = true
			break
		}
	}
	if a.ArtifactExists {
		a.Notes = append(a.Notes, "产物: "+a.ArtifactPath)
	}

	if cmdExists("nft") {
		out, err := runCapture("nft", "list", "ruleset")
		if err == nil {
			// 只认自有表，避免把 iptables-nft 的 `table ip nat` 里的别人的 masq 算成我们的
			own := strings.Contains(out, "table "+FirewallNatFamily+" "+FirewallNatTable)
			masq := own && strings.Contains(out, "masquerade")
			a.RulesInKernel = masq
			if own && !masq {
				a.Notes = append(a.Notes, "自有表在、但表里没有 masquerade 规则")
			}
		} else {
			a.Notes = append(a.Notes, "nft list ruleset 失败（无法判定内核规则）")
		}
	} else {
		a.Notes = append(a.Notes, "无 nft：内核规则查不了（不是「没问题」）")
	}

	a.TunnelUp = currentTunHealth(home).Healthy()
	// 只有「该在而不在」才算丢：产物没写过 = 从没装过（提示性），不是丢失。
	a.Lost = a.ArtifactExists && a.TunnelUp && !a.RulesInKernel
	if a.Lost {
		a.Notes = append(a.Notes, "隧道在跑却查不到规则 = 被 flush/reset 掉，且当前无自动重放（见持久化面）")
	}
	return a
}

func printFirewallDoctor(rep firewallDoctorReport) {
	fmt.Println("firewall doctor（两层检查：云侧清单 + 机内 nft/iptables）")
	if rep.Subject != nil {
		fmt.Printf("主体: conf=%s source=%s\n", rep.Subject.ConfDir, rep.Subject.Source)
	}
	fmt.Printf("UDP listen: %d\n\n", rep.UDPPort)

	fmt.Println("── 机内层 ──")
	if rep.Machine.Accepted {
		fmt.Printf("✓ 已放行（%s）\n", rep.Machine.Backend)
		for _, e := range rep.Machine.Evidence {
			fmt.Println("  ", e)
		}
	} else {
		fmt.Printf("✗ 未放行（%s）\n", rep.Machine.Backend)
		for _, n := range rep.Machine.Notes {
			fmt.Println("  ", n)
		}
		// 放行规则随 nft 产物重放（FirewallNftRules 的 input 链），不要手工往
		// 别人的 `inet filter input` 里插 —— 那是 ufw/fw4 的管辖面，reload 即丢。
		fmt.Printf("  修复: mynet firewall sync  （放行规则在产物 %s 的 input 链）\n", FirewallNftArtifactName)
	}

	fmt.Println("\n── 云侧层（需人工核对，本机看不到安全组）──")
	for _, it := range rep.Cloud.Items {
		fmt.Println("  ·", it)
	}

	if rep.Alive != nil {
		fmt.Println("\n── 规则存活态 ──")
		switch {
		case rep.Alive.Lost:
			fmt.Println("✗ 产物在、隧道在跑，但内核里没有 mynet 规则 —— 已被防火墙 reset")
			fmt.Println("  修复: mynet firewall sync")
		case !rep.Alive.ArtifactExists:
			fmt.Println("· 产物未生成（还没装过规则）: mynet firewall sync")
		case rep.Alive.RulesInKernel:
			fmt.Println("✓ 内核里有 mynet 自有表规则（与产物一致）")
		default:
			fmt.Println("· 内核里查不到规则（隧道未起或本平台无 nft），未下结论")
		}
		for _, n := range rep.Alive.Notes {
			fmt.Println("  ", n)
		}
	}

	if rep.Persistence != nil {
		title := "fw4 持久化"
		if rep.Persistence.Backend != "" && rep.Persistence.Backend != string(FirewallBackendFw4) {
			title = "防火墙持久化（" + rep.Persistence.Backend + "）"
		}
		fmt.Println("\n── " + title + " ──")
		none := rep.Persistence.Backend == string(FirewallBackendNone) ||
			rep.Persistence.Backend == string(FirewallBackendUfw) ||
			rep.Persistence.Backend == string(FirewallBackendFirewalld)
		if none {
			fmt.Println("✗ 规则活不过防火墙重启（如下）")
		} else if rep.Persistence.Exists && rep.Persistence.HasRule {
			fmt.Println("✓", rep.Persistence.Fw4NftFile, "含规则 —— 防火墙重启后仍在")
		} else if rep.Persistence.Exists {
			fmt.Println("⚠", rep.Persistence.Fw4NftFile, "存在但缺规则")
		} else {
			fmt.Println("✗", rep.Persistence.Fw4NftFile, "不存在 —— 防火墙重启会丢规则")
		}
		for _, n := range rep.Persistence.Notes {
			fmt.Println("  ", n)
		}
	}

	for _, w := range rep.Warnings {
		fmt.Println("⚠", w)
	}
}
