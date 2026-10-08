package mynet

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

func exitTestPolicy(cidrs ...string) *domain.ProxyPolicy {
	return &domain.ProxyPolicy{Enabled: true, Role: domain.ProxyRoleServer, ACL: &domain.ProxyACL{Mode: domain.ProxyACLAllowlist, AllowedCIDRs: cidrs}}
}

// fake executor 对 nft batch 在成功时一次提交；失败时保留先前现场。
type exitNftFake struct {
	table     string
	batches   []string
	deleted   []string
	failWrite bool
	readErr   bool
	legacy    map[string]string
}

func (f *exitNftFake) run(argv []string, input string) (string, error) {
	args := strings.Join(argv, " ")
	if args == "nft list table inet "+proxyExitName("gnb_tun") {
		if f.readErr {
			return "Operation not permitted", errors.New("denied")
		}
		if f.table == "" {
			return "No such file or directory", errors.New("missing")
		}
		return f.table, nil
	}
	if args == "nft -f -" {
		f.batches = append(f.batches, input)
		if f.failWrite {
			return "invalid rule", errors.New("write failed")
		}
		if strings.Contains(input, "add table") {
			f.table = input
		} else {
			f.table = ""
		}
		return "", nil
	}
	if strings.HasPrefix(args, "nft -a list chain") {
		return f.legacy[args], nil
	}
	if strings.HasPrefix(args, "nft delete rule") {
		f.deleted = append(f.deleted, args)
		return "", nil
	}
	return "", fmt.Errorf("unexpected command: %s", args)
}
func TestProxyExitNftReplaceRevokeAndIsolation(t *testing.T) {
	f := &exitNftFake{}
	if err := syncProxyExitFirewall(snapNft(), exitTestPolicy("10.30.0.3/32"), "gnb_tun", false, f.run); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.table, `iifname "gnb_tun" drop`) || !strings.Contains(f.table, "masquerade") {
		t.Fatal(f.table)
	}
	if err := syncProxyExitFirewall(snapNft(), exitTestPolicy("10.30.0.4/32"), "gnb_tun", false, f.run); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.table, "10.30.0.3/32") || !strings.Contains(f.table, "delete table inet "+proxyExitName("gnb_tun")) {
		t.Fatal("旧白名单未替换")
	}
	previous := f.table
	if err := syncProxyExitFirewall(snapNft(), exitTestPolicy("10.30.0.4/32"), "gnb_tun", false, f.run); err != nil {
		t.Fatal(err)
	}
	if f.table != previous {
		t.Fatal("重复 apply 不幂等")
	}
	if err := syncProxyExitFirewall(snapNft(), nil, "gnb_tun", false, f.run); err != nil {
		t.Fatal(err)
	}
	if f.table != "" {
		t.Fatal("撤销后仍有 NAT/ACL")
	}
	if err := syncProxyExitFirewall(snapNft(), nil, "gnb_tun", false, f.run); err != nil {
		t.Fatal(err)
	}
	for _, batch := range f.batches {
		if strings.Contains(batch, "delete table ip nat") || strings.Contains(batch, "flush ruleset") || strings.Contains(batch, "delete table ip filter") {
			t.Fatal("误清共享防火墙")
		}
	}
	if proxyExitName("gnb_tun") == proxyExitName("wg0") {
		t.Fatal("接口归属碰撞")
	}
}
func TestProxyExitNftFailurePreservesPrevious(t *testing.T) {
	f := &exitNftFake{}
	if err := syncProxyExitFirewall(snapNft(), exitTestPolicy("10.30.0.3/32"), "gnb_tun", false, f.run); err != nil {
		t.Fatal(err)
	}
	before := f.table
	f.failWrite = true
	if err := syncProxyExitFirewall(snapNft(), exitTestPolicy("10.30.0.4/32"), "gnb_tun", false, f.run); err == nil {
		t.Fatal("ACL 失败被忽略")
	}
	if f.table != before {
		t.Fatal("失败破坏上一代规则")
	}
	if err := syncProxyExitFirewall(snapNft(), nil, "gnb_tun", false, f.run); err == nil {
		t.Fatal("停用失败被忽略")
	}
	f.failWrite = false
	f.readErr = true
	if err := syncProxyExitFirewall(snapNft(), nil, "gnb_tun", false, f.run); err == nil {
		t.Fatal("权限不足被当作规则不存在")
	}
}
func TestProxyExitEmptyCIDRsFailClosedAndIPv6(t *testing.T) {
	text, err := proxyExitNftBatch("gnb_tun", exitTestPolicy(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `iifname "gnb_tun" drop`) {
		t.Fatal("空地址白名单变成开放")
	}
	text, err = proxyExitNftBatch("gnb_tun", exitTestPolicy("fd00::3/128"), false)
	if err != nil || !strings.Contains(text, "ip6 saddr fd00::3/128 accept") {
		t.Fatalf("IPv6: %s %v", text, err)
	}
	if _, err = proxyExitNftBatch("bad\";flush ruleset", exitTestPolicy(), false); err == nil {
		t.Fatal("接口注入未拒绝")
	}
	for _, bad := range []string{"bad", "10.0.0.1/33"} {
		if _, err = proxyExitNftBatch("gnb_tun", exitTestPolicy(bad), false); err == nil {
			t.Fatal("非法 CIDR 未拒绝")
		}
	}
	if _, err = proxyExitIPTDesired("gnb_tun", exitTestPolicy("fd00::3/128")); err == nil {
		t.Fatal("iptables 静默忽略 IPv6")
	}
}
func TestProxyExitLegacyCleanupExactRulesOnly(t *testing.T) {
	f := &exitNftFake{legacy: map[string]string{
		"nft -a list chain ip nat POSTROUTING": `iifname "gnb_tun" masquerade # handle 10
 iifname "wg0" masquerade # handle 11
 iifname "gnb_tun" masquerade comment "user-rule" # handle 12`,
		"nft -a list chain ip filter FORWARD": `iifname "gnb_tun" ip saddr 10.30.0.3 accept # handle 20
 iifname "gnb_tun" drop # handle 21
 iifname "gnb_tun" tcp dport 443 accept # handle 22
 iifname "wg0" drop # handle 23`,
	}}
	if err := syncProxyExitFirewall(snapNft(), nil, "gnb_tun", true, f.run); err != nil {
		t.Fatal(err)
	}
	if len(f.batches) != 1 {
		t.Fatalf("历史迁移未原子提交: %v", f.batches)
	}
	text := f.batches[0]
	if strings.Count(text, "delete rule") != 3 {
		t.Fatal(text)
	}
	for _, handle := range []string{"11", "12", "22", "23"} {
		if strings.Contains(text, "handle "+handle+"\n") {
			t.Fatal("删了其他规则")
		}
	}
}
func TestProxyExitIPTablesOwnedReplaceAndDelete(t *testing.T) {
	name := proxyExitName("gnb_tun")
	snapshot := "-P FORWARD DROP\n-N " + name + "\n-A " + name + " -s 10.30.0.3/32 -j ACCEPT\n-A " + name + " -j DROP\n-A FORWARD -i gnb_tun -j " + name + "\n-A FORWARD -i wg0 -j ACCEPT\n"
	desired, err := proxyExitIPTDesired("gnb_tun", exitTestPolicy("10.30.0.4/32"))
	if err != nil {
		t.Fatal(err)
	}
	text := proxyExitIPTRestore("filter", snapshot, name, desired["filter"])
	if strings.Contains(text, "wg0") || strings.Contains(text, "10.30.0.3") || strings.Contains(text, "-F FORWARD") {
		t.Fatal("泄漏旧名单/清共享链")
	}
	for _, want := range []string{"-D FORWARD -i gnb_tun -j " + name, "-F " + name, "10.30.0.4/32", "-A " + name + " -j DROP"} {
		if !strings.Contains(text, want) {
			t.Fatal(text)
		}
	}
	text = proxyExitIPTRestore("filter", snapshot, name, nil)
	if !strings.Contains(text, "-X "+name) || strings.Contains(text, "wg0") {
		t.Fatal(text)
	}
	if strings.Contains(strings.Join(desired["nat"], "\n"), "-i ") {
		t.Fatal("POSTROUTING 不支持入接口匹配")
	}
}
func TestProxyExitIPTablesFailureRollsBackOwnedRules(t *testing.T) {
	name := proxyExitName("gnb_tun")
	old := "-N " + name + "\n-A " + name + " -j DROP\n-A FORWARD -i gnb_tun -j " + name + "\n"
	var inputs []string
	writes := 0
	run := func(argv []string, input string) (string, error) {
		if argv[0] == "iptables" {
			return old, nil
		}
		writes++
		inputs = append(inputs, input)
		if writes == 2 {
			return "injected", errors.New("mangle failed")
		}
		return "", nil
	}
	if err := syncProxyExitIPTables(exitTestPolicy("10.30.0.4/32"), "gnb_tun", false, run); err == nil {
		t.Fatal("中途失败被当成功")
	}
	if len(inputs) != 3 || !strings.Contains(inputs[2], "-A "+name+" -j DROP") || strings.Contains(inputs[2], "10.30.0.4") {
		t.Fatalf("没恢复旧授权: %v", inputs)
	}
}

func TestProxyExitMarkCollisionRejected(t *testing.T) {
	desired, err := proxyExitIPTDesired("gnb_tun", exitTestPolicy("10.30.0.3/32"))
	if err != nil {
		t.Fatal(err)
	}
	mark := strings.Fields(desired["mangle"][1])[5]
	if err := proxyExitMarkConflict("-A other-exit -j MARK --set-xmark "+mark, proxyExitName("gnb_tun"), desired["mangle"]); err == nil {
		t.Fatal("出口 NAT 标记碰撞未拒绝")
	}
	if err := proxyExitMarkConflict("-A client -j MARK --set-xmark 0xc8/0xff", proxyExitName("gnb_tun"), desired["mangle"]); err != nil {
		t.Fatal(err)
	}
}
