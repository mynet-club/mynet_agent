package mynet

import (
	"encoding/json"
	"fmt"
	"os"
)

// cmdRouteExists — G3 执行器入口：mynet route exists --dest CIDR | --host IP [--json]
// 输出 {"exists": bool}；无法判定时退出码 1（不把未知当 false）。
func cmdRouteExists(args []string) int {
	dest := ""
	host := ""
	asJSON := true
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dest":
			if i+1 < len(args) {
				dest = args[i+1]
				i++
			}
		case "--host":
			if i+1 < len(args) {
				host = args[i+1]
				i++
			}
		case "--json":
			asJSON = true
		}
	}
	target := dest
	if target == "" {
		target = host
	}
	if target == "" {
		fmt.Fprintln(os.Stderr, "用法: mynet route exists --dest CIDR | --host IP")
		return 2
	}
	ok := osRouteExists(target)
	if !ok && host != "" {
		ok = localRouteExists(host)
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		_ = enc.Encode(map[string]any{"exists": ok})
	}
	if !ok {
		// 存在性=false 是合法结果；本命令在「查得到」时始终 0
		return 0
	}
	return 0
}
