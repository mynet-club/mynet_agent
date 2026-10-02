// mynet_tray — MyNet 桌面托盘（Windows / macOS）。
//
// 职责边界（AGENTS.md §1.2 的桌面壳）：
//
//	· 状态机 + 菜单 + 开机自启 + 打开面板
//	· 启停 mynetd 交给平台控制器（launchd / 计划任务），托盘不自己管进程表
//
// 用法：
//
//	mynet_tray [-api 127.0.0.1:8096] [-mynetd PATH] [-home DIR]
//
// 面板与 API 同址（http://127.0.0.1:8096/），与 mynetd.DefaultLocalAPI 一致。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

var version = "dev" // -ldflags 注入

func main() {
	api := flag.String("api", "", "本机 API host:port（默认取 MYNET_LOCAL_API，再默认 "+LocalAPI+"）")
	mynetd := flag.String("mynetd", "", "mynetd 路径（覆盖默认探测；开发用）")
	home := flag.String("home", "", "MYNET_HOME 覆盖（注册表 Run 给不了环境变量时用）")
	showVer := flag.Bool("version", false, "打印版本并退出")
	flag.Parse()

	if *showVer {
		fmt.Printf("mynet_tray %s\n", version)
		return
	}

	// -home 优先（launchd/计划任务之外的自启通道）；其次环境变量；
	// 最后按安装约定猜：托盘在 /Applications/MyNet.app/… 时 GetMyNetHome 会猜错，
	// 未设时用 /var/lib/mynet（darwin/linux）或 ProgramData\MyNet（windows）。
	if *home != "" {
		_ = os.Setenv("MYNET_HOME", *home)
	}
	if os.Getenv("MYNET_HOME") == "" {
		switch runtime.GOOS {
		case "darwin", "linux":
			if _, err := os.Stat("/var/lib/mynet"); err == nil {
				_ = os.Setenv("MYNET_HOME", "/var/lib/mynet")
			}
		case "windows":
			if d := os.Getenv("ProgramData"); d != "" {
				_ = os.Setenv("MYNET_HOME", filepath.Join(d, "MyNet"))
			}
		}
	}
	if *api == "" {
		*api = envOr("MYNET_LOCAL_API", LocalAPI)
	}

	// 菜单用的 API 基址与状态机探针同一个，避免「面板 8096、托盘 8711」
	// 这种并存幻觉（install 脚本漂移过端口）。
	prob := NewHTTPProber(*api)
	ctrl := NewController(*mynetd)
	app := NewApp(prob, ctrl, *api)

	app.Run()
	os.Exit(0)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
