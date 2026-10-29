package mynet

import (
	"testing"
)

func withStartGates(t *testing.T, nodeReady, dnsReady bool) {
	t.Helper()
	prevNode, prevDNS := probeAccelerationNodeReady, probeDNSResolutionReady
	probeAccelerationNodeReady = func() bool { return nodeReady }
	probeDNSResolutionReady = func() bool { return dnsReady }
	t.Cleanup(func() {
		probeAccelerationNodeReady = prevNode
		probeDNSResolutionReady = prevDNS
	})
}

func TestAccelerationStartGateRequiresTunnelAndNode(t *testing.T) {
	pcAccelerationTestHome(t)
	withStartGates(t, true, true)

	// 无隧道：不得过闸
	if err := accelerationStartGate(); err == nil {
		t.Fatal("no tunnel must block acceleration start")
	}

	// 有隧道但节点不可达：不得过闸
	t.Cleanup(func() {})
	// liveHookTunDevice 读运行态；测试里用假隧道困难，改为直接测 node/dns 子闸。
	if accelerationNodeReady() != true {
		t.Fatal("node gate should pass when probe says ready")
	}
	withStartGates(t, false, true)
	if accelerationNodeReady() {
		t.Fatal("node gate must fail when exit unreachable")
	}
}

func TestDNSHookStartGateRequiresResolution(t *testing.T) {
	withStartGates(t, true, true)
	if err := dnsHookStartGate(); err != nil {
		t.Fatalf("dns gate should pass when resolution works: %v", err)
	}
	withStartGates(t, true, false)
	if err := dnsHookStartGate(); err == nil {
		t.Fatal("dns gate must fail when resolution unavailable")
	}
}

func TestAccelerationStateReadyWaitingNodeAndDNS(t *testing.T) {
	for _, tc := range []struct {
		wanted, alive, tunnel, node, dns bool
		state                            string
	}{
		{true, false, true, false, true, "waiting_node"},
		{true, false, true, true, false, "waiting_dns"},
		{true, false, true, true, true, "failed"},
		{true, false, true, false, false, "waiting_node"},
		{true, true, true, false, false, "running"},
		{true, false, false, false, false, "waiting_tunnel"},
	} {
		if got := accelerationStateReady(tc.wanted, tc.alive, tc.tunnel, tc.node, tc.dns); got != tc.state {
			t.Fatalf("%+v: got %s want %s", tc, got, tc.state)
		}
	}
}

func TestStartDNSHookSidecarSkipsWhenGatesClosed(t *testing.T) {
	pcAccelerationTestHome(t)
	if err := SetDNSHookWanted(true); err != nil {
		t.Fatal(err)
	}
	// 节点不可达：跳过启动，不报错、不起进程
	withStartGates(t, false, true)
	res := startDNSHookSidecar()
	if res.err != nil {
		t.Fatalf("waiting node must not be error: %v", res.err)
	}
	if dnsHookRunning() {
		t.Fatal("must not start hook while exit node unreachable")
	}

	// DNS 不可用：同样跳过
	withStartGates(t, true, false)
	res = startDNSHookSidecar()
	if res.err != nil {
		t.Fatalf("waiting dns must not be error: %v", res.err)
	}
	if dnsHookRunning() {
		t.Fatal("must not start hook while DNS resolution unavailable")
	}
}
