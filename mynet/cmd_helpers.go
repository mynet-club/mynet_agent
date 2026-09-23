package mynet

import (
	"os/exec"
	"runtime"
	"time"

	"github.com/mynet-club/mynet_agent-V2/shared"
)

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
