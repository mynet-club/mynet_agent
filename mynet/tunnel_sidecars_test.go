package mynet

import (
	"os"
	"path/filepath"
	"testing"
)

// 默认不得劫持：没有意图文件时 DNSHookWanted 必须为 false（D4/§14）。
func TestDNSHookWantedDefaultsFalse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")

	if DNSHookWanted() {
		t.Fatal("无意图文件时不得默认开启加速")
	}
}

// 用户开关写入意图；重启后（新进程读文件）仍能读到 —— 隧道启停联动靠它。
func TestDNSHookIntentPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")

	if err := SetDNSHookWanted(true); err != nil {
		t.Fatal(err)
	}
	if !DNSHookWanted() {
		t.Fatal("SetDNSHookWanted(true) 后应读到 true")
	}
	if err := SetDNSHookWanted(false); err != nil {
		t.Fatal(err)
	}
	if DNSHookWanted() {
		t.Fatal("SetDNSHookWanted(false) 后应读到 false")
	}
}

// 损坏的意图文件不得被当成「已启用」—— fail-closed 到默认不劫持。
func TestDNSHookIntentCorruptIsDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")

	p := dnsHookIntentFile()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if DNSHookWanted() {
		t.Fatal("损坏意图文件必须按未启用处理")
	}
}

// 未启用时编排必须是 no-op 路径（不 panic、不尝试起进程）。
func TestSyncSidecarsUpSkipsWhenUnconfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")

	// 不应写任何意图/不应因缺 proxy intent 而 panic。
	SyncSidecarsOnTunnelUp()
	SyncSidecarsOnTunnelDown()

	if DNSHookWanted() {
		t.Fatal("编排不得隐式开启加速意图")
	}
}
