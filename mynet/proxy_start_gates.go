package mynet

// 启动门禁（2026-10-28 用户裁定）：
//   - 启动加速：加速节点（出口）必须已经可达，否则不启动。
//   - 启动 dns-hook：DNS 解析必须可用，否则不启动。
//
// 门禁只拦「拉起进程」这一下；用户意图（wanted）照旧保存，
// 隧道/节点/DNS 就绪后由侧车编排或用户再次开启时重试。

import (
	"fmt"
	"time"
)

// 可注入，测试不打真 gnb / 系统解析器。
var (
	// probeAccelerationNodeReady 驱动探活加速节点是否可达。
	probeAccelerationNodeReady = defaultAccelerationNodeReady
	// probeDNSResolutionReady 系统解析器是否还能解析（不经过 hook）。
	probeDNSResolutionReady = defaultDNSResolutionReady
)

// defaultAccelerationNodeReady 用出口探测判加速节点是否已可达。
// 无出口配置时视为不就绪 —— 没有可加速的目标，不该起加速。
func defaultAccelerationNodeReady() bool {
	nodeID, via := currentExitProbe()
	if nodeID == "" && via == "" {
		return false
	}
	res := probeExitOnce(nodeID, via, time.Now())
	return res.Reachable
}

// defaultDNSResolutionReady 系统 DNS 解析可用（写后保险丝同口径）。
func defaultDNSResolutionReady() bool {
	return verifySystemDNSWorks() == nil
}

// accelerationNodeReady 加速节点（出口）是否已可达。
func accelerationNodeReady() bool {
	return probeAccelerationNodeReady()
}

// dnsResolutionReady DNS 解析是否可用。
func dnsResolutionReady() bool {
	return probeDNSResolutionReady()
}

// accelerationStartGate 启动加速前的门禁：隧道已在、节点可达。
// 返回 nil 表示可以启动；非 nil 为等待原因（用户可见话术）。
func accelerationStartGate() error {
	if liveHookTunDevice() == "" {
		return fmt.Errorf("等待隧道连接")
	}
	if !accelerationNodeReady() {
		return fmt.Errorf("等待加速节点可达")
	}
	return nil
}

// dnsHookStartGate 启动 dns-hook 前的门禁：DNS 解析可用。
// 在 accelerationStartGate 通过之后调用（先节点、后 DNS）。
func dnsHookStartGate() error {
	if !dnsResolutionReady() {
		return fmt.Errorf("等待 DNS 解析可用")
	}
	return nil
}
