package main

// 探针：问本机 API「你在不在 / 节点什么状态」。
//
// 只读，不写任何东西 —— 启停在 tray.go（经 mynetd 的 command / config 面）。
//
// 三件事必须分开，不能塌成一个布尔（D3/D4）：
//
//	Up=false          本机 API 无响应      → 服务不可用，用户该去查服务
//	Up=true,Valid=false 有响应但状态不可信 → 非200 / 信封非法 / 执行器 500
//	Up=true,Valid=true  拿到节点事实       → 按 NodeState 判连接态
//
// 旧版把三者全映射成「已停止」，于是 mynetd 没装、执行器崩了、返回 403
// 都显示成「隧道没连」，用户点连接只会失败，且看不出该做什么。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// LocalAPI 默认本机 API（SSOT：mynetd/doc.go DefaultLocalAPI = 8096）。
// ⚠️ install 脚本曾写过 8711，那是漂移；托盘以 8096 为准，可被
// MYNET_LOCAL_API 环境变量覆盖。
const LocalAPI = "127.0.0.1:8096"

// maxStatusBody node-status 含 peers 数组、route_conf_text、
// system_routes_text，正常也就几十 KB；4MB 是防异常响应的上界，
// 超过则判不可信（而不是把托盘内存吃光）。
const maxStatusBody = 4 << 20

// Probe 一次探针的结果。
type Probe struct {
	// Up 本机 API 是否有 HTTP 响应（连不上 = false）。
	Up bool
	// Valid 是否拿到可信的 {success:true,data:{…}} 信封。
	// Up=true 而 Valid=false 表示「服务在说话，但说的听不懂」：
	// 非200、执行器 500、JSON 非法、success=false、缺 data。
	Valid bool
	// NodeState node-status 的 state（wire 原值，不加工）。
	// 需要区分未安装/未配置/已停止时直接比这个字符串 —— 它是执行器权威，
	// 不再自己推导 installed 布尔（D18：推导出来的第二套判据必然漂）。
	NodeState string
	// ProcessUp 节点/隧道进程是否在（node-status.process_up）。
	ProcessUp bool
	// OneLine 状态一言之。⚠️ 未经清洗，进 UI 前必须过 safeText（D20）。
	OneLine string
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

// Probe GET /api/v1/node-status。
//
// 权威信封是 {success:true,data:{…}}（mynetd/localapi/node_status_handler.go:37）；
// 执行器失败返回 500 {success:false,message}（同文件 :22/:30-34）。
// 旧版还认一种「顶层直接放 state」的形状，全仓零测试覆盖也零产生者
// （D17：不具保留资格的兼容分支），已删。
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
		// 连不上不是错误，是常态（服务没起）
		return Probe{Up: false}, nil
	}
	defer resp.Body.Close()

	// 非200：403 invalid host（-api 指到非回环）、500 执行器失败、
	// 404 路由不存在 —— 都是「服务在，状态拿不到」。
	if resp.StatusCode != http.StatusOK {
		return Probe{Up: true}, nil
	}

	var env struct {
		Success bool `json:"success"`
		Data    *struct {
			State     string `json:"state"`
			OneLine   string `json:"one_line"`
			ProcessUp bool   `json:"process_up"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxStatusBody)).Decode(&env); err != nil {
		return Probe{Up: true}, nil
	}
	// success=false（执行器报错）或缺 data 都不可信：
	// 不能当成「节点未连接」，那会把故障伪装成一个正常状态。
	if !env.Success || env.Data == nil {
		return Probe{Up: true}, nil
	}
	return Probe{
		Up:        true,
		Valid:     true,
		NodeState: env.Data.State,
		ProcessUp: env.Data.ProcessUp,
		OneLine:   env.Data.OneLine,
	}, nil
}

// eventFromProbe 把探针结果映射成状态机事件。
//
// 关键：**节点没跑 ≠ 服务不可用 ≠ 状态不可信**（D3/D4）。
// 只有 Valid 的节点事实才能决定连接态；其余两种各自有专属事件，
// 让 FSM 落到 StateSvcDown 而不是 StateStopped。
//
// ⚠️ 这里产生的事件必须覆盖 FSM 转移表用到的每一种（D1/D2 的教训）：
// 旧版从不产生 EvDaemonUp，于是 Starting 只能靠超时退出、Failed 成终态，
// 而单测手工喂 EvDaemonUp 全绿 —— 测试证明不了产品。
func eventFromProbe(p Probe) Event {
	if !p.Up {
		return EvSvcDown
	}
	if !p.Valid {
		return EvSvcInvalid
	}
	switch p.NodeState {
	case "RUNNING":
		if !p.ProcessUp {
			// 报 RUNNING 但进程没了 —— 当未连接，不当异常
			return EvDaemonDown
		}
		return EvNodeHealthy
	case "DEGRADED":
		if !p.ProcessUp {
			return EvDaemonDown
		}
		return EvNodeDegraded
	case "FAILED":
		// 进程都不在 = 没起来，显示「未连接」而不是「连接异常」
		if !p.ProcessUp {
			return EvDaemonDown
		}
		return EvNodeFailed
	default:
		// STOPPED / UNINSTALLED / INSTALLED / CONFIGURED / 空：节点未连接
		return EvDaemonDown
	}
}

// String 便于日志。
func (p Probe) String() string {
	switch {
	case !p.Up:
		return "svc-down"
	case !p.Valid:
		return "svc-invalid"
	default:
		return fmt.Sprintf("up state=%s process_up=%v", p.NodeState, p.ProcessUp)
	}
}
