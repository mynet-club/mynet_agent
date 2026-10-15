package mynet

// deviceinfo —— 把本机硬件/系统快照上报到后端 `PUT /api/v1/nodes/{id}/deviceinfo`。
//
// GUI 设备页读的就是这条（NodeListPage 批量 deviceinfo），此前**客户端从未上报**，
// 于是页面长期「暂无设备信息」。口径：启动/登录后采一次即可（硬件不变），
// 不走心跳（心跳是节点面，deviceinfo 是用户面 + Auth::id）。

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mynet-club/mynet_agent/shared"
)

func cmdDeviceInfo(args []string) int {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "report", "":
		return deviceInfoReport()
	default:
		fmt.Fprintln(os.Stderr, "用法: mynet deviceinfo report")
		return 2
	}
}

// deviceInfoReport 读 system.json → 映射 → PUT deviceinfo。
func deviceInfoReport() int {
	snap, err := shared.ReadSnapshot(shared.DefaultSnapshotPath())
	if err != nil {
		// 快照缺失就地探测一次 —— 装完还没跑 detector 是常态
		if code := cmdDetector(nil); code != 0 {
			fmt.Fprintln(os.Stderr, "system.json 缺失且 detector 失败")
			return 1
		}
		snap, err = shared.ReadSnapshot(shared.DefaultSnapshotPath())
		if err != nil {
			fmt.Fprintln(os.Stderr, "读 system.json 失败:", err)
			return 1
		}
	}

	nodeID := ActiveNodeID(os.Getenv("MYNET_HOME"))
	if nodeID == "" {
		fmt.Fprintln(os.Stderr, "无活动节点（先 mynet node claim）")
		return 1
	}

	cred := requireCred()
	c := newBackClientFor(cred)

	payload := deviceInfoFromSnapshot(snap)
	path := "/api/v1/nodes/" + nodeID + "/deviceinfo"

	// 路由是 PATCH（api.php: `Route::patch('/{id}/deviceinfo')`），不是 PUT
	raw, code, err := c.doJSON("PATCH", path, payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "上报设备信息失败: %v\n", err)
		return 1
	}
	var env struct {
		Success bool            `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(raw, &env)
	if code < 200 || code > 299 || !env.Success {
		fmt.Fprintf(os.Stderr, "后端拒绝（HTTP %d）: %s\n", code, truncate(string(raw), 200))
		return 1
	}
	fmt.Printf("设备信息已上报 node_id=%s\n", nodeID)
	return 0
}

// deviceInfoFromSnapshot 把 system.json 投影成 deviceinfo 契约字段。
// 只填能证实的；cpu_model / public_ip 等探测不到就不填（后端是 sometimes）。
func deviceInfoFromSnapshot(snap *shared.SystemSnapshot) map[string]any {
	p := map[string]any{
		"collected_at": time.Now().UTC().Format(time.RFC3339),
	}
	if snap == nil {
		return p
	}
	if snap.Distribution != "" {
		p["os_name"] = snap.Distribution
	} else if snap.OS != "" {
		p["os_name"] = snap.OS
	}
	// os_version 在 detail.system.version（detector 顶层没有 version 列）
	if v := detailString(snap, "system", "version"); v != "" {
		p["os_version"] = v
	}
	if snap.Kernel != "" {
		p["kernel_version"] = snap.Kernel
	}
	if snap.Architecture != "" {
		p["system_arch"] = snap.Architecture
		p["cpu_arch"] = snap.Architecture
	}
	if n := len(snap.Interfaces); n > 0 {
		p["nic_count"] = n
	}
	if ips := localIPsFromDetail(snap); len(ips) > 0 {
		p["local_ips"] = ips
	}
	if fw := detailString(snap, "firewall", "type"); fw != "" {
		p["firewall_type"] = fw
	}
	if en := detailBool(snap, "firewall", "enabled"); en != nil {
		if *en {
			p["firewall_enabled"] = 1
		} else {
			p["firewall_enabled"] = 0
		}
	}
	return p
}

// detailString 从 snap.Detail[domain] 的 JSON 里按点路径取字符串。
func detailString(snap *shared.SystemSnapshot, domain, key string) string {
	raw, ok := snap.Detail[domain]
	if !ok {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func detailBool(snap *shared.SystemSnapshot, domain, key string) *bool {
	raw, ok := snap.Detail[domain]
	if !ok {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	if v, ok := m[key].(bool); ok {
		return &v
	}
	return nil
}

// localIPsFromDetail 从 detail.routing.interfaces[].ips 取非回环 IPv4。
func localIPsFromDetail(snap *shared.SystemSnapshot) []string {
	raw, ok := snap.Detail["routing"]
	if !ok {
		return nil
	}
	var r struct {
		Interfaces []struct {
			Name string   `json:"name"`
			IPs  []string `json:"ips"`
		} `json:"interfaces"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil
	}
	var out []string
	for _, iface := range r.Interfaces {
		for _, ip := range iface.IPs {
			if ip == "" || ip == "127.0.0.1" {
				continue
			}
			out = append(out, ip)
		}
	}
	return out
}
