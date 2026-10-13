package mynet

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		"vpn":      len(m.VPN),
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

func cmdExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
