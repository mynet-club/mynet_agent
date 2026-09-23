package mynet

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// cmdInstall / cmdUninstall — R2-4：装/卸到系统
// 桌面：systemd/launchd；OpenWrt：procd（ipk postinst 也调）。
func cmdInstall(args []string) int {
	prefix := "/opt/mynet"
	if runtime.GOOS == "darwin" {
		prefix = "/usr/local/opt/mynet"
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "--prefix" && i+1 < len(args) {
			i++
			prefix = args[i]
		}
	}
	binDir := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// 复制当前 mynet / mynetd（若同目录存在）
	self, _ := os.Executable()
	selfDir := filepath.Dir(self)
	for _, name := range []string{"mynet", "mynetd"} {
		src := filepath.Join(selfDir, name)
		dst := filepath.Join(binDir, name)
		if _, err := os.Stat(src); err == nil {
			if err := copyFile(src, dst, 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "install %s: %v\n", name, err)
				return 1
			}
			fmt.Println("installed", dst)
		}
	}
	unit := unitPath()
	if unit != "" {
		fmt.Println("service unit:", unit)
		fmt.Println("enable: 请按平台执行 systemctl/launchctl")
	}
	return 0
}

func cmdUninstall(_ []string) int {
	fmt.Println("uninstall: 停服务后删除 prefix/bin/{mynet,mynetd}；OpenWrt 走 ipk remove")
	return 0
}

func unitPath() string {
	switch runtime.GOOS {
	case "linux":
		return "/etc/systemd/system/mynetd.service"
	case "darwin":
		return "/Library/LaunchDaemons/club.mynet.mynetd.plist"
	default:
		return ""
	}
}

func copyFile(src, dst string, perm os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, perm)
}
