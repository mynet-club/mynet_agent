package mynet

// PlatformDescriptor 描述**本产物**的平台事实（playbook §4.1）：发行平台、
// 包管理方式、服务 manager 与已编译能力。它与运行期探测互补 ——
// 条件编译决定「有哪些平台实现」，探测决定「当前系统具有哪些能力」。
type PlatformDescriptor struct {
	Platform     string   // linux | openwrt | darwin | windows | unsupported
	PkgManager   string   // apt/rpm | opkg | pkg | nsis/msi
	ServiceMgr   string   // systemd | procd | launchd | scm
	Capabilities []string // 本产物已编译的能力标识
}
