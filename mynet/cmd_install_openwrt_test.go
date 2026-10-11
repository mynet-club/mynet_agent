//go:build linux && openwrt

package mynet

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenWrtInitsEmbedded(t *testing.T) {
	if err := assertOpenWrtInitEmbedded(); err != nil {
		t.Fatalf("assertOpenWrtInitEmbedded: %v", err)
	}
	for _, name := range OpenWrtInitNames {
		body, ok := openWrtInit(name)
		if !ok {
			t.Fatalf("init %s missing", name)
		}
		if !strings.Contains(body, "USE_PROCD=1") {
			t.Fatalf("init %s must be procd (USE_PROCD=1)", name)
		}
		if !strings.Contains(body, "start_service()") {
			t.Fatalf("init %s missing start_service", name)
		}
		// 禁止 pkill/killall（安全红线）
		if strings.Contains(body, "pkill") || strings.Contains(body, "killall") {
			t.Fatalf("init %s must not pkill/killall", name)
		}
	}
}

func TestOpenWrtMynetdInitHasLuaExecutor(t *testing.T) {
	body, _ := openWrtInit("mynetd")
	if !strings.Contains(body, "MYNET_EXECUTOR=lua") {
		t.Fatal("mynetd init must set MYNET_EXECUTOR=lua")
	}
	if !strings.Contains(body, "8096") {
		t.Fatal("mynetd init must listen 8096")
	}
}

func TestOpenWrtMynetInitUsesProcdInstance(t *testing.T) {
	body, _ := openWrtInit("mynet")
	if !strings.Contains(body, "procd_open_instance") {
		t.Fatal("mynet init must use procd_open_instance for gnb")
	}
	if !strings.Contains(body, "respawn") {
		t.Fatal("mynet init must respawn gnb")
	}
	// stop 必须走 procd_kill + 精确清 gnb_es，而不是 Lua spawn/kill
	if !strings.Contains(body, "procd_kill") {
		t.Fatal("mynet init stop must procd_kill instance")
	}
	if !strings.Contains(body, "gnb_es") {
		t.Fatal("mynet init stop must clean gnb_es orphans")
	}
}

// 收敛守卫：procd 托管的 gnb 与「驱动侧 daemon 化 spawn 的 gnb」重叠时会留下
// 「procd 实例没 tun、孤儿占着 tun」。start 前必须按 confdir 收敛，且精确停。
func TestOpenWrtMynetInitConvergesStaleGnb(t *testing.T) {
	body, _ := openWrtInit("mynet")
	for _, want := range []string{"converge_stale_gnb", "procd_gnb_pids"} {
		if !strings.Contains(body, want) {
			t.Errorf("mynet init 缺同节点双 gnb 收敛守卫 %q", want)
		}
	}
	// 只按 confdir 匹配（多节点安全），且 pid+cmdline 精确停 —— 不得按名字杀。
	if !strings.Contains(body, `case "$cmd" in`) {
		t.Error("收敛守卫必须按 cmdline 归属判定，不能按进程名")
	}
	if strings.Contains(body, "killall") || strings.Contains(body, "pkill") {
		t.Error("收敛守卫不得按名字杀进程")
	}
}

// 双驱动：init 必须按 VPN_TYPE 分支 gnb | wireguard（架构支持）
func TestOpenWrtMynetInitDualDriver(t *testing.T) {
	body, _ := openWrtInit("mynet")
	for _, want := range []string{
		"VPN_TYPE",
		"wireguard",
		"start_wg_interface",
		"stop_wg_interface",
		"wg_conf_path",
		"wg-quick",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("mynet init missing dual-driver piece %q", want)
		}
	}
	// gnb 路径不能写死成唯一分支
	if !strings.Contains(body, "start_gnb_instance") {
		t.Fatal("mynet init must keep gnb branch")
	}
}

// OpenWrt：mynet install 是**服务安装**，不 copy 二进制（文件落地归 ipk）。
// 全平台同一契约：安装包放文件，install 只装服务。
func TestOpenWrtInstallIsServiceOnly(t *testing.T) {
	body, _ := openWrtInit("mynetd")
	if !strings.Contains(body, "USE_PROCD=1") {
		t.Fatal("mynetd init must be procd")
	}
	for _, path := range []string{
		"cmd_install_openwrt.go",
		"cmd_install.go",
		"cmd_install_win.go",
	} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := string(src)
		// 禁「把自身/同目录二进制 copy 进前缀」的调用点
		// （func copyFile(src, dst 的定义本身不算）
		for _, bad := range []string{
			"copyFile(self,",
			"copyFile(src, filepath",
			"copyFile(gnbBin,",
		} {
			if strings.Contains(s, bad) {
				t.Fatalf("%s must NOT %s — package install places files", path, bad)
			}
		}
		if !strings.Contains(s, "安装包") && !strings.Contains(s, "ipk") {
			t.Fatalf("%s should mention package-install responsibility", path)
		}
	}
}

// gnb 就位检查必须用 **OpenWrt 方言**路径（$home/driver/gnb/bin），不得拿别平台的
// /usr/local/bin 默认位判 —— 否则每次 install/opkg 升级都误报「服务会起不来」。
func TestOpenWrtGnbPresentCheck(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir()) // 隔离 PATH，排除系统 gnb 干扰
	if gnbPresentOpenWrt(home) {
		t.Fatal("空 home 且 PATH 无 gnb 时应判缺")
	}
	binDir := filepath.Join(home, "driver", "gnb", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "gnb"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !gnbPresentOpenWrt(home) {
		t.Fatal("$home/driver/gnb/bin/gnb 存在时应判就位")
	}
	src, err := os.ReadFile("cmd_install_openwrt.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "warnGnbMissing(gnbBin") {
		t.Fatal("OpenWrt 安装不得再用平台默认位 /usr/local/bin 误报 gnb 缺失")
	}
}

// OpenWrt 服务安装必须含 network/zone 配置（否则 enable 了也转发不通）
func TestOpenWrtInstallSetsNetworkZone(t *testing.T) {
	src, err := os.ReadFile("cmd_install_openwrt.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "setupOpenWrtNetworkZone") {
		t.Fatal("installOpenWrt must call setupOpenWrtNetworkZone")
	}
	if !strings.Contains(s, "network.mynet") || !strings.Contains(s, "firewall") {
		t.Fatal("setup must cover network.mynet + firewall zone")
	}
}

// ---- G-11 回归：用形状忠实的 stub uci 真跑 UCI 步骤序列 ----

// stubUci 是最小 uci 替身（show/add/set/get/commit），足以驱动 openWrtUciSteps。
// 真机才能跑真 uci；本 stub 验证**步骤序列的逻辑**：首装成功、重复执行幂等。
const stubUci = `#!/bin/sh
ST="${UCI_STATE:?}"
case "$1" in
  show) if [ -n "$2" ]; then grep -F "$2." "$ST" 2>/dev/null; else cat "$ST" 2>/dev/null; fi ;;
  add)
    n=$(wc -l < "$ST" 2>/dev/null | tr -d ' ')
    sec="cfg$(printf '%06d' $((n+1)))"
    echo "$2.$sec=$3" >> "$ST"; echo "$sec" ;;
  set)
    expr="$2"; key="${expr%%=*}"; val="${expr#*=}"
    cfg="${key%%.*}"; rest="${key#*.}"; sec="${rest%%.*}"; opt="${rest#*.}"
    if [ "$sec" = "$rest" ]; then
      awk -v k="$cfg.$sec" -F= '$1!=k' "$ST" > "$ST.t" 2>/dev/null; mv "$ST.t" "$ST" 2>/dev/null || :
      echo "$cfg.$sec=$val" >> "$ST"
    else
      awk -v k="$cfg.$sec.$opt" -F= '$1!=k' "$ST" > "$ST.t" 2>/dev/null; mv "$ST.t" "$ST" 2>/dev/null || :
      echo "$cfg.$sec.$opt='$val'" >> "$ST"
    fi ;;
  get)
    key="$2"; cfg="${key%%.*}"; rest="${key#*.}"; sec="${rest%%.*}"; opt="${rest#*.}"
    awk -v k="$cfg.$sec.$opt" -F= '$1==k { v=$2; sub(/^./,"",v); sub(/.$/,"",v); print v; f=1 } END{ exit (f?0:1) }' "$ST" ;;
  commit) : ;;
  *) exit 1 ;;
esac
`

func TestOpenWrtUciStepsFirstInstallThenIdempotent(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "uci.state")
	if err := os.WriteFile(state, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uci"), []byte(stubUci), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UCI_STATE", state)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	read := func() string { b, _ := os.ReadFile(state); return string(b) }
	run := func() {
		t.Helper()
		for _, c := range openWrtUciSteps("gnb_tun_16") {
			if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
				t.Fatalf("步骤 %v 失败: %v\n%s", c, err, out)
			}
		}
	}

	run() // 首装
	if n := strings.Count(read(), "=zone"); n != 1 {
		t.Fatalf("首装应建 1 个 zone，实际 %d：\n%s", n, read())
	}
	if n := strings.Count(read(), "=forwarding"); n != 2 {
		t.Fatalf("首装应建 2 条 forwarding，实际 %d：\n%s", n, read())
	}
	for _, want := range []string{"name='mynet'", "masq='1'", "src='lan'", "dest='mynet'", "src='mynet'", "dest='wan'", "device='gnb_tun_16'"} {
		if !strings.Contains(read(), want) {
			t.Fatalf("首装后缺 %q：\n%s", want, read())
		}
	}

	run() // 重跑必须幂等
	if n := strings.Count(read(), "=zone"); n != 1 {
		t.Fatalf("重跑不应新建 zone，实际 %d：\n%s", n, read())
	}
	if n := strings.Count(read(), "=forwarding"); n != 2 {
		t.Fatalf("重跑不应重复 forwarding，实际 %d：\n%s", n, read())
	}
}

// 结构防线：zone 步骤必须捕获 `uci add firewall zone` 的 section 名（旧实现丢进 /dev/null）。
func TestOpenWrtUciZoneStepCapturesSectionID(t *testing.T) {
	step := openWrtZoneStep("")
	if !strings.Contains(step, "s=$(uci add firewall zone)") {
		t.Fatal("zone 步骤必须捕获 `uci add firewall zone` 的 section 名，不能丢进 /dev/null")
	}
	if !strings.Contains(step, `-F"[.=]"`) {
		t.Fatal("section 名必须按 [.=] 切分（-F. 会把 =类型 粘进字段 → 幂等判据失效）")
	}
}

// G-9 回归：gnb_es 清理必须按**可验证归属**（exe 是本部署的 gnb_es + 父进程是本实例 gnb），
// 不得再用 PPID=1 —— 孤儿只说明「没有父进程」，**不证明属于本节点**，
// 据此清理会误杀同机上别的实例留下的 gnb_es。
func TestOpenWrtInitGnbEsCleanupUsesVerifiableOwnership(t *testing.T) {
	body, ok := openWrtInit("mynet")
	if !ok {
		t.Fatal("mynet init 未嵌入")
	}
	if strings.Contains(body, `"$ppid" = "1"`) {
		t.Fatal("stop_gnb_instance 不得用 PPID=1 判归属（孤儿 ≠ 本实例）")
	}
	for _, want := range []string{"/cmdline", "skip gnb_es"} {
		if !strings.Contains(body, want) {
			t.Fatalf("gnb_es 清理缺可验证归属判据 %q（应为 exe + 父进程本实例 gnb）", want)
		}
	}
}
