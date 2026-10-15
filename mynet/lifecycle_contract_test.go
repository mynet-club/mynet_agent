package mynet

import (
	"runtime"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// H0 生命周期合同 fixture —— 代码侧对照见
// mynet_ctl/docs/knowledge-base/spec/lifecycle-contract.md。
//
// 这里只锁两类东西：
//  1. 动作矩阵与已知差距台账的完整性/一致性（枚举漏项、ID 重复即红）；
//  2. 当前平台**实际符号**（prefix / 服务登记名 / unit 落盘目录）与合同是否一致。
//
// 纪律：不对"尚未实现"的行为断言期望值（契约 D3）；跨平台其它行只是合同数据，
// 仅在当前 GOOS 可达时比对真实符号。

type lifecycleOp struct {
	name   string // 动作名
	layer  string // A–F 动作层
	owner  string // 唯一写系统状态的主体
	dryRun bool   // 是否支持 --dry-run（零写入）
}

// 对应合同 §2。dryRun 与代码实际提供的 flag 必须一致。
var lifecycleOps = []lifecycleOp{
	{"install", "B", "mynet install（平台实现）", true},
	{"uninstall", "F", "mynet uninstall（平台实现）", true},
	{"service", "D", "mynet service → 平台/驱动适配", false},
	{"apply", "E", "mynet apply → 平台网络后端", true},
	{"update", "F", "升级协调器（mynet update）", true},
	{"driver-install", "F", "mynet driver install/upgrade", false},
}

func TestLifecycleOperationMatrix(t *testing.T) {
	seen := map[string]bool{}
	dry := map[string]bool{}
	for _, op := range lifecycleOps {
		if op.name == "" || op.owner == "" {
			t.Errorf("动作 %+v 缺 name/owner", op)
		}
		if op.layer < "A" || op.layer > "F" {
			t.Errorf("动作 %s 的 layer=%q 不在 A–F", op.name, op.layer)
		}
		if seen[op.name] {
			t.Errorf("动作名重复: %s", op.name)
		}
		seen[op.name] = true
		if op.dryRun {
			dry[op.name] = true
		}
	}
	// 代码里实际提供 --dry-run 的就是这四个（cmd_install / cmd_apply / cmd_update）。
	// 少一个说明 fixture 漏标，多一个说明有人加了假的 dry-run —— 两种都要拦。
	want := map[string]bool{"install": true, "uninstall": true, "apply": true, "update": true}
	for k := range want {
		if !dry[k] {
			t.Errorf("动作 %s 支持 --dry-run，fixture 未标记", k)
		}
	}
	for k := range dry {
		if !want[k] {
			t.Errorf("动作 %s 未提供 --dry-run，fixture 不该标记", k)
		}
	}
}

type knownGap struct {
	id        string
	operation string
	target    string // 关闭该差距的工作包：H1–H4
}

// 与合同 §5「现状与差距」表逐行对应。任务收口时删行，不要在别处补说明。
var knownGaps = []knownGap{
	{"G-6", "update", "H4"},   // locateBinaries 不认 .exe
	{"G-7", "update", "H4"},   // OpenWrt 未走 opkg
	{"G-10", "install", "H3"}, // Wintun 缺 DLL 空函数指针
	{"G-12", "update", "H4"},  // desktop 发行 workflow 输入缺失
	{"G-13", "install", "H1"}, // OpenWrt Ops 不满足 connect 合同
}

func TestLifecycleKnownGapsTracked(t *testing.T) {
	ops := map[string]bool{}
	for _, op := range lifecycleOps {
		ops[op.name] = true
	}
	ids := map[string]bool{}
	targets := map[string]bool{"H1": true, "H2": true, "H3": true, "H4": true}
	for _, g := range knownGaps {
		if g.id == "" || ids[g.id] {
			t.Errorf("差距 ID 空或重复: %q", g.id)
		}
		ids[g.id] = true
		if !targets[g.target] {
			t.Errorf("差距 %s 的 target=%q 不在 H1–H4", g.id, g.target)
		}
		if !ops[g.operation] {
			t.Errorf("差距 %s 引用了矩阵外的动作 %q", g.id, g.operation)
		}
	}
}

type registration struct {
	platform string
	manager  string
	service  string // 登记名（多个用逗号）
	unitDir  string // unit/plist/init 落盘目录（windows 为空）
	envPath  string // env 文件（darwin 为空：走 launchd EnvironmentVariables）
	prefix   string // 程序文件位（defaultPrefix）
	home     string // Store Root（PlatformDefaultHome）—— 与 prefix 不同概念
}

// 合同 §3 登记矩阵。service/unitDir/prefix/home 是目标值，必须与代码符号一致。
var registrationMatrix = []registration{
	{"linux", "systemd", "mynetd,mynet_gnb", "/etc/systemd/system", "/etc/mynet/mynetd.env", "/opt/mynet", "/opt/mynet"},
	{"darwin", "launchd", "com.mynet.mynetd", "/Library/LaunchDaemons", "", "/opt/mynet", "/opt/mynet"},
	{"windows", "scm", "mynetd", "", `C:\ProgramData\MyNet\mynetd.env`, `C:\Program Files\MyNet`, `C:\ProgramData\MyNet`},
	{"openwrt", "procd", "mynetd,mynet", "/etc/init.d", "/etc/mynet/mynetd.env", "/usr", "/etc/mynet"},
}

func registrationFor(goos string) (registration, bool) {
	for _, r := range registrationMatrix {
		if r.platform == goos {
			return r, true
		}
	}

	return registration{}, false
}

// TestPlatformRegistrationMatchesContract 只比对当前平台可达的真实符号。
// TestPlatformRegistrationMatchesContract 只比对当前平台可达的真实符号。
func TestPlatformRegistrationMatchesContract(t *testing.T) {
	row, ok := registrationFor(runtime.GOOS)
	if !ok {
		t.Fatalf("合同登记矩阵缺 %s 行", runtime.GOOS)
	}
	if got := defaultPrefix(); got != row.prefix {
		t.Errorf("defaultPrefix()=%q，合同=%q", got, row.prefix)
	}
	// home 与 prefix 是两回事：home 存身份/配置，prefix 放程序文件（G-14 的根因）。
	if got := utils.PlatformDefaultHome(); got != row.home {
		t.Errorf("PlatformDefaultHome()=%q，合同=%q", got, row.home)
	}
	switch runtime.GOOS {
	case "linux":
		if got := strings.Join(SystemdUnitNames, ","); got != row.service {
			t.Errorf("SystemdUnitNames=%q，合同=%q", got, row.service)
		}
		// gnb 别名必须落到 mynet_gnb，否则 install/uninstall 找不到对方写的文件
		if got := unitFileName("gnb"); got != "mynet_gnb.service" {
			t.Errorf("unitFileName(gnb)=%q，应落 mynet_gnb.service", got)
		}
	}
	// darwin 的 Label / plist 目录断言在 cmd_install_launchd_test.go（那些符号按 build tag 只在 darwin 编译）。
}
