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

// isOpenWrt — 运行期判定 OpenWrt（不依赖 runtime.GOOS：OpenWrt 也是 linux）。
func isOpenWrt() bool {
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		return true
	}
	// 兜底：ubus / fw4 存在且无 systemd（ImmortalWrt 同口径）
	if _, err := os.Stat("/sbin/procd"); err == nil {
		if _, err := os.Stat("/etc/systemd/system"); err != nil {
			return true
		}
	}
	return false
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
	if err := assertUnitsEmbedded(); err != nil {
		fmt.Fprintln(os.Stderr, "内嵌 init 异常：", err)
		return 1
	}
	// OpenWrt 惯例：二进制在 sbin（由 ipk 装好）
	binDir := filepath.Join(prefix, "sbin")
	envPath := "/etc/mynet/mynetd.env"
	initDir := "/etc/init.d"
	mynetBin := filepath.Join(binDir, "mynet")
	mynetdBin := filepath.Join(binDir, "mynetd")

	fmt.Println("将执行以下动作（OpenWrt 服务安装，--dry-run 只看不做）：")
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

	if _, err := os.Stat(gnbBin); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ %s 不存在 —— mynet 服务会起不来。请装 gnb 或 --gnb-bin 指定。\n", gnbBin)
	}

	fmt.Println("\n下一步（节点身份，install 不代劳）：")
	fmt.Printf("  1) %s setup --local --node-id <ID>   # 或 --online\n", mynetBin)
	fmt.Println("  2) 同步配置（bundle / LuCI / mynet refresh-config）")
	fmt.Println("  3) /etc/init.d/mynet start            # procd 拉起 gnb + 装路由")
	return 0
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
// dev 直接拼进 `sh -c` 的那段，安全性来自 domain.ValidateTunDevice：字符集
// 限定 `[A-Za-z0-9._-]{1,15}`，不含空格/引号/分号。别在这里再放宽。
func openWrtUciSteps(dev string) [][]string {
	zoneBody := `s=$(uci show firewall | grep "name='mynet'" | cut -d. -f2 | cut -d= -f1 | head -1); \
		[ -n "$s" ] || exit 1; \
		uci set firewall.$s.name=mynet; \
		uci set firewall.$s.input=ACCEPT; \
		uci set firewall.$s.output=ACCEPT; \
		uci set firewall.$s.forward=ACCEPT; \
		uci set firewall.$s.masq=1`
	if dev != "" {
		zoneBody += `; \
		uci set firewall.$s.device=` + dev
	}

	steps := [][]string{
		{"uci", "set", "network.mynet=interface"},
		{"uci", "set", "network.mynet.proto=none"},
	}
	if dev != "" {
		steps = append(steps, []string{"uci", "set", "network.mynet.device=" + dev})
	}

	return append(steps,
		// zone 已存在则跳过创建（uci add 会重复）
		[]string{"sh", "-c", `uci show firewall | grep -q "name='mynet'" || uci add firewall zone >/dev/null`},
		[]string{"sh", "-c", zoneBody},
		[]string{"sh", "-c", `uci show firewall | grep -q "src='lan'.*dest='mynet'" || { \
			s=$(uci add firewall forwarding); \
			uci set firewall.$s.src=lan; \
			uci set firewall.$s.dest=mynet; }`},
		[]string{"sh", "-c", `uci show firewall | grep -q "src='mynet'.*dest='wan'" || { \
			s=$(uci add firewall forwarding); \
			uci set firewall.$s.src=mynet; \
			uci set firewall.$s.dest=wan; }`},
		[]string{"uci", "commit", "network"},
		[]string{"uci", "commit", "firewall"},
	)
}

func runInit(name, action string) ([]byte, error) {
	return exec.Command("/etc/init.d/"+name, action).CombinedOutput()
}
