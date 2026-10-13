package mynet

// guest —— GNB 离线（Guest）模式配置写面（G5.b）。
//
// route.conf 等 gnb 投影的**唯一写面**在本执行器；Lua 只调
// `mynet guest route --node-id N`，不再自己拼 route.conf（B4/第二 writer）。
//
//	mynet guest route --node-id 1001 [--guest-file PATH]
//
// 从 guest.json 读节点表，写出 `driver/gnb/conf/<id>/route.conf`
// （本机行在前、`id|ip|255.255.255.255`）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

type guestNode struct {
	NodeID    int64  `json:"node_id"`
	VirtualIP string `json:"virtual_ip"`
	Name      string `json:"name,omitempty"`
	IsLocal   bool   `json:"is_local,omitempty"`
}

type guestFile struct {
	Nodes []guestNode `json:"nodes"`
}

func cmdGuest(args []string) int {
	if len(args) < 1 || args[0] != "route" {
		fmt.Fprintln(os.Stderr, "用法: mynet guest route --node-id N [--guest-file PATH]")
		return 2
	}
	return guestWriteRoute(args[1:])
}

func guestWriteRoute(args []string) int {
	var nodeID int64
	guestPath := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--node-id":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &nodeID)
				i++
			}
		case "--guest-file":
			if i+1 < len(args) {
				guestPath, i = args[i+1], i+1
			}
		}
	}
	if nodeID == 0 {
		fmt.Fprintln(os.Stderr, "缺 --node-id")
		return 2
	}
	if guestPath == "" {
		guestPath = filepath.Join(utils.GetMyNetHome(), "conf", "guest.json")
	}
	raw, err := os.ReadFile(guestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读 guest.json 失败: %v\n", err)
		return 1
	}
	var g guestFile
	if err := json.Unmarshal(raw, &g); err != nil || len(g.Nodes) == 0 {
		fmt.Fprintln(os.Stderr, "guest.json 无节点")
		return 1
	}
	content := renderGuestRouteConf(nodeID, g.Nodes)
	if content == "" {
		fmt.Fprintln(os.Stderr, "无法生成 route.conf")
		return 1
	}
	// gnb conf dir：节点唯一存放位 conf/<form>/<nodeId>/（D4-E，与 apply 同源）
	form, _ := utils.ActiveNodeInfo()
	confDir, err := utils.NodeRuntimeDir(form, fmt.Sprintf("%d", nodeID))
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析 gnb conf 目录失败: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "建目录失败: %v\n", err)
		return 1
	}
	out := filepath.Join(confDir, "route.conf")
	if err := os.WriteFile(out, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "写 route.conf 失败: %v\n", err)
		return 1
	}
	fmt.Println(out)
	return 0
}

// renderGuestRouteConf 与原 Lua gen_route_conf 同构：
// 每行 `node_id|virtual_ip|255.255.255.255`，本机行必须在第一行
// （gnb 缺 local_node 会报 miss local_node is NULL）。
func renderGuestRouteConf(selfID int64, nodes []guestNode) string {
	var selfLine string
	var lines []string
	for _, n := range nodes {
		l := fmt.Sprintf("%d|%s|255.255.255.255", n.NodeID, n.VirtualIP)
		if n.NodeID == selfID {
			selfLine = l
		} else {
			lines = append(lines, l)
		}
	}
	if selfLine == "" {
		return ""
	}
	lines = append([]string{selfLine}, lines...)
	return strings.Join(lines, "\n") + "\n"
}
