package utils

// 服务登记标识 —— 生命周期合同（lifecycle-contract §3 服务名矩阵）的共享 SSOT。
//
// ⚠️ **install 注册什么，托盘/控制器就启停什么**。此前两侧各写各的字面量：
// 托盘 macOS 打 `club.mynet.mynetd`（install 注册的是 `com.mynet.mynetd`）、
// Windows 托盘打 schtasks `MyNetd`（install 已改 SCM `mynetd`）——
// 启停目标指向 install 从不注册的标识，按钮等于摆设（C1-S3 / CPI-73·74）。
//
// 收敛到 shared：mynet install（执行器）与 cmd/mynet_tray（桌面壳）共用
// 同一组常量，改名时全仓一起动；fixture 锁住「常量 = 合同矩阵值」。
const (
	// ServiceNameMynetd mynetd 的登记名：systemd unit / Windows SCM 服务 / OpenWrt init。
	ServiceNameMynetd = "mynetd"
	// ServiceNameGNB gnb 的 systemd 单元名（OpenWrt 由 procd 统管，不单列）。
	ServiceNameGNB = "mynet_gnb"

	// LaunchdLabelMynetd macOS LaunchDaemon label（install 唯一注册名）。
	LaunchdLabelMynetd = "com.mynet.mynetd"
	// LaunchdLabelGNB 旧版 gnb LaunchDaemon label —— 只在卸载时清理，不再写入。
	LaunchdLabelGNB = "com.mynet.gnb"
	// LaunchdLabelTray 托盘 LaunchAgent label（托盘自启与 install 同名，防双托盘）。
	LaunchdLabelTray = "club.mynet.tray"

	// LaunchAgentDirSystem 安装器代写托盘 LaunchAgent 的目录：全体登录用户，
	// launchd 每次登录自动加载（装完即有托盘）。托盘自己**只能写用户级**
	// ~/Library/LaunchAgents，所以判「是否已自启」必须两处都看 ——
	// 只看用户级会让装完的菜单一直显示未勾选（D10）。
	LaunchAgentDirSystem = "/Library/LaunchAgents"

	// TrayRunValueName Windows Run 键下的托盘值名（安装器与托盘同名，防双托盘）。
	TrayRunValueName = "MyNetTray"
	// TrayRunKeySystem 安装器写的 HKLM Run：每个用户登录都拉，语义对齐
	// LaunchAgentDirSystem。托盘只能读写 HKCU 那份 ⇒ 同样必须两处都看，
	// 否则启用会产生跨 hive 双份（双托盘）、停用只删 HKCU（用户永远关不掉）（D11）。
	TrayRunKeySystem = `HKLM\Software\Microsoft\Windows\CurrentVersion\Run`
	// TrayRunKeyUser 托盘自己可读写的 HKCU Run。
	TrayRunKeyUser = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
)
