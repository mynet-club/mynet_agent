//go:build windows

package localapi

// gnbExtraDirs Windows 的 gnb 常见安装位。
// 标准位是 MYNET_HOME\driver\gnb\bin（与 conf 投影同树）；其余是迁移期脚印。
func gnbExtraDirs() []string {
	return []string{
		`C:\Program Files\MyNet\driver\gnb\bin`,
		`C:\Program Files\MyNet\bin`,
		`C:\mynet\driver\gnb\bin`,
		`C:\mynet\bin`,
	}
}

// gnbExeName Windows 可执行文件带 .exe。
func gnbExeName() string { return "gnb.exe" }
