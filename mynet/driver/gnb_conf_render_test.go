package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent-V2/shared/domain"
)

// realIntent 取自 staging 上 back 真实下发的 envelope（节点 2132596500330640，
// `deploy/diag-bundle.php --full` 的输出），不是编的夹具 —— 渲染器的测试只有
// 打在真数据形状上才有意义。
//
// 唯一改动：ifname 用 gnb_tun 而不是 back 当时发的 gnb_tun_<nodeid>。
// 后者 24 字符，超过 gnb 的 15 字符上限（gnb_conf_file.c:275-277 硬截断）与
// Linux IFNAMSIZ-1，会被静默截成 gnb_tun_2132596 —— 见
// TestGnbRejectsOverlongIfname，那条钉的是 back 侧的缺陷。
func realIntent() *domain.GnbIntent {
	return &domain.GnbIntent{
		Identity: domain.GnbIdentity{
			NodeID:         "2132596500330640",
			KeyAlgorithm:   "ed25519",
			PrivateKey:     "", // back 不持有节点私钥，由 cmd_refresh 从 conf/mynet.conf 注入
			PublicKey:      "a97298cb67045eafdfc6edbfd2666e703472056ec826720cd03713924b107f25",
			PeerPublicKeys: map[string]string{"1005272783125838": "d34082e6f120fa28d4263394438475816bd796cb045380fc12924b4d452afa7f"},
		},
		Listeners: []string{"0.0.0.0:9016"},
		Nodes: []domain.GnbNode{
			{NodeID: "2132596500330640", PublicKey: "a97298cb…", TunAddress: "10.0.0.10/32"},
			{NodeID: "1005272783125838", PublicKey: "d34082e6…", TunAddress: "10.0.0.16/32"},
		},
		Routes: []domain.GnbRoute{
			{NodeID: "1005272783125838", Prefix: "10.0.0.16/32", Source: "lan"},
		},
		Discovery: &domain.GnbDiscovery{
			StaticNodes: []domain.GnbEndpointHint{
				{NodeID: "1005272783125838", Endpoint: "192.168.0.16:9016", Role: "p"},
			},
		},
		Forwarding: &domain.GnbForwarding{Direct: true, Standard: true, Unified: "auto"},
		Options:    &domain.GnbOptions{MultiSocket: true, SafeIndex: true, MTU: 1450, Ifname: "gnb_tun"},
	}
}

func TestGnbRouteConfUsesPipeSeparatorAndDottedMask(t *testing.T) {
	text := RenderGNBRouteConf(realIntent())

	// gnb_conf_file.c:919-928：分隔符只认 `|` 或 `/`，且 sscanf 必须凑满 3 段，
	// 否则 `3 != num` → continue，整行被跳过。掩码走 inet_pton(AF_INET,…)，
	// 所以必须是点分十进制，不能是 CIDR 前缀长度。
	want := []string{
		"2132596500330640|10.0.0.10|255.255.255.255", // 本机自己那条，缺了 gnb 不知道自己的 tun IP
		"1005272783125838|10.0.0.16|255.255.255.255", // 对端
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Fatalf("route.conf 缺 %q\ngot:\n%s", w, text)
		}
	}
	if strings.Contains(text, "/32") {
		t.Fatalf("route.conf 里出现了 CIDR 前缀长度，gnb 的 inet_pton 会解析失败:\n%s", text)
	}
	// 旧实现写的是 "<nodeID> <prefix>"（空格分隔）—— 分隔符不认 → 整份文件被跳过
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if strings.Contains(line, " ") {
			t.Fatalf("route.conf 有空格分隔的行，会被 gnb 静默跳过: %q", line)
		}
		if n := strings.Count(line, "|"); n != 2 {
			t.Fatalf("route.conf 行必须是 3 段（2 个 |），实际 %d 个: %q", n, line)
		}
	}
}

func TestGnbRouteConfAlwaysIncludesSelf(t *testing.T) {
	// bundle 的 gnb.routes 只含**对端**；本机地址在 gnb.nodes 里。
	// 只渲染 routes 会得到一份没有本机 tun IP 的 route.conf —— gnb 起来后
	// 接口没有地址。这条钉住「以 nodes 为主源」这个决定。
	g := realIntent()
	g.Routes = nil // 极端情况：一个对端路由都没有
	text := RenderGNBRouteConf(g)
	if !strings.Contains(text, "2132596500330640|10.0.0.10|255.255.255.255") {
		t.Fatalf("即使没有对端路由，也必须写出本机自己那条:\n%s", text)
	}
}

func TestGnbRouteConfSkipsIPv6AndMalformed(t *testing.T) {
	g := realIntent()
	g.Nodes = []domain.GnbNode{
		{NodeID: "1", TunAddress: "10.0.0.1/32"},
		{NodeID: "2", TunAddress: "fd00::1/128"}, // gnb 的 route.conf 只走 inet_pton(AF_INET)
		{NodeID: "3", TunAddress: "不是CIDR"},
		{NodeID: "4", TunAddress: ""},
	}
	g.Routes = nil
	text := RenderGNBRouteConf(g)
	if strings.Count(strings.TrimSpace(text), "\n") != 0 {
		t.Fatalf("只应留下合法的 IPv4 一行，实际:\n%s", text)
	}
	if !strings.Contains(text, "1|10.0.0.1|255.255.255.255") {
		t.Fatalf("合法行被误丢了:\n%s", text)
	}
}

func TestGnbRouteConf24BecomesDottedMask(t *testing.T) {
	g := realIntent()
	g.Nodes = []domain.GnbNode{{NodeID: "3001", TunAddress: "10.31.0.1/24"}}
	g.Routes = nil
	// 旧 U1 mesh 那份真跑通的配置里就是 3001|10.31.0.1|255.255.255.0
	if got := strings.TrimSpace(RenderGNBRouteConf(g)); got != "3001|10.31.0.1|255.255.255.0" {
		t.Fatalf("got %q", got)
	}
}

func TestGnbAddressConfFormat(t *testing.T) {
	g := realIntent()
	g.Discovery.IndexServers = []string{"101.32.178.3:9001"}
	text := RenderGNBAddressConf(g)

	// gnb_conf_file.c:50-56：sscanf "%16[^|]|%llu|%46[^|]|%hu"，4 段缺一不可
	want := []string{
		"p|1005272783125838|192.168.0.16|9016", // 直连对端，attrib 原样透传
		"i|0|101.32.178.3|9001",                // index：uuid 用 0、attrib 必须 i（上游官方示例同形）
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Fatalf("address.conf 缺 %q\ngot:\n%s", w, text)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if n := strings.Count(line, "|"); n != 3 {
			t.Fatalf("address.conf 行必须是 4 段（3 个 |），实际 %d: %q", n, line)
		}
	}
}

func TestGnbAddressConfEmptyWithoutDiscovery(t *testing.T) {
	g := realIntent()
	g.Discovery = nil
	if got := RenderGNBAddressConf(g); got != "" {
		t.Fatalf("无 discovery 时应返回空串（于是不写 address.conf），got %q", got)
	}
}

func TestGnbAddressConfRejectsBarePort(t *testing.T) {
	// address.conf 必须有 host；只给端口凑不出 4 段，宁可跳过也不要写出会被
	// 静默 continue 的行
	g := realIntent()
	g.Discovery.StaticNodes = []domain.GnbEndpointHint{{NodeID: "9", Endpoint: "9016", Role: "p"}}
	if got := RenderGNBAddressConf(g); got != "" {
		t.Fatalf("裸端口应被拒，got %q", got)
	}
}

func TestGnbWriteIntentLaysOutWholeConfDir(t *testing.T) {
	dir := t.TempDir()
	d := NewGNBDriver(2132596500330640)
	d.ConfDir = dir

	g := realIntent()
	g.Identity.PrivateKey = strings.Repeat("a1", 64) // 128 hex，GNB 的私钥布局
	if err := d.WriteIntent(g); err != nil {
		t.Fatal(err)
	}

	// 目录布局对齐 gnb 的读取路径（gnb_keys.c:60-61,103）与旧 U1 真配置
	for _, rel := range []string{
		"node.conf", "route.conf", "address.conf",
		"security/2132596500330640.private",
		"security/2132596500330640.public",
		"ed25519/1005272783125838.public",
	} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("缺 %s: %v", rel, err)
		}
	}

	// ⚠️ 密钥文件必须是**纯 hex 无换行**：gnb 用裸 read() 且要求读满
	// 128 / 64 字节，读不满就 exit(0)（gnb_keys.c:68-73, 84-89, 108-112）
	for rel, want := range map[string]int{
		"security/2132596500330640.private": 128,
		"security/2132596500330640.public":  64,
		"ed25519/1005272783125838.public":   64,
	} {
		raw, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) != want {
			t.Fatalf("%s 长度 %d，必须恰好 %d（多一个换行 gnb 就读不满/读错）", rel, len(raw), want)
		}
	}

	// 私钥文件必须 0600
	st, err := os.Stat(filepath.Join(dir, "security/2132596500330640.private"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("私钥权限 %v，必须 0600", st.Mode().Perm())
	}

	// node.conf 的关键键
	nc, err := os.ReadFile(filepath.Join(dir, "node.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"nodeid 2132596500330640", "ifname gnb_tun", "listen 0.0.0.0:9016",
		"mtu 1450", "daemon on", "multi-socket on", "safe-index on",
		"direct-forwarding on", "standard-forwarding on", "unified-forwarding auto",
	} {
		if !strings.Contains(string(nc), w) {
			t.Fatalf("node.conf 缺 %q:\n%s", w, nc)
		}
	}
	// pid-file / ctl-block 刻意不写：gnb 用 -c 时自己默认到 <conf>/gnb.pid 与 gnb.map
	for _, bad := range []string{"pid-file", "ctl-block"} {
		if strings.Contains(string(nc), bad) {
			t.Fatalf("node.conf 不该写 %s（gnb 已有正确默认值）:\n%s", bad, nc)
		}
	}
}

func TestGnbWriteIntentOmitsPrivateKeyWhenBundleHasNone(t *testing.T) {
	dir := t.TempDir()
	d := NewGNBDriver(1)
	d.ConfDir = dir
	if err := d.WriteIntent(realIntent()); err != nil { // PrivateKey == ""
		t.Fatal(err)
	}
	// back 不下发私钥，此时不该写出一个空的 .private（gnb 读到 0 字节会 exit）
	if _, err := os.Stat(filepath.Join(dir, "security/2132596500330640.private")); !os.IsNotExist(err) {
		t.Fatalf("bundle 无私钥时不该落 .private 文件，err=%v", err)
	}
	// 但公钥与对端公钥仍要落盘
	if _, err := os.Stat(filepath.Join(dir, "security/2132596500330640.public")); err != nil {
		t.Fatalf("公钥应落盘: %v", err)
	}
}

func TestGnbRejectsOverlongIfname(t *testing.T) {
	dir := t.TempDir()
	d := NewGNBDriver(1)
	d.ConfDir = dir

	g := realIntent()
	// 这正是 back 当时真实下发的值：'gnb_tun_'.$nodeId = 8 + 16 = 24 字符
	g.Options.Ifname = "gnb_tun_2132596500330640"

	err := d.WriteIntent(g)
	if err == nil {
		t.Fatal("24 字符的 ifname 必须被拒：gnb 会静默截断到 15 字符（gnb_conf_file.c:275-277），" +
			"之后所有按接口名做的操作都对不上，且没有任何报错")
	}
	if !strings.Contains(err.Error(), "gnb_tun_2132596500330640") {
		t.Fatalf("错误信息应带上 offending 名字，got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "node.conf")); !os.IsNotExist(statErr) {
		t.Fatal("校验失败就不该留下半成品 node.conf")
	}
}

func TestGnbWriteIntentRequiresNodeID(t *testing.T) {
	d := NewGNBDriver(0)
	d.ConfDir = t.TempDir()
	g := realIntent()
	g.Identity.NodeID = ""
	if err := d.WriteIntent(g); err == nil {
		t.Fatal("缺 node_id 必须报错：nodeid 是 gnb 的必填键，缺了会 exit(1)")
	}
}

// recordingExec 记下所有 argv，用来断言"调的是哪个二进制、带什么参数"。
func recordingExec(d *GNBDriver, calls *[]string) {
	d.Exec = func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, strings.Join(append([]string{name}, args...), " "))
		return nil, nil
	}
}

// recordingSpawn 同上，但记的是 Spawn（会自我 daemon 化的命令走这条路）。
// 同时模拟 gnb 起来后写 pid 文件 —— 否则 Start() 的存活确认会白等 5s 再报错。
func recordingSpawn(d *GNBDriver, calls *[]string, pidFile string) {
	d.Spawn = func(name string, args ...string) error {
		*calls = append(*calls, strings.Join(append([]string{name}, args...), " "))
		if pidFile != "" {
			return os.WriteFile(pidFile, []byte("4242\n"), 0o644)
		}
		return nil
	}
}

func TestGnbStartExecsGnbNotGnbCtl(t *testing.T) {
	dir := t.TempDir()
	d := NewGNBDriver(3001)
	d.ConfDir = dir
	var calls []string
	recordingExec(d, &calls)
	recordingSpawn(d, &calls, filepath.Join(dir, "gnb.pid"))

	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "gnb -c "+dir) {
		t.Fatalf("Start 必须拉起 `gnb -c <confdir>`（gnb_argv.c:899），实际调用:\n%s", joined)
	}
	// 旧实现是 `gnb_ctl -b <conf>/gnb.map`：-b 只是 --ctl-block（指定读哪块共享内存），
	// 且 gnb_ctl 没有 -s/-a 时只打开映射就 return 0 —— 会**假装启动成功**
	if strings.Contains(joined, "gnb_ctl") {
		t.Fatalf("Start 不该调 gnb_ctl（那是控制/查询工具，起不了守护进程）:\n%s", joined)
	}
	// 存活确认走 kill -0，不是 pgrep gnb_es
	if !strings.Contains(joined, "kill -0 4242") {
		t.Fatalf("Start 应确认进程真的活着（daemon 化后父进程 exit(0)，配置错报不回来），实际:\n%s", joined)
	}
}

func TestGnbStartReportsFailureWhenProcessNeverAppears(t *testing.T) {
	dir := t.TempDir()
	d := NewGNBDriver(3001)
	d.ConfDir = dir
	var calls []string
	recordingExec(d, &calls)
	// Spawn 成功但**不写 pid 文件** —— 模拟 gnb 子进程读配置失败后 exit
	recordingSpawn(d, &calls, "")
	// 缩短探测预算，否则这条用例要真等满 5s
	d.StartProbes, d.StartProbeDelay = 3, time.Millisecond

	err := d.Start()
	if err == nil {
		t.Fatal("gnb 没起来时 Start 必须报错：daemon 化后父进程已 exit(0)，" +
			"子进程的配置错误传不回来，只看 Spawn 返回值会误判成功")
	}
	if !strings.Contains(err.Error(), "没有活进程") {
		t.Fatalf("错误信息应说明现象与去哪查，got: %v", err)
	}
}

func TestGnbStartIsIdempotentWhenAlreadyRunning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gnb.pid"), []byte("4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewGNBDriver(3001)
	d.ConfDir = dir
	var calls []string
	recordingExec(d, &calls)
	recordingSpawn(d, &calls, filepath.Join(dir, "gnb.pid"))

	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(calls, "\n"), "gnb -c") {
		t.Fatalf("已在跑时不该重复拉起:\n%s", strings.Join(calls, "\n"))
	}
}

func TestGnbStopUsesPidFileNotGnbCtlK(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gnb.pid"), []byte("4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewGNBDriver(3001)
	d.ConfDir = dir
	var calls []string
	recordingExec(d, &calls)

	if err := d.Stop(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	// 按 pid 停，不按名字（AGENTS.md §3：多实例可同名并存，禁 pkill/killall）
	if !strings.Contains(joined, "kill -TERM 4242") {
		t.Fatalf("Stop 必须按 pid 文件发 SIGTERM（gnb 装了 handler，gnb.c:395），实际:\n%s", joined)
	}
	// 旧实现是 `gnb_ctl -k`，而 gnb_ctl 的 getopt 串是 "b:n:csa:o46h"
	// （gnb_ctl.c:102），long_options 里也没有 k —— 这个 flag 根本不存在
	if strings.Contains(joined, "gnb_ctl") {
		t.Fatalf("Stop 不该调 gnb_ctl -k（该 flag 不存在）:\n%s", joined)
	}
	for _, bad := range []string{"pkill", "killall"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("Stop 不该按名字杀进程（%s）:\n%s", bad, joined)
		}
	}
}

func TestGnbStopFailsCleanlyWithoutPidFile(t *testing.T) {
	d := NewGNBDriver(3001)
	d.ConfDir = t.TempDir()
	var calls []string
	recordingExec(d, &calls)
	if err := d.Stop(); err == nil {
		t.Fatal("没有 pid 文件时 Stop 必须报错，不能静默成功")
	}
	if len(calls) != 0 {
		t.Fatalf("没拿到 pid 就不该 exec 任何东西，实际: %v", calls)
	}
}

func TestGnbIsProcessAliveDoesNotLookForGnbEs(t *testing.T) {
	d := NewGNBDriver(3001)
	d.ConfDir = t.TempDir() // 无 pid 文件
	var calls []string
	recordingExec(d, &calls)

	if d.IsProcessAlive() {
		t.Fatal("没有 pid 文件时应判为 not alive")
	}
	// 旧实现是 `pgrep -f gnb_es` —— gnb_es 是上游的 endpoint scanner，不是主守护进程。
	// 用它会有两种错：本机跑着 gnb 却报 not alive；别处恰好有 gnb_es 时误报 alive。
	if strings.Contains(strings.Join(calls, "\n"), "gnb_es") {
		t.Fatalf("不该用 gnb_es 判活:\n%s", strings.Join(calls, "\n"))
	}
}

// gnbUsageOutput 是 `gnb_ctl` 缺 -b 时的真实输出（实测于 1.6.8）：
// 打印 usage 然后 **exit 0**。此前 IsAvailable() 就是被它骗过去的。
const gnbUsageOutput = `GNB Ctl
GNB version Dev 1.6.8  protocol version 1.6.0
Build Time [Sep 23 2026,16:39:09]
Copyright (C) 2019 gnbdev
Usage: gnb_ctl -b CTL_BLOCK [OPTION]
Command Summary:
  -b, --ctl-block           ctl block mapper file
`

// gnbCtlBlockError 是非 root 读 0600 root:root 的 gnb.map 时的真实输出，
// 同样 **exit 0**。
const gnbCtlBlockError = "open ctl block error [/home/arthur/mynet-stage/var/gnb/gnb.map]\n"

// gnbStatusOutput 是 `gnb_ctl -b <map> -c -s` 的真实输出形状（截取自 Phase 6 实测）。
const gnbStatusOutput = `conf->conf_dir[/home/arthur/mynet-stage/var/gnb]
node_num[2]
wan6_port[0]
wan4_port[0]

====================
node 2132596500330640
tun_ipv4 10.0.0.10
in  840 (840 bytes)
out 840 (840 bytes)
ipv4 Local node

====================
node 1005272783125838
tun_ipv4 10.0.0.16
in  840 (840 bytes)
out 840 (840 bytes)
ipv4 Direct Point to Point
`

func TestGnbCtlStatusPassesFullFlags(t *testing.T) {
	dir := t.TempDir()
	d := NewGNBDriver(3001)
	d.ConfDir = dir
	var calls []string
	d.Exec = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return []byte(gnbStatusOutput), nil
	}

	if !d.IsAvailable() {
		t.Fatal("有真状态输出时应判为可用")
	}
	got := strings.Join(calls, " ")
	// -b 必须指向**本实例**的 map：不带 -b 时 gnb_ctl 只打 usage 就 exit 0
	want := "gnb_ctl -b " + dir + "/gnb.map -c -s"
	if got != want {
		t.Fatalf("argv 不对\n got: %s\nwant: %s", got, want)
	}
}

func TestGnbCtlStatusRejectsUsageOutput(t *testing.T) {
	d := NewGNBDriver(3001)
	d.ConfDir = t.TempDir()
	d.Exec = func(string, ...string) ([]byte, error) { return []byte(gnbUsageOutput), nil }

	if d.IsAvailable() {
		t.Fatal("gnb_ctl 打印 usage 时退出码也是 0 —— 只看退出码会把「命令没干活」当成「驱动可用」")
	}
	if _, err := d.GetAllPeers(); err == nil {
		t.Fatal("GetAllPeers 必须报错，不能把 usage 文本当状态解析成 0 个 peer")
	}
}

func TestGnbCtlStatusRejectsCtlBlockError(t *testing.T) {
	d := NewGNBDriver(3001)
	d.ConfDir = t.TempDir()
	d.Exec = func(string, ...string) ([]byte, error) { return []byte(gnbCtlBlockError), nil }

	if d.IsAvailable() {
		t.Fatal("读不到 ctl block（gnb.map 是 0600 root:root）时退出码仍是 0，必须靠输出判")
	}
	_, err := d.GetAllPeers()
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "open ctl block error") {
		t.Fatalf("错误里应带上 gnb_ctl 的原话便于定位，got: %v", err)
	}
}

func TestGnbParseStatusFromRealCtlOutput(t *testing.T) {
	d := NewGNBDriver(3001)
	d.ConfDir = t.TempDir()
	d.Exec = func(string, ...string) ([]byte, error) { return []byte(gnbStatusOutput), nil }

	peers, err := d.GetAllPeers()
	if err != nil {
		t.Fatal(err)
	}
	// 头部 4 行 + 分隔行不属于任何 peer；两个 `node <uuid>` 段才是
	if len(peers) != 2 {
		t.Fatalf("应解析出 2 个 node 段，实际 %d：%+v", len(peers), peers)
	}
	var local, remote *domain.VPNPeer
	for _, p := range peers {
		if p.IsLocal {
			local = p
		} else {
			remote = p
		}
	}
	if local == nil || remote == nil {
		t.Fatalf("应各有 1 个本机与对端，got %+v", peers)
	}
	if remote.VirtualIP != "10.0.0.16" {
		t.Fatalf("对端 tun_ipv4 解析错: %q", remote.VirtualIP)
	}
	if !remote.IsOnline() {
		t.Fatalf("`ipv4 Direct Point to Point` 必须判为在线，got status=%q", remote.ConnectionStatus)
	}
	if local.IsOnline() {
		t.Fatal("本机那段是 `Local node`，不该被判成在线 peer")
	}
	if remote.RxBytes != 840 || remote.TxBytes != 840 {
		t.Fatalf("in/out 字节数解析错: rx=%d tx=%d", remote.RxBytes, remote.TxBytes)
	}
}
