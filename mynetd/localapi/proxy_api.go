package localapi

// WebUI 本地配置 proxy：双控合并 + 执行器落地 + 回写云端。
//
//	GET  /api/v1/plugin/proxy  → effective + local/cloud + writeback 状态
//	POST /api/v1/plugin/proxy  → ApplyLocal → 执行器 plugin apply → 异步回写
//
// 回写失败不回滚本地（Dirty 保留），下次 POST 或重试任务再推。
//
// B2：本文件**不 import shared/config / mynet**——读写经 ProxyLoad/Save、
// 插件投影经 ProxySyncPlugin（组合根注入）。localapi 只做 HTTP 壳。

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// ProxyIntentLoader / Saver — 双控意图读写（注入，避免 localapi 碰配置文件格式）。
type ProxyIntentLoader func(dir string) (*domain.ProxyIntentState, error)
type ProxyIntentSaver func(dir string, st *domain.ProxyIntentState) error

var errProxyStoreNotReady = errors.New("proxy 状态读写未装配（组合根未注入）")

// ProxyWritebackFunc 把本地意图推到云端（node-face 签名 → gateway → back）。
// 返回云端新的 revision（>0）；失败返回 error，状态保持 Dirty。
type ProxyWritebackFunc func(policy domain.ProxyPolicy, revision int64) (cloudRevision int64, err error)

// ProxyConfDir 双控状态目录。由 mynetd 装配注入（通常为 conf/local 或 active source）。
// 空则用默认 conf/local。

func (s *Server) registerProxyRoutes() {
	s.mux.HandleFunc("/api/v1/plugin/proxy", s.handlePluginProxy)
}

type proxyStatusBody struct {
	Effective *domain.StampedProxyPolicy `json:"effective,omitempty"`
	Local     *domain.StampedProxyPolicy `json:"local,omitempty"`
	Cloud     *domain.StampedProxyPolicy `json:"cloud,omitempty"`
	Status    domain.ProxyWriteback      `json:"status"`
	Describe  string                     `json:"describe"`
}

func (s *Server) loadProxyState() (*domain.ProxyIntentState, string, error) {
	dir := s.cfg.ProxyConfDir
	if dir == "" {
		// 必须用 MYNET_HOME 下的绝对路径：守护进程 cwd 往往不是数据根，
		// 相对 conf/local 会 mkdir 失败（read-only / 错目录）。
		dir = utils.LocalConfDir()
	}
	if s.ProxyLoad == nil {
		return nil, dir, errProxyStoreNotReady
	}
	st, err := s.ProxyLoad(dir)
	return st, dir, err
}

func (s *Server) saveProxyState(dir string, st *domain.ProxyIntentState) error {
	if s.ProxySave == nil {
		return errProxyStoreNotReady
	}
	return s.ProxySave(dir, st)
}

func (s *Server) handlePluginProxy(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		st, _, err := s.loadProxyState()
		if err != nil {
			writeJSON(w, 500, map[string]any{"success": false, "message": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"success": true, "data": proxyStatusBody{
			Effective: st.Effective(),
			Local:     st.Local,
			Cloud:     st.Cloud,
			Status:    st.Status,
			Describe:  st.Describe(),
		}})
	case http.MethodPost:
		s.postPluginProxy(w, r)
	default:
		writeJSON(w, 405, map[string]any{"success": false, "message": "GET|POST only"})
	}
}

func (s *Server) postPluginProxy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Policy json.RawMessage `json:"policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"success": false, "message": "bad json"})
		return
	}
	var p domain.ProxyPolicy
	if err := json.Unmarshal(body.Policy, &p); err != nil {
		writeJSON(w, 400, map[string]any{"success": false, "message": "policy 非法: " + err.Error()})
		return
	}

	st, dir, err := s.loadProxyState()
	if err != nil {
		writeJSON(w, 500, map[string]any{"success": false, "message": err.Error()})
		return
	}
	now := time.Now()
	if err := st.ApplyLocal(p, now); err != nil {
		writeJSON(w, 400, map[string]any{"success": false, "message": err.Error()})
		return
	}

	// 双轨合一：策略写入后投影到 plugins/proxy.json
	if st.Local != nil && s.ProxySyncPlugin != nil {
		_ = s.ProxySyncPlugin(&st.Local.Policy)
	}

	// 1) 本地生效：执行器 plugin apply（mynetd 只派发，不自己动系统）
	if s.Ops != nil {
		if _, err := s.Ops([]string{"plugin", "apply", "proxy"}); err != nil {
			// 本地生效失败也要留下状态（intent 已记），但要报出来
			st.MarkWritebackFail(err, now)
			_ = s.saveProxyState(dir, st)
			writeJSON(w, 500, map[string]any{
				"success": false,
				"message": "本地生效失败: " + err.Error(),
				"data":    st.Describe(),
			})
			return
		}
	}

	// 1b) 刷周边：client 分流的 nft/ipset/ip rule/table 都在 `mynet apply` 里，写意图 +
	// 驱动投影之后必须补这一次，否则页面开关"看起来生效、周边没装"，要等下一次
	// bundle/reload 才落地（2026-10-11 实测的时通时断来源之一）。
	// ⚠️ 用 `--proxy-only`：开/关代理只该动它自己那套（fwmark 规则 + table 200 + 打标
	// 集合），**不碰 OS 路由/防火墙/接口** —— 那是另一关注点，重刷多余且会惊动路由面。
	if s.Ops != nil {
		if _, err := s.Ops([]string{"apply", "--proxy-only"}); err != nil {
			st.MarkWritebackFail(err, now)
			_ = s.saveProxyState(dir, st)
			writeJSON(w, 500, map[string]any{
				"success": false,
				"message": "周边刷新失败（intent 已记）: " + err.Error(),
				"data":    st.Describe(),
			})
			return
		}
	}

	// 2) 回写云端（失败不回滚）。LocalOnly = 仅本机 proxy，故意不推云端。
	if s.ProxyWriteback != nil && !p.LocalOnly {
		rev, err := s.ProxyWriteback(st.Local.Policy, st.Local.Revision)
		if err != nil {
			st.MarkWritebackFail(err, time.Now())
			_ = s.saveProxyState(dir, st)
			writeJSON(w, 200, map[string]any{
				"success": true,
				"message": "本地已生效；回写云端失败，已标 dirty",
				"data": proxyStatusBody{
					Effective: st.Effective(),
					Local:     st.Local,
					Cloud:     st.Cloud,
					Status:    st.Status,
					Describe:  st.Describe(),
				},
			})
			return
		}
		st.MarkWritebackOK(rev, time.Now())
	}

	if err := s.saveProxyState(dir, st); err != nil {
		writeJSON(w, 500, map[string]any{"success": false, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"success": true,
		"message": "已生效",
		"data": proxyStatusBody{
			Effective: st.Effective(),
			Local:     st.Local,
			Cloud:     st.Cloud,
			Status:    st.Status,
			Describe:  st.Describe(),
		},
	})
}

// ApplyCloudIntent 云端 envelope 到达时合并出口许可（后覆盖先），返回是否该重投影。
// 读写经注入的 store，保持 localapi 不碰 shared/config。
// D1 收窄：云端只带许可（enabled + acl），落 Cloud 轨时投影为 server 角色策略。
func ApplyCloudIntent(load ProxyIntentLoader, save ProxyIntentSaver, dir string, p domain.ProxyPermission, revision int64) (needApply bool, st *domain.ProxyIntentState, err error) {
	st, err = load(dir)
	if err != nil {
		return false, nil, err
	}
	before := ""
	if eff := st.Effective(); eff != nil {
		before = string(eff.Source) + "#" + itoa64(eff.Revision)
	}
	if err := st.ApplyCloud(p.AsCloudPolicy(), revision, time.Now()); err != nil {
		return false, nil, err
	}
	after := ""
	if eff := st.Effective(); eff != nil {
		after = string(eff.Source) + "#" + itoa64(eff.Revision)
	}
	if err := save(dir, st); err != nil {
		return false, nil, err
	}
	return before != after, st, nil
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}
