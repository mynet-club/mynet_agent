package mynet

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/detector"
	"github.com/mynet-club/mynet_agent/shared"
)

// cmdDetector — F-1/F-6：探测系统能力并写 system.json。
//
// 用法: mynet detector [--json] [--out PATH] [--force]
//
// 收敛说明：本命令背后只有 detector 一套实现（原 tools/{firewall,routing} 的
// 重复读逻辑已废弃，OpenWrt/Darwin 覆盖已并入 detector）。
func cmdDetector(args []string) int {
	outPath := shared.DefaultSnapshotPath()
	asJSON := false
	force := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--force":
			force = true
		case "--out":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--out 需要路径")
				return 2
			}
			i++
			outPath = args[i]
		}
	}

	// 指纹重跑：已有快照且接口未变则跳过（--force 强制）
	if !force {
		if snap, err := shared.ReadSnapshot(outPath); err == nil {
			ifaces := probeIfaceFingerprints()
			if !shared.NeedsRedetect(snap, ifaces) {
				if asJSON {
					_ = json.NewEncoder(os.Stdout).Encode(snap)
				} else {
					fmt.Printf("system.json 指纹未变，跳过（--force 强制重探测）: %s\n", outPath)
				}
				return 0
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ud, err := detector.GetDefaultDetector()
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建探测器失败: %v\n", err)
		return 1
	}
	defer detector.ShutdownDefaultDetector()

	dctx := detector.DetectionContext{
		Context:   ctx,
		Timeout:   10 * time.Second,
		ConfigDir: shared.VarDir(),
	}

	result, err := ud.DetectAll(dctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "探测失败: %v\n", err)
		return 1
	}

	snap := buildSnapshot(result)
	if err := shared.WriteSnapshot(outPath, snap); err != nil {
		fmt.Fprintf(os.Stderr, "写 system.json 失败: %v\n", err)
		return 1
	}

	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(snap)
	} else {
		fmt.Printf("已写入 %s\n  os=%s kernel=%s ifaces=%d fingerprint=%s\n",
			outPath, snap.OS, snap.Kernel, len(snap.Interfaces), snap.Fingerprint[:16])
	}
	return 0
}

func buildSnapshot(result *detector.DetectionResult) *shared.SystemSnapshot {
	snap := &shared.SystemSnapshot{
		GeneratedAt:  time.Now().UTC(),
		Capabilities: map[string]bool{},
		Detail:       map[string]json.RawMessage{},
	}
	if result.System != nil {
		snap.Hostname = result.System.Hostname
		snap.OS = result.System.OS
		snap.Distribution = result.System.Distribution
		snap.Kernel = result.System.Kernel
		snap.Architecture = result.System.Architecture
	}
	if snap.Architecture == "" {
		snap.Architecture = runtime.GOARCH
	}
	if result.Routing != nil {
		for _, i := range result.Routing.Interfaces {
			snap.Interfaces = append(snap.Interfaces, shared.IfaceFingerprint{
				Name:  i.Name,
				MAC:   i.MAC,
				State: i.State,
			})
		}
		// 能力线索
		snap.Capabilities["ip"] = true
	}
	if result.Firewall != nil {
		switch result.Firewall.Type {
		case "iptables", "fw3", "firewalld", "ufw", "nftables":
			snap.Capabilities[result.Firewall.Type] = true
		}
	}
	// 常用工具可用性（apply 用来决定 exec 什么）
	for _, tool := range []string{"iptables", "nft", "uci", "ip", "ss", "wg"} {
		snap.Capabilities[tool] = commandExists(tool)
	}
	// D3：本地解析器探测（网关 DNS 接管要用）
	// 有智能 DNS 用之；没有 → 接平台 DNS
	snap.Capabilities["dnsmasq"] = commandExists("dnsmasq")
	snap.Capabilities["smartdns"] = commandExists("smartdns")
	snap.Fingerprint = shared.ComputeFingerprint(snap.Interfaces)

	if raw, err := json.Marshal(result.System); err == nil {
		snap.Detail["system"] = raw
	}
	if raw, err := json.Marshal(result.Routing); err == nil {
		snap.Detail["routing"] = raw
	}
	if raw, err := json.Marshal(result.Firewall); err == nil {
		snap.Detail["firewall"] = raw
	}
	return snap
}

func commandExists(name string) bool {
	for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/bin", "/bin"} {
		if st, err := os.Stat(dir + "/" + name); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

// probeIfaceFingerprints 仅取指纹所需字段（轻量，供跳过判断）
func probeIfaceFingerprints() []shared.IfaceFingerprint {
	ud, err := detector.GetDefaultDetector()
	if err != nil {
		return nil
	}
	defer detector.ShutdownDefaultDetector()
	dctx := detector.DetectionContext{
		Context:   context.Background(),
		Timeout:   5 * time.Second,
		ConfigDir: shared.VarDir(),
	}
	r, err := ud.DetectRouting(dctx)
	if err != nil || r == nil {
		return nil
	}
	var out []shared.IfaceFingerprint
	for _, i := range r.Interfaces {
		out = append(out, shared.IfaceFingerprint{Name: i.Name, MAC: i.MAC, State: i.State})
	}
	return out
}
