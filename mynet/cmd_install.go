package mynet

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// printInstallActionsHeader 打印「安装/卸载动作清单」的表头。
//
// 只有 --dry-run 才说「只看不做」。真实执行时若照抄这句，日志里会同时出现
// 「--dry-run 只看不做」和随后的 installed/enabled/started —— 排障的人据此
// 误判「只干跑没落地」（2026-10-11 .182 升级的 opkg 日志实测踩过）。
func printInstallActionsHeader(ctx string, dry bool) {
	switch {
	case dry && ctx != "":
		fmt.Printf("将执行以下动作（%s，--dry-run 只看不做）：\n", ctx)
	case dry:
		fmt.Println("将执行以下动作（--dry-run 只看不做）：")
	case ctx != "":
		fmt.Printf("执行以下动作（%s）：\n", ctx)
	default:
		fmt.Println("执行以下动作：")
	}
}

// cmdInstall — **服务安装**（不是安装包安装）。
//
//	mynet install [--prefix DIR] [--home DIR] [--gateways URL[,URL]]
//	               [--gnb-bin PATH] [--no-enable] [--dry-run]
//
// 职责边界（全平台一致）：
//   - **文件落地**（mynet/mynetd/gnb 二进制、LuCI、默认单元）归**安装包**
//     （OpenWrt ipk · deb/rpm · macOS pkg · Windows MSI · dist 包解压）。
//   - **服务装配**归 `mynet install`：写 env、写服务单元、enable/start。
//
// 本文件是**共享入口**：解析参数、做与平台无关的校验，然后委派给平台实现
// `platformInstall`（按 build tag 编译期互斥，见 cmd_install_{systemd,launchd,win,openwrt_only}.go）。
//
// 为什么不 copy 二进制：包管理器负责放置/校验/卸载；install 再 copy 一份会
// 和包打架（覆盖、双份、卸载残留）。缺二进制时明确报错，让人去装包。
func cmdInstall(args []string) int {
	prefix := defaultPrefix()
	home := ""
	gateways := ""
	gnbBin := "/usr/local/bin/gnb"
	enable := true
	dry := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--prefix":
			if i+1 >= len(args) {
				return usageInstall("--prefix 需要目录")
			}
			i++
			prefix = args[i]
		case "--home":
			if i+1 >= len(args) {
				return usageInstall("--home 需要目录")
			}
			i++
			home = args[i]
		case "--gateways":
			if i+1 >= len(args) {
				return usageInstall("--gateways 需要 URL")
			}
			i++
			gateways = args[i]
		case "--gnb-bin":
			if i+1 >= len(args) {
				return usageInstall("--gnb-bin 需要路径")
			}
			i++
			gnbBin = args[i]
		case "--no-enable":
			enable = false
		case "--dry-run":
			dry = true
		default:
			return usageInstall("未知参数 " + args[i])
		}
	}
	// 先解析唯一 home（--home > MYNET_HOME），再校验 gateway。
	// 不再"调用前先 requireHome 而忽略 --home"（G-1）：显式给了路径就能装。
	resolved, code := resolveInstallHome(home)
	if code != 0 {
		return code
	}
	if gateways == "" {
		// 节点模式必须知道 gateway 在哪，否则 mynetd 起来是「纯本机模式」
		// （无 WS、无节点身份、不碰驱动），装了等于没装。
		return usageInstall("需要 --gateways wss://…（节点模式的 WS 入口；不给则 mynetd 只有 localapi，隧道起不来）")
	}
	code = platformInstall(prefix, resolved, gateways, gnbBin, enable, dry)
	if code == 0 {
		// O4：把旧 Lua 权威 proxy_role.conf 折叠进统一 Local 意图（幂等）。
		// 失败只告警：迁移是过渡桥，不该挡住安装本身；非旧部署时是 no-op。
		reportLegacyProxyMigration(resolved, dry)
		// O1/O8：把旧 Lua 配置采纳进 Go 配置档（幂等、只读旧文件）。
		// 密钥已按节点身份解耦（ResolveKeysDir 跨档回退 conf/local），切 online 不再丢密钥。
		reportLegacyConfigAdoption(resolved, dry)
	}
	return code
}

// reportLegacyConfigAdoption 跑一次旧配置采纳并报告（best-effort）。
func reportLegacyConfigAdoption(home string, dry bool) {
	res, err := AdoptLegacyConfig(home, dry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ 旧 Lua 配置未采纳（不猜，留待人工）: %v\n", err)
		return
	}
	if res.Adopted {
		fmt.Println("配置采纳:", res.Reason)
	}
}

// reportLegacyProxyMigration 跑一次旧 proxy conf 迁移并报告（best-effort）。
func reportLegacyProxyMigration(home string, dry bool) {
	res, err := MigrateLegacyProxyConf(home, dry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ 旧 proxy conf 迁移未做（不猜，留待人工处理）: %v\n", err)
		return
	}
	if res.Migrated {
		fmt.Println("proxy 意图迁移:", res.Reason)
	}
}

// resolveInstallHome 解析 install/uninstall 的**唯一** home。
//
// 口径（lifecycle-contract §2.1）：
//   - 优先级：--home > MYNET_HOME > 平台默认（PlatformDefaultHome）；
//   - 两者都给且不同 → 拒绝（私有密钥/配置落在哪不能有歧义）。
//   - 不回退 prefix/exe/cwd。
func resolveInstallHome(explicit string) (string, int) {
	env := os.Getenv("MYNET_HOME")
	switch {
	case explicit != "" && env != "":
		if filepath.Clean(explicit) != filepath.Clean(env) {
			fmt.Fprintf(os.Stderr, "✗ --home(%s) 与 MYNET_HOME(%s) 不一致：装/卸前请只给一个，避免写错配置树\n", explicit, env)
			return "", 1
		}
		if !filepath.IsAbs(explicit) {
			fmt.Fprintf(os.Stderr, "✗ home 必须是绝对路径，得到 %q\n", explicit)
			return "", 1
		}
		return filepath.Clean(explicit), 0
	case explicit != "":
		if !filepath.IsAbs(explicit) {
			fmt.Fprintf(os.Stderr, "✗ --home 必须是绝对路径，得到 %q\n", explicit)
			return "", 1
		}
		return filepath.Clean(explicit), 0
	case env != "":
		h, err := utils.RequireMyNetHome()
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			return "", 1
		}
		return h, 0
	default:
		// 平台默认（unix=/opt/mynet，OpenWrt=/etc/mynet，Windows=ProgramData\MyNet）
		h := utils.PlatformDefaultHome()
		if h == "" || !filepath.IsAbs(h) {
			fmt.Fprintln(os.Stderr, "✗ 无法解析 home：请用 --home DIR 或 export MYNET_HOME=…")
			return "", 1
		}
		fmt.Printf("  home 未显式指定，使用平台默认 %s\n", h)
		return h, 0
	}
}

func usageInstall(msg string) int {
	fmt.Fprintln(os.Stderr, "用法: mynet install --gateways wss://… [--prefix DIR] [--home DIR] [--gnb-bin PATH] [--no-enable] [--dry-run]")
	if msg != "" {
		fmt.Fprintln(os.Stderr, "     ", msg)
	}
	return 2
}

// cmdUninstall — 停服务并移除安装件。
//
//	mynet uninstall [--home DIR] [--keep-bin] [--dry-run]
//
// ⚠️ 不动 $home 下的**节点身份与数据**（conf/ 含节点密钥与驱动 conf ——
// D4-E 起密钥在 conf/<form>/<nodeId>/security|wg.conf，var/bundle.json 是配置态）。
// 那些是业务数据，卸载程序不该替人决定丢掉；要清得显式删目录。
func cmdUninstall(args []string) int {
	home := ""
	keepBin := false
	dry := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--home":
			if i+1 >= len(args) {
				return usageInstall("--home 需要目录")
			}
			i++
			home = args[i]
		case "--keep-bin":
			keepBin = true
		case "--dry-run":
			dry = true
		default:
			return usageInstall("未知参数 " + args[i])
		}
	}
	// 与 install 同一口径解析 home（--home > MYNET_HOME），不再静默回退 prefix。
	resolved, code := resolveInstallHome(home)
	if code != 0 {
		return code
	}
	return platformUninstall(resolved, keepBin, dry)
}

// defaultPrefix 见 prefix_{unix,windows}.go。

// fileExists 存在性判断（比到处写 os.Stat 两次可读）。
func fileExists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}

// clearDownloadMarks 清除二进制的下载来源标记，返回警告（不中止安装）：
//   - darwin：com.apple.quarantine xattr —— 不清 launchd 起不来、报错还不直观
//   - windows：Zone.Identifier ADS（MOTW）—— 不清 SCM/Defender 可能拦
//   - 其它平台：无对应概念，no-op
func clearDownloadMarks(paths []string) []string {
	var warnings []string
	for _, p := range paths {
		switch runtime.GOOS {
		case "darwin":
			if out, err := exec.Command("xattr", "-p", "com.apple.quarantine", p).Output(); err == nil && len(out) > 0 {
				if out2, err2 := exec.Command("xattr", "-d", "com.apple.quarantine", p).CombinedOutput(); err2 != nil {
					warnings = append(warnings, fmt.Sprintf("清除 quarantine 失败 %s: %v %s", p, err2, strings.TrimSpace(string(out2))))
				} else {
					fmt.Println("已清除 com.apple.quarantine:", p)
				}
			}
		case "windows":
			zone := p + ":Zone.Identifier"
			if _, err := os.Stat(zone); err == nil {
				if err := os.Remove(zone); err != nil {
					warnings = append(warnings, fmt.Sprintf("清除 Zone.Identifier 失败 %s: %v", p, err))
				} else {
					fmt.Println("已清除 Zone.Identifier (MOTW):", p)
				}
			}
		}
	}
	return warnings
}

// writeEnvFile 写 mynetd.env（0600 —— 它指向的 MYNET_HOME 里含节点私钥）。
// 各平台的落盘路径不同（systemd: /etc/mynet/mynetd.env；windows: $home\mynetd.env）。
func writeEnvFile(path, home, gateways, binDir string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := "# 由 mynet install 生成。改配置请改这里，不要改单元\n" +
		"MYNET_HOME=" + home + "\n" +
		"MYNET_GATEWAYS=" + gateways + "\n" +
		"MYNET_BIN=" + filepath.Join(binDir, "mynet") + "\n"

	return os.WriteFile(path, []byte(body), 0o600)
}

// writeGatewaysFile 写 `$HOME/conf/gateways.json` —— 网关列表的**配置面**
// （原 mynetd.env 的 MYNET_GATEWAYS，2026-10-12 裁定归 conf/）。
//
// 为什么不是 env 文件：env 只留给「服务单元必须在进程起来前给出」的 MYNET_HOME；
// gateways 是进程自己读得到的，属 conf/（spec/paths.md §2.1）。
func writeGatewaysFile(home, gateways string) error {
	confDir := filepath.Join(home, "conf")
	if err := os.MkdirAll(confDir, 0o700); err != nil {
		return err
	}
	list := []string{}
	for _, g := range strings.Split(gateways, ",") {
		if g = strings.TrimSpace(g); g != "" {
			list = append(list, g)
		}
	}
	body, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(confDir, "gateways.json"), append(body, '\n'), 0o644)
}

func copyFile(src, dst string, perm os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	return os.WriteFile(dst, b, perm)
}

// gnbSuite 出厂四件套（与 mynet_gnb BASELINE 的 PROGRAMS 一致）。
var gnbSuite = []string{"gnb", "gnb_ctl", "gnb_es", "gnb_crypto"}

// provisionDriverGnbBin 把 gnb 四件套落进 <prefix>/bin「标准位」（D4-E：driver/ 安装层
// 退役，gnb 与 agent 同装 prefix 指定目录；旧 $MYNET_HOME/driver/gnb/bin 只作查找回退）。
//
// 源优先级（同一次 install 只认第一个找到的位，保证四件套同树 —— R2）：
//  1. $prefix/driver/gnb/bin     —— 旧 pkg 落地位（darwin pkg / dist 布局，迁移期）
//  2. /usr/local/bin             —— deb/rpm 常规位
//  3. dirname(--gnb-bin)         —— 自定义位
//
// 内容 sha256 一致就跳过（幂等；包升级后重跑 install 会跟着刷新），
// 不一致才覆盖。返回 copied（已写标准位）与 missing（哪都没找到/写失败）。
func provisionDriverGnbBin(prefix, home, gnbBin string) (copied, missing []string) {
	dstDir := filepath.Join(prefix, "bin")
	srcDirs := []string{
		filepath.Join(home, "driver", "gnb", "bin"),
		filepath.Join(prefix, "driver", "gnb", "bin"),
		"/usr/local/bin",
		filepath.Dir(gnbBin),
	}
	for _, prog := range gnbSuite {
		src := ""
		for _, d := range srcDirs {
			p := filepath.Join(d, prog)
			if fileExists(p) {
				src = p
				break
			}
		}
		if src == "" {
			missing = append(missing, prog)
			continue
		}
		dst := filepath.Join(dstDir, prog)
		if sameFile(src, dst) {
			continue
		}
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			missing = append(missing, prog+" (建目录失败: "+err.Error()+")")
			continue
		}
		if err := copyFile(src, dst, 0o755); err != nil {
			missing = append(missing, prog+" (复制失败: "+err.Error()+")")
			continue
		}
		copied = append(copied, dst)
	}
	return copied, missing
}

// sameFile src 与 dst 内容一致（dst 不存在/读失败都算不一致）。
func sameFile(src, dst string) bool {
	a, err := hashFile(src)
	if err != nil {
		return false
	}
	b, err := hashFile(dst)
	if err != nil {
		return false
	}
	return a == b
}

func hashFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return string(sum[:]), nil
}

// ---- 平台安装共享步骤（systemd / launchd 复用；openwrt 单独口径）----

// runInstallDetector 装完文件的第一件事：写 var/system.json（L6 素材）。
// 不跑则 apply --auto 读不到 system.json，路由装不全。
func runInstallDetector(binDir, home string) {
	det := filepath.Join(binDir, "mynet")
	cmd := exec.Command(det, "detector")
	cmd.Env = append(os.Environ(), "MYNET_HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ detector 失败（install 继续，但 apply 会缺 system.json）: %v %s\n",
			err, strings.TrimSpace(string(out)))
	} else {
		fmt.Println("detector: 已写", filepath.Join(home, "var", "system.json"))
	}
}

// provisionProxyIfStamped 出厂补丁栈声明含 p2-route-proxy → 置备 proxy 插件包，
// 装完即「已安装」（是否启用由用户决定）。老出厂（无 P2）不置备，不误报。
func provisionProxyIfStamped(home string) {
	if !strings.Contains(utils.GnbPatchStack, "p2-route-proxy") {
		return
	}
	created, err := provisionProxyPlugin(home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ proxy 插件置备失败: %v\n", err)
		return
	}
	if created {
		fmt.Println("proxy 插件已置备（出厂补丁栈 p2-route-proxy）")
	}
}

// provisionDriverAndReport 置备 gnb 四件套到标准位并报告结果。
func provisionDriverAndReport(prefix, home, gnbBin string) {
	copied, gone := provisionDriverGnbBin(prefix, home, gnbBin)
	for _, c := range copied {
		fmt.Println("gnb 标准位已置备:", c)
	}
	for _, g := range gone {
		fmt.Fprintf(os.Stderr, "⚠ 标准位缺 %s（包内位//usr/local/bin/gnbBin 同目录都没有）——"+
			"gnb_ctl 计数等同目录探测会跳过它\n", g)
	}
}

// warnGnbMissing gnb 不在位时警告（tail 是平台特定的后果说明）。
func warnGnbMissing(gnbBin, tail string) {
	if _, err := os.Stat(gnbBin); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ %s 不存在 —— %s\n", gnbBin, tail)
	}
}

// printInstallNextSteps 打印安装后的节点身份引导（全平台同一口径）。
func printInstallNextSteps(binDir string, darwin bool) {
	fmt.Println("\n下一步（节点身份，install 不代劳）：")
	fmt.Printf("  1) sudo %s auth login --email … --password-stdin\n", filepath.Join(binDir, "mynet"))
	fmt.Printf("  2) sudo %s setup --online --tenant T --node-id ID   # 不生成密钥\n", filepath.Join(binDir, "mynet"))
	fmt.Println("     密钥：mynet config keys set（导入）或 keys generate（新建）")
	fmt.Println("  3) mynet config keys sync && mynet config pull")
	fmt.Println("  4) mynet apply --active --auto && mynet service start gnb")
	fmt.Println("  （detector 已在 install 时跑过；system.json 就绪）")
	if darwin {
		fmt.Println("  图形化路径：菜单栏托盘 → 打开控制台；未自动弹出时浏览器访问 http://127.0.0.1:8096/ui/")
	}
}

// removeOwnBinaries 删自身程序文件（--keep-bin 时保留）。包文件归包管理器，
// 这里只删 mynet install 视角下的自身二进制。
func removeOwnBinaries(home string, keepBin bool) {
	if keepBin {
		return
	}
	for _, b := range []string{"mynet", "mynetd"} {
		p := filepath.Join(home, "bin", utils.ExeName(b))
		if err := os.Remove(p); err == nil {
			fmt.Println("removed", p)
		}
	}
}

// uninstallTail 卸载收尾提示（保留节点身份与密钥的口径，全平台一致）。
func uninstallTail(home string) {
	fmt.Printf("\n已卸载。节点身份与密钥仍保留在 %s —— 如需彻底清除请自行 rm 该目录（含节点私钥）。\n", home)
}
