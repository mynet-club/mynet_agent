package localapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 自动缓存策略：读名称时缺条目或过期就静默拉一次；60s 内失败不重试（防打爆）。
const (
	nodeNamesTTL        = 10 * time.Minute
	nodeNamesRetryAfter = 60 * time.Second
)

var (
	nodeNamesMu        sync.Mutex
	lastNamesAttemptAt time.Time
)

// 节点显示名本地缓存：node_id → 名称。
// 目的：设备名一次拿到后本机永久保存，断网/会话失效也能显示名称，
// 只在显式刷新（/config/cloud 或 peers/names/refresh）时更新；文件丢失则重新学习。
type nodeNameCache struct {
	Names map[string]string `json:"names"`
}

func nodeNamesPath() string {
	home := os.Getenv("MYNET_HOME")
	if home == "" {
		home = "."
	}
	return filepath.Join(home, "var", "node_names.json")
}

func loadNodeNames() map[string]string {
	raw, err := os.ReadFile(nodeNamesPath())
	if err != nil {
		return map[string]string{}
	}
	var c nodeNameCache
	if json.Unmarshal(raw, &c) != nil || c.Names == nil {
		return map[string]string{}
	}
	return c.Names
}

func saveNodeNames(names map[string]string) error {
	if len(names) == 0 {
		return nil
	}
	p := nodeNamesPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(nodeNameCache{Names: names})
	if err != nil {
		return err
	}
	if err := os.WriteFile(p+".tmp", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// mergeNodeNames 从云端节点清单抽取 id→name 并入缓存（不删旧条目）。
func mergeNodeNames(data map[string]any) {
	nodes, _ := data["nodes"].([]any)
	if len(nodes) == 0 {
		return
	}
	cur := loadNodeNames()
	changed := false
	for _, item := range nodes {
		n, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := anyString(n["node_id"])
		if id == "" {
			id = anyString(n["id"])
		}
		name := anyString(n["node_name"])
		if name == "" {
			name = anyString(n["name"])
		}
		if name == "" {
			name = anyString(n["hostname"])
		}
		if id != "" && name != "" && cur[id] != name {
			cur[id] = name
			changed = true
		}
	}
	if changed {
		_ = saveNodeNames(cur)
	}
}

func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		// JSON 数字 ID（雪花 ID 超过 2^53 时已损精度，这里只做键匹配兜底）
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	default:
		return ""
	}
}

// registerNodeNameRoutes
//
//	GET  /api/v1/peers/names         本机缓存的 id→名称（不访问网络）
//	POST /api/v1/peers/names/refresh 拉云端并更新缓存
func (s *Server) registerNodeNameRoutes() {
	s.mux.HandleFunc("/api/v1/peers/names", getOnly(s.handleNodeNames))
	s.mux.HandleFunc("/api/v1/peers/names/refresh", s.withConfigMutation(s.handleNodeNamesRefresh))
}

// handleNodeNames 读取缓存；需要时**自动补齐**（缺 id / 过期），不要求用户点刷新。
//
//	?ids=1,2,3  当前页面关心的节点；缺名即触发一次自动拉取
func (s *Server) handleNodeNames(w http.ResponseWriter, r *http.Request) {
	names := loadNodeNames()
	need := namesEmptyOrStale() || missingRequested(names, r.URL.Query().Get("ids"))
	if need {
		names = s.autoRefreshNames(names)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    map[string]any{"names": names},
	})
}

func namesEmptyOrStale() bool {
	st, err := os.Stat(nodeNamesPath())
	if err != nil {
		return true
	}
	return time.Since(st.ModTime()) > nodeNamesTTL
}

func missingRequested(names map[string]string, idsParam string) bool {
	for _, id := range strings.Split(idsParam, ",") {
		id = strings.TrimSpace(id)
		if id != "" && names[id] == "" {
			return true
		}
	}
	return false
}

// autoRefreshNames 自动补缓存：有冷却，失败保留旧缓存。
func (s *Server) autoRefreshNames(cur map[string]string) map[string]string {
	nodeNamesMu.Lock()
	if time.Since(lastNamesAttemptAt) < nodeNamesRetryAfter {
		nodeNamesMu.Unlock()
		return cur
	}
	lastNamesAttemptAt = time.Now()
	nodeNamesMu.Unlock()

	data, err := s.opsJSON([]string{"config", "cloud", "--json"})
	if err != nil {
		return cur // 未登录/离线：静默保留旧名
	}
	mergeNodeNames(data)
	return loadNodeNames()
}

func (s *Server) handleNodeNamesRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "POST only"})
		return
	}
	data, err := s.opsJSON([]string{"config", "cloud", "--json"})
	if err != nil {
		status := http.StatusBadGateway
		if isAuthExpired(err.Error()) {
			status = http.StatusUnauthorized
		}
		writeJSON(w, status, map[string]any{"success": false, "message": err.Error(), "need_login": status == http.StatusUnauthorized})
		return
	}
	mergeNodeNames(data)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    map[string]any{"names": loadNodeNames()},
	})
}
