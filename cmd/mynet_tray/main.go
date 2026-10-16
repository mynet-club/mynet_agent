// mynet_tray — MyNet 桌面托盘（Windows / macOS）。
//
// 职责边界（AGENTS.md §1.2 的桌面壳）：
//
//	· 状态机 + 菜单 + 开机自启 + 打开面板
//	· 只控制隧道（经 mynetd 本机 API）；mynetd 由系统服务保证常驻，托盘不启停它
//
// 用法：
//
//	mynet_tray [-api 127.0.0.1:8096] [-home DIR]
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
	home := flag.String("home", "", "MYNET_HOME 覆盖（注册表 Run 给不了环境变量时用）")
	showVer := flag.Bool("version", false, "打印版本并退出")
	flag.Parse()

	if *showVer {
		fmt.Printf("mynet_tray %s\n", version)
		return
	}

	// -home 优先（launchd/计划任务之外的自启通道）；其次环境变量；
	// 最后用平台默认（与 utils.PlatformDefaultHome 同口径，不猜 exe 目录）。
	//
	// ⚠️ D9 之后托盘自己**不再读** MYNET_HOME（intent 落在用户配置目录，
	// 见 intent.go），这里设置它只为保持 -home 的既有语义与子进程环境。
	// -home 这个 flag 不能删：安装器登记的 argv 里带着它，删了 flag.Parse
	// 会因未知参数直接退出。
	if *home != "" {
		_ = os.Setenv("MYNET_HOME", *home)
	}
	if os.Getenv("MYNET_HOME") == "" {
		switch runtime.GOOS {
		case "darwin", "linux", "freebsd", "openbsd", "netbsd":
			_ = os.Setenv("MYNET_HOME", "/opt/mynet")
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
	app := NewApp(prob, *api)

	app.Run()
	os.Exit(0)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
