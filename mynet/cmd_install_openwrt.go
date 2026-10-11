//go:build linux && openwrt

package mynet

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

// OpenWrt 装配后端 —— 编译期只进 `-tags openwrt` 的产物（playbook §4.1：
// `linux && openwrt` 与 `linux && !openwrt` 互斥）。与通用 Linux 后端**不共享**：
// 目录（/etc/mynet、/sbin）、服务（procd）、防火墙（fw4/UCI zone）都不同。
// 通用 Linux 二进制在 OpenWrt 上会探测到并**拒绝装配**（cmd_install_systemd.go）。

// platformInstall 是 OpenWrt 产物的 `mynet install` 入口。
func platformInstall(prefix, home, gateways, gnbBin string, enable, dry bool) int {
	return installOpenWrt(prefix, home, gateways, gnbBin, enable, dry)
}

// platformUninstall 是 OpenWrt 产物的 `mynet uninstall` 入口。
func platformUninstall(home string, keepBin, dry bool) int {
	return uninstallOpenWrt(home, keepBin, dry)
}

func platformDescriptor() PlatformDescriptor {
	return PlatformDescriptor{
		Platform:     "openwrt",
		PkgManager:   "opkg",
		ServiceMgr:   "procd",
		Capabilities: []string{"service.procd", "install.openwrt", "apply.osroute"},
	}
}

// installOpenWrt — OpenWrt **服务安装**（E-1）。
//
// 职责边界（与系统安装包分开）：
//   - **文件落地**（二进制 / LuCI / 默认 init）归 **ipk / opkg**，本函数不 copy 二进制。
//   - **服务装配**归 `mynet install`：写 env、写 procd init、enable/start。
//
// 与 systemd 路径的差异：桌面 tarball 没有包管理器，那边 install 顺带放二进制；
// OpenWrt 有 opkg，重复 copy 只会和包管理打架（覆盖/卸载/校验）。
//
// 进程生命周期归 procd（E-1），配置见 mynet.conf 的 VPN_TYPE（gnb|wireguard）。
func installOpenWrt(prefix, home, gateways, gnbBin string, enable, dry bool) int {
	if err := assertOpenWrtInitEmbedded(); err != nil {
		fmt.Fprintln(os.Stderr, "内嵌 init 异常：", err)
		return 1
	}
	// OpenWrt 惯例：二进制在 sbin（由 ipk 装好）
	binDir := filepath.Join(prefix, "sbin")
	envPath := "/etc/mynet/mynetd.env"
	initDir := "/etc/init.d"
	mynetBin := filepath.Join(binDir, "mynet")
	mynetdBin := filepath.Join(binDir, "mynetd")

	printInstallActionsHeader("OpenWrt 服务安装", dry)
	fmt.Printf("  检查二进制已在位（ipk 负责安装）: %s, %s\n", mynetdBin, mynetBin)
	fmt.Println("  配置 mynet 网络/防火墙 zone（network.mynet + zone + forwarding）")
	fmt.Printf("  写 %s/{mynetd,mynet}（procd 服务单元）\n", initDir)
	fmt.Printf("  写 %s  (MYNET_HOME=%s, MYNET_GATEWAYS=%s, MYNET_EXECUTOR=lua)\n",
		envPath, home, gateways)
	fmt.Printf("  建 %s/{conf,var}\n", home)
	if enable {
		fmt.Println("  /etc/init.d/mynetd enable && start")
		fmt.Println("  /etc/init.d/mynet  enable")
	}
	if dry {
		fmt.Println("\n--dry-run：以上都没有执行")
		return 0
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "install 需要 root：要写 /etc/init.d 与 /etc/mynet。请 sudo mynet install …")
		return 1
	}
	if err := mustAcceptPort(DefaultLocalAPI); err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return 1
	}

	// 0) 二进制必须已由包管理装好。缺了就明确说去装 ipk，不悄悄 copy。
	for _, bin := range []string{mynetdBin, mynetBin} {
		if !fileExists(bin) {
			fmt.Fprintf(os.Stderr, "✗ 缺二进制 %s\n", bin)
			fmt.Fprintln(os.Stderr, "  文件落地归 ipk：opkg install mynet-agent（或 debug 工装）")
			return 1
		}
	}

	// 1) 先停服务再改 env/init（覆盖 init 时 procd 已加载旧脚本）
	if out, err := runInit("mynetd", "stop"); err != nil {
		fmt.Fprintf(os.Stderr, "  init stop mynetd（首次可忽略）: %v %s\n", err, strings.TrimSpace(string(out)))
	}
	if out, err := runInit("mynet", "stop"); err != nil {
		fmt.Fprintf(os.Stderr, "  init stop mynet（首次可忽略）: %v %s\n", err, strings.TrimSpace(string(out)))
	}

	// 2) home
	for _, sub := range []string{"conf", "var"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	// 2b) mynet 网络 + 防火墙 zone（服务可跑的前置）
	//   network.mynet（proto=none，device 只在有 bundle 时绑）
	//   firewall zone mynet + lan↔mynet / mynet↔wan forwarding
	// 优先委托 $MYNET_HOME/scripts/firewall.mynet install（与 LuCI 侧同一实现）；
	// 脚本不在时用 UCI 最小集兜底，避免「服务 enable 了但 zone 没建」。
	tunDev, err := setupOpenWrtNetworkZone(home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置 network/zone:", err)
		return 1
	}
	if tunDev == "" {
		fmt.Println("configured network.mynet + firewall zone mynet（未绑接口名：还没有 bundle，首次 start / 同步配置时按 node.interface 绑）")
	} else {
		fmt.Println("configured network.mynet + firewall zone mynet，device=" + tunDev)
	}

	// 3) env
	if err := writeOpenWrtEnv(envPath, home, gateways, binDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("installed", envPath)

	// 4) procd init（服务单元；文件二进制仍归 ipk）
	for _, name := range OpenWrtInitNames {
		body, ok := openWrtInit(name)
		if !ok {
			fmt.Fprintf(os.Stderr, "init %s 未嵌入\n", name)
			return 1
		}
		dst := filepath.Join(initDir, name)
		if err := os.WriteFile(dst, []byte(body), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "install %s: %v\n", dst, err)
			return 1
		}
		fmt.Println("installed", dst)
	}

	// 5) enable
	if enable {
		if _, err := runInit("mynetd", "enable"); err != nil {
			fmt.Fprintf(os.Stderr, "init enable mynetd: %v\n", err)
			return 1
		}
		if _, err := runInit("mynetd", "start"); err != nil {
			fmt.Fprintf(os.Stderr, "init start mynetd: %v\n", err)
			return 1
		}
		fmt.Println("enabled + started: mynetd")
		// gnb 服务只 enable，不强制 start：节点身份/配置可能还没就绪
		if _, err := runInit("mynet", "enable"); err != nil {
			fmt.Fprintf(os.Stderr, "init enable mynet: %v\n", err)
			return 1
		}
		fmt.Println("enabled: mynet（gnb 待配置后 /etc/init.d/mynet start）")
	}

	// gnb 就位检查（**OpenWrt 方言**）：gnb 由 ipk 落在 $home/driver/gnb/bin（与 init 的
	// 路径解析同序），**不在** /usr/local/bin —— 拿别平台的默认位检查会把「装得好好的」
	// 误报成「服务会起不来」（.182 与 arm64 测试 VM 上每次 install/opkg 升级都误报）。
	if !gnbPresentOpenWrt(home) {
		fmt.Fprintf(os.Stderr, "⚠️ 未找到 gnb（%s 与 PATH 都没有）—— mynet 服务会起不来。请装 gnb 的 ipk。\n",
			filepath.Join(home, "driver", "gnb", "bin", "gnb"))
	}

	fmt.Println("\n下一步（节点身份，install 不代劳）：")
	fmt.Printf("  1) %s setup --local --node-id <ID>   # 或 --online\n", mynetBin)
	fmt.Println("  2) 同步配置（bundle / LuCI / mynet refresh-config）")
	fmt.Println("  3) /etc/init.d/mynet start            # procd 拉起 gnb + 装路由")
	return 0
}

// gnbPresentOpenWrt gnb 是否就位：与 init 的解析同序 —— $home/driver/gnb/bin/gnb → PATH。
func gnbPresentOpenWrt(home string) bool {
	if fileExists(filepath.Join(home, "driver", "gnb", "bin", "gnb")) {
		return true
	}
	_, err := exec.LookPath("gnb")
	return err == nil
}

func writeOpenWrtEnv(path, home, gateways, binDir string) error {
	body := "# 由 mynet install 生成（OpenWrt）。改配置请改这里\n" +
		"MYNET_HOME=" + home + "\n" +
		"MYNET_GATEWAYS=" + gateways + "\n" +
		"MYNET_BIN=" + filepath.Join(binDir, "mynet") + "\n" +
		"MYNET_EXECUTOR=lua\n" +
		"PATH=" + binDir + ":/usr/sbin:/usr/bin:/sbin:/bin\n"
	return os.WriteFile(path, []byte(body), 0o600)
}

// setupOpenWrtNetworkZone — 服务前置：mynet 网络接口 + 防火墙 zone。
//
// 装服务不等于服务能跑：没有 network.mynet / zone / forwarding，
// 隧道起来了也转发不通。优先走 firewall.mynet install（与 LuCI 同一实现，
// 已处理 include/NAT 边角）；脚本缺失时用 UCI 最小集。
//
// 返回值是**实际绑定的接口名**（"" = 本次没绑，交由首次 start / 配置同步补上），
// 调用方要把它说出来 —— 「装了但没绑接口」和「装好」不能长得一样。
func setupOpenWrtNetworkZone(home string) (string, error) {
	script := filepath.Join(home, "scripts", "firewall.mynet")
	if fileExists(script) {
		cmd := exec.Command("sh", script, "install")
		cmd.Env = append(os.Environ(), "MYNET_HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("firewall.mynet install: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return "", nil
	}

	// UCI 最小集：network.mynet + zone mynet + lan↔mynet / mynet↔wan
	dev, err := installTunDevice(home)
	if err != nil {
		return "", fmt.Errorf("接口名不可用: %w", err)
	}
	for _, c := range openWrtUciSteps(dev) {
		out, err := exec.Command(c[0], c[1:]...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("%v: %w (%s)", c, err, strings.TrimSpace(string(out)))
		}
	}

	return dev, nil
}

// installTunDevice 取安装期**唯一**能拿到的接口名权威：$MYNET_HOME/var/bundle.json
// 的 node.interface（同一解析口 domain.BundleEnvelope.TunDevice）。
//
// 没 bundle（首次安装、还没同步过配置）返回 ("", nil) —— 调用方必须**跳过**
// device 写入；bundle 在但名字解析不出来返回 error，因为接口名是阻断性配置：
// 猜出来的名字在单 zone 机器上能用，在现网 182 这类机器上恰好是**另一个 zone 的
// 接口**，而 `uci set network.mynet.device=X` / 防火墙按 X 匹配都不报错
// —— 宁可安装失败、错误可见，也不写一份会把流量搬走的绑定。
// 绑定会由首次 `init start`（firewall.mynet start --interface）或
// LuCI 的 config.lua / node.lua 按实际接口名补上。
func installTunDevice(home string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(home, "var", "bundle.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	env, err := domain.DecodeBundleEnvelope(raw)
	if err != nil {
		return "", err
	}

	return env.TunDevice()
}

// openWrtUciSteps 生成 UCI 兜底命令序列，dev == "" 时**不含任何 device 写入**。
//
// 幂等契约（G-11）：首装成功建 zone + 两条 forwarding；重复执行不新增任何 section。
//
// dev 直接拼进 `sh -c` 的那段，安全性来自 domain.ValidateTunDevice：字符集
// 限定 `[A-Za-z0-9._-]{1,15}`，不含空格/引号/分号。别在这里再放宽。
//
// section 名一律用 awk -F"[.=]" 取第 2 字段 —— `uci show` 的 section 行形如
// `firewall.cfg000009=forwarding`，用 `-F.` 会把 `=类型` 粘进字段 2，
// 拿到伪 id 后 `uci get firewall.<id>.src` 全失败，幂等判据失效。
const uciLoopForwarding = `for x in $(uci show firewall | awk -F"[.=]" '/^firewall\..*=forwarding$/ {print $2}'); do `

// openWrtZoneLookup 幂等找 zone：已有 `name='mynet'` 的 section 复用，否则新建并
// **捕获** `uci add firewall zone` 打印的 section 名（旧实现把它丢进 /dev/null、
// 再按 name 反查，而新 section 此时还没有 name → 首装必 exit 1）。
const openWrtZoneLookup = `s=$(uci show firewall | awk -F"[.=]" '/^firewall\..*\.name=.mynet.$/ {print $2; exit}'); [ -n "$s" ] || s=$(uci add firewall zone); `

func openWrtZoneStep(dev string) string {
	s := openWrtZoneLookup +
		`uci set firewall.$s.name=mynet; uci set firewall.$s.input=ACCEPT; ` +
		`uci set firewall.$s.output=ACCEPT; uci set firewall.$s.forward=ACCEPT; uci set firewall.$s.masq=1`
	if dev != "" {
		s += `; uci set firewall.$s.device=` + dev
	}

	return s
}

// openWrtForwardingStep 幂等建 src→dest forwarding。
// 判据不能写成 `grep "src='lan'.*dest='mynet'"`：`uci show` 每条 option 单独一行，
// 单行里永远不会同时出现 src 与 dest → 永不命中、重复添加。改为逐 section 读 src/dest 比对。
// src/dest 是调用方传入的常量（lan / mynet / wan）。
func openWrtForwardingStep(src, dest string) string {
	return `f(){ ` + uciLoopForwarding +
		`[ "$(uci get firewall.$x.src 2>/dev/null)" = "` + src + `" ] && [ "$(uci get firewall.$x.dest 2>/dev/null)" = "` + dest + `" ] && return 0; ` +
		`done; return 1; }; f ` + src + ` ` + dest + ` || { s=$(uci add firewall forwarding); ` +
		`uci set firewall.$s.src=` + src + `; uci set firewall.$s.dest=` + dest + `; }`
}

func openWrtUciSteps(dev string) [][]string {
	steps := [][]string{
		{"uci", "set", "network.mynet=interface"},
		{"uci", "set", "network.mynet.proto=none"},
	}
	if dev != "" {
		steps = append(steps, []string{"uci", "set", "network.mynet.device=" + dev})
	}

	return append(steps,
		[]string{"sh", "-c", openWrtZoneStep(dev)},
		[]string{"sh", "-c", openWrtForwardingStep("lan", "mynet")},
		[]string{"sh", "-c", openWrtForwardingStep("mynet", "wan")},
		[]string{"uci", "commit", "network"},
		[]string{"uci", "commit", "firewall"},
	)
}

func runInit(name, action string) ([]byte, error) {
	return exec.Command("/etc/init.d/"+name, action).CombinedOutput()
}

// uninstallOpenWrt 停用并移除自有 procd init 登记与 env；不擦 LAN/WAN
// （network.mynet 与 firewall zone 的删除归 LuCI / firewall.mynet）。
//
// 被两处复用：OpenWrt 专用产物（cmd_install_openwrt_only.go）与通用 Linux 包在
// OpenWrt 上的 legacy 卸载（cmd_install_systemd.go 的 isOpenWrt 分支）—— 修 G-15，
// 避免通用包在 OpenWrt 上卸载时走 systemd 路径、遗留 init/zone。
func uninstallOpenWrt(home string, keepBin, dry bool) int {
	printInstallActionsHeader("OpenWrt 卸载", dry)
	fmt.Println("  /etc/init.d/mynetd stop && disable")
	fmt.Println("  /etc/init.d/mynet  stop && disable")
	for _, name := range OpenWrtInitNames {
		fmt.Printf("  删 /etc/init.d/%s\n", name)
	}
	fmt.Println("  删 /etc/mynet/mynetd.env")
	fmt.Println("  （network.mynet / firewall zone 不在此删：归 LuCI 或 firewall.mynet uninstall）")
	fmt.Printf("  保留 %s/{conf,var}（节点身份与密钥）\n", home)
	if dry {
		fmt.Println("\n--dry-run：以上都没有执行")
		return 0
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "uninstall 需要 root。请 sudo mynet uninstall …")
		return 1
	}

	for _, name := range OpenWrtInitNames {
		if out, err := runInit(name, "stop"); err != nil {
			fmt.Fprintf(os.Stderr, "  init stop %s（首次可忽略）: %v %s\n", name, err, string(out))
		}
		if out, err := runInit(name, "disable"); err != nil {
			fmt.Fprintf(os.Stderr, "  init disable %s: %v %s\n", name, err, string(out))
		}
		p := filepath.Join("/etc/init.d", name)
		if err := os.Remove(p); err == nil {
			fmt.Println("removed", p)
		}
	}
	if err := os.Remove("/etc/mynet/mynetd.env"); err == nil {
		fmt.Println("removed /etc/mynet/mynetd.env")
	}

	removeOwnBinaries(home, keepBin)
	uninstallTail(home)
	return 0
}
