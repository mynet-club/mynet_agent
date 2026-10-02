package mynet

import (
	"fmt"
	"os"
	"os/exec"
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

func cmdExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
