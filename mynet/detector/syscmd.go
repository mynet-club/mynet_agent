package detector

import (
	"os"
	"os/exec"
	"path/filepath"
)

// 系统工具的绝对路径解析。
//
// ⚠️ `exec.Command("route", …)` 在**创建时**用父进程的 PATH 查可执行文件；
// 之后设置 `cmd.Env` 只影响**子进程**环境，救不了查找本身。
// 所以 safeExecCommand 里那句 PATH=/usr/sbin:… 是无效的 —— daemon 的 PATH
// 残缺时照样 `exec: "route": not found`（实测：apply、node-status、detector
// 三处都栽过同一坑）。
//
// 正确做法：先按扩展 PATH 解析成绝对路径，再 exec。

// sysPaths 系统工具常在的目录。比 daemon 的默认 PATH 多了 sbin 两兄弟。
var sysPaths = []string{
	"/usr/local/sbin", "/usr/local/bin",
	"/usr/sbin", "/usr/bin", "/sbin", "/bin",
}

// lookPathSys 先走 LookPath（尊重用户 PATH 里的自定义版本），
// 失败后扫 sysPaths。找不到返回空串。
func lookPathSys(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range sysPaths {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// sysExec 用绝对路径跑系统工具。name 找不到时原样交给 exec.Command，
// 让错误信息里带上名字（不要吞成 nil）。
func sysExec(name string, args ...string) *exec.Cmd {
	if p := lookPathSys(name); p != "" {
		name = p
	}
	return exec.Command(name, args...)
}
