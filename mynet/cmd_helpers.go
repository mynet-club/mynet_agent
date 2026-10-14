package mynet

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// requireHome 改变状态的入口必须显式 MYNET_HOME（C3 / spec/paths.md §2）。
// 静默回退 exe/cwd 会写进错误的配置树 —— 五节点取证里 181/183/188 都栽过。
func requireHome() (string, int) {
	h, err := utils.RequireMyNetHome()
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return "", 1
	}
	return h, 0
}

func readSystemSnapshot() (*shared.SystemSnapshot, error) {
	return shared.ReadSnapshot(shared.DefaultSnapshotPath())
}

func collectMetricsBrief() map[string]any {
	m := &Metrics{CollectedAt: time.Now().UTC()}
	collectSystemMetrics(m)
	collectVPNMetrics(m)
	return map[string]any{
		"cpu":      m.System.CPUPercent,
		"mem_used": m.System.MemUsedMB,
		"load1":    m.System.Load1,
		"vpn":      countUpVPNIfaces(m.VPN), // 只数活口（down 格是缺口⑥的信号，不算「在用」）
	}
}

func goos() string   { return runtime.GOOS }
func goarch() string { return runtime.GOARCH }

// gnbConfRel gnb `-c` 的 home 相对目标（D4-E：节点唯一存放位 conf/<form>/<nodeId>）。
// form+nodeId 取 mode.json；节点未定时给 conf/local/default（setup 后重跑 install 归位）。
// 服务单元生成面（install / migrate-layout）负责让它与实际节点目录一致。
// 放无 build-tag 的本文件：迁移命令（全平台）与 install（分平台）都要用。
func gnbConfRel(home string) string {
	form, nodeID := "local", ""
	raw, err := os.ReadFile(filepath.Join(home, "conf", "mode.json"))
	if err != nil {
		raw, err = os.ReadFile(filepath.Join(home, "mode.json"))
	}
	if err == nil {
		var m struct {
			Mode   string `json:"mode"`
			Active struct {
				NodeID string `json:"node_id"`
			} `json:"active"`
		}
		if json.Unmarshal(raw, &m) == nil {
			if m.Mode == "online" {
				form = "online"
			}
			nodeID = m.Active.NodeID
		}
	}
	if nodeID == "" {
		nodeID = "default"
	}
	return filepath.Join("conf", form, nodeID)
}

// modeFileMissing home 还没有 mode.json（conf/ 与旧根位都查）——
// 身份未定时 gnbConfRel 只能回退 conf/<form>/default，单元里的 -c 是待归位的。
func modeFileMissing(home string) bool {
	for _, p := range []string{
		filepath.Join(home, "conf", "mode.json"),
		filepath.Join(home, "mode.json"),
	} {
		if _, err := os.Stat(p); err == nil {
			return false
		}
	}
	return true
}

// warnGnbUnitConfRelPending install 时身份未定的显式警告（缺口⑦）。
// 188 实测：install 先于 setup，单元按 gnbConfRel 回退写出
// `-c ${MYNET_HOME}/conf/local/default`，gnb 拿着错目录 crash-loop 50 次。
// 单元照写（install 的职责是装服务），但把「待归位」明说，别让它静默变坑。
func warnGnbUnitConfRelPending(home string) {
	fmt.Fprintf(os.Stderr, "⚠ mynet_gnb.service 的 -c 待 mynet setup 后归位："+
		"%s 尚无 mode.json，单元暂指 %s（mynet setup 后自动归位，或重跑 mynet install）\n",
		home, gnbConfRel(home))
}

// realignGnbUnitAfterSetup setup 写完 mode.json 后把已装服务单元的 `-c` 归位（缺口⑦）。
//
// 为什么挂在 setup：单元生成是覆盖写，而 install 可能先于 setup 跑（此时无
// mode.json，单元必坏）—— 身份定型的时刻在 setup，归位要跟着它走。
// macOS 的 launchd 不写 gnb plist（gnb 不进 launchd，plist 无内嵌 conf 路径，
// 由 TestLaunchdSpecsDoNotEmbedGnbConfPath 锁定）—— 无需归位；
// OpenWrt 走 init/UCI 另线。
func realignGnbUnitAfterSetup(home string) {
	n := rewriteGnbUnitConfRelFiles(home, gnbSystemdUnitPaths)
	if n == 0 {
		return
	}
	// daemon-reload 尽力而为：没有 systemctl / 无权限都只提示，不判失败 ——
	// 单元内容已归位，systemd 下次读它时自然生效。
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ systemctl daemon-reload 未执行（单元已归位，下次生效）: %v %s\n",
			err, strings.TrimSpace(string(out)))
	}
}

func cmdExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
