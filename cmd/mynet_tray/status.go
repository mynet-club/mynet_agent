package main

// 探针：问本机 API「你在不在 / 节点什么状态」。
// 只读，不写任何东西 —— 启停在 control.go。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// LocalAPI 默认本机 API（SSOT：mynetd/doc.go DefaultLocalAPI = 8096）。
// ⚠️ install 脚本曾写过 8711，那是漂移；托盘以 8096 为准，可被
// MYNET_LOCAL_API 环境变量覆盖。
const LocalAPI = "127.0.0.1:8096"

// Probe 结果。
type Probe struct {
	// Up API 是否响应（mynetd 本机面）。
	Up bool
	// NodeState /api/v1/node-status 的 state；Up=false 时为空。
	NodeState string
	// ProcessUp 节点/隧道进程是否在（node-status.process_up）。
	ProcessUp bool
	// OneLine 状态一言之。
	OneLine string
	// NodeInstalled 节点是否已安装（来自 node-status）。
	NodeInstalled bool
}

// Prober 探针接口 —— 测试注入替身。
type Prober interface {
	Probe() (Probe, error)
}

// HTTPProber 真探针。
type HTTPProber struct {
	Base string
	HC   *http.Client
}

func NewHTTPProber(base string) *HTTPProber {
	if base == "" {
		base = LocalAPI
	}
	return &HTTPProber{
		Base: base,
		HC:   &http.Client{Timeout: 2 * time.Second},
	}
}

// Probe GET /api/v1/node-status。API 无响应 → Up=false，err=nil（这是常态，不是错）。
func (p *HTTPProber) Probe() (Probe, error) {
	url := "http://" + p.Base + "/api/v1/node-status"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return Probe{}, err
	}
	// Origin 校验（安全红线：embed WebUI 必须校验 Origin/Host）。
	req.Header.Set("Origin", "http://"+p.Base)

	resp, err := p.HC.Do(req)
	if err != nil {
		// 连不上 = Stopped，不是错误
		return Probe{Up: false}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Probe{Up: true}, nil
	}

	var body struct {
		State     string `json:"state"`
		OneLine   string `json:"one_line"`
		ProcessUp bool   `json:"process_up"`
		Data      *struct {
			State     string `json:"state"`
			OneLine   string `json:"one_line"`
			ProcessUp bool   `json:"process_up"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Probe{Up: true}, nil
	}
	st := body.State
	ol := body.OneLine
	pu := body.ProcessUp
	if body.Data != nil {
		if st == "" {
			st = body.Data.State
		}
		if ol == "" {
			ol = body.Data.OneLine
		}
		if !pu {
			pu = body.Data.ProcessUp
		}
	}
	installed := st != "" && st != "UNINSTALLED"
	return Probe{
		Up:            true,
		NodeState:     st,
		ProcessUp:     pu,
		OneLine:       ol,
		NodeInstalled: installed,
	}, nil
}

// eventFromProbe 把探针结果映射成状态机事件。
//
// 关键：**节点没跑 ≠ 异常**。API 在但节点未跑（UNINSTALLED/INSTALLED/
// CONFIGURED/无 process_up）一律映射 EvDaemonDown → Stopped。
// 只有 RUNNING/DEGRADED/FAILED 才进对应稳态。
func eventFromProbe(p Probe) Event {
	if !p.Up {
		return EvDaemonDown
	}
	switch p.NodeState {
	case "RUNNING":
		if !p.ProcessUp {
			// 报 RUNNING 但进程没了 —— 当未启动，不当异常
			return EvDaemonDown
		}
		return EvNodeHealthy
	case "DEGRADED":
		if !p.ProcessUp {
			return EvDaemonDown
		}
		return EvNodeDegraded
	case "FAILED":
		// 进程都不在 = 没起来，显示「已停止」而不是「异常」
		if !p.ProcessUp {
			return EvDaemonDown
		}
		return EvNodeFailed
	default:
		// UNINSTALLED / INSTALLED / CONFIGURED / 空：节点未启动
		return EvDaemonDown
	}
}

// String 便于日志。
func (p Probe) String() string {
	if !p.Up {
		return "down"
	}
	return fmt.Sprintf("up state=%s", p.NodeState)
}
