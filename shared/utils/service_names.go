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
)
