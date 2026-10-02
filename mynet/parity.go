package mynet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// ParityItem 一项「对等配置」检查结果。
//
// 对等配置 = 两端必须一致才能互通的参数。不一致时小包可能仍通、
// 大包/TCP 静默丢（实测 zip 不一致），因此要在导入/恢复/本地配置时提醒。
type ParityItem struct {
	Key      string `json:"key"`
	Local    string `json:"local"`
	Remote   string `json:"remote,omitempty"`
	Source   string `json:"source,omitempty"` // cloud | expected | missing
	Match    bool   `json:"match"`
	Critical bool   `json:"critical"`
	Hint     string `json:"hint,omitempty"`
}

// gnbParityKeys 与对端必须对齐的键。判定依据：
//   - zip / zip-level：压缩帧格式。不一致 → 大包被丢（2026-09-27 实测）
//   - crypto：数据面加密算法，两端必须同算法
//   - safe-index：索引帧校验方式，必须一致
//   - passcode：若启用则两端相同
//   - multi-socket / unified-forwarding：行为相关，建议一致
var gnbParityKeys = []struct {
	Key      string
	Critical bool
	Hint     string
}{
	{"zip", true, "压缩必须两端一致，否则大包/TCP 会静默丢弃"},
	{"zip-level", true, "压缩级别建议一致"},
	{"crypto", true, "数据面加密算法必须两端相同"},
	{"safe-index", false, "索引校验建议两端一致"},
	{"passcode", true, "passcode 必须两端相同"},
	{"multi-socket", false, "建议与对端一致"},
	{"unified-forwarding", false, "建议与对端一致"},
}

// ParseNodeConfKV 解析 node.conf 为 map（键已 tolower）。
func ParseNodeConfKV(text string) map[string]string {
	out := map[string]string{}
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		fields := strings.Fields(ln)
		if len(fields) == 0 {
			continue
		}
		k := strings.ToLower(fields[0])
		v := ""
		if len(fields) > 1 {
			v = strings.Join(fields[1:], " ")
		}
		out[k] = v
	}
	return out
}

// LoadNodeConfKV 读当前激活节点的 node.conf。
// currentParity N6：当前节点 conf 的对等项检查（sync 后必查）。
// remote 为 nil 时按工作基线比对（zip/crypto 等 expected 值）。
func currentParity() []ParityItem {
	local, _, err := LoadNodeConfKV()
	if err != nil || local == nil {
		return nil
	}
	return CheckGNBParity(local, nil)
}

func LoadNodeConfKV() (map[string]string, string, error) {
	home := utils.GetMyNetHome()
	cands := []string{}
	// 常见位：driver/gnb/conf/<nodeId>/node.conf —— 从 mode.json 取 node
	if id := activeNodeID(); id != "" {
		cands = append(cands, filepath.Join(home, "driver", "gnb", "conf", id, "node.conf"))
	}
	// 兜底：任意 node.conf
	if ents, err := os.ReadDir(filepath.Join(home, "driver", "gnb", "conf")); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				cands = append(cands, filepath.Join(home, "driver", "gnb", "conf", e.Name(), "node.conf"))
			}
		}
	}
	for _, p := range cands {
		b, err := os.ReadFile(p)
		if err == nil {
			return ParseNodeConfKV(string(b)), p, nil
		}
	}
	return nil, "", os.ErrNotExist
}

// CheckGNBParity 对照本地 node.conf 与云端/期望值。
// remote 为云端 bundle.gnb.options（可为 nil）；nil 时用 working 默认。
func CheckGNBParity(local map[string]string, remote map[string]any) []ParityItem {
	// 工作正常的对端（10.182.236.180）实测基线 —— 云端 bundle 不下发 zip/crypto
	// safe-index 缺省 off（gnb 默认；历史可用配置也未写该键）
	fallback := map[string]string{
		"zip":                "auto",
		"zip-level":          "-1",
		"crypto":             "xor",
		"safe-index":         "off",
		"multi-socket":       "on",
		"unified-forwarding": "auto",
	}
	remoteKV := map[string]string{}
	remoteSrc := map[string]string{}
	for k, v := range remote {
		remoteKV[strings.ToLower(strings.ReplaceAll(k, "_", "-"))] = toConfOnOff(v)
		remoteSrc[strings.ToLower(strings.ReplaceAll(k, "_", "-"))] = "cloud"
	}
	items := make([]ParityItem, 0, len(gnbParityKeys))
	for _, spec := range gnbParityKeys {
		lv, lok := local[spec.Key]
		rv, rok := remoteKV[spec.Key]
		src := "missing"
		if !rok {
			if fb, ok := fallback[spec.Key]; ok {
				rv, rok = fb, true
				src = "expected"
			}
		} else {
			src = remoteSrc[spec.Key]
		}
		// crypto 缺省即 xor（gnb 默认）
		if spec.Key == "crypto" && lok && lv == "" {
			lv = "xor"
			lok = true
		}
		if spec.Key == "crypto" && !lok {
			lv = "xor"
			lok = true
		}
		it := ParityItem{
			Key:      spec.Key,
			Local:    lv,
			Remote:   rv,
			Source:   src,
			Critical: spec.Critical,
			Hint:     spec.Hint,
			Match:    lok && rok && normalizeConf(lv) == normalizeConf(rv),
		}
		if !lok {
			it.Local = ""
			it.Match = false
			it.Source = "missing"
		}
		// 双方都未配置 passcode = 都不用 passcode，视为一致
		if spec.Key == "passcode" && it.Local == "" && it.Remote == "" {
			it.Match = true
			it.Source = "none"
			it.Hint = "双方均未使用 passcode"
		}
		// safe-index：gnb 默认 off；云端 options 里的 true 是「能力/推荐」，
		// 历史可用配置未写该键也能互通 —— 缺省时不视为 critical 失败。
		if spec.Key == "safe-index" && !lok {
			it.Match = normalizeConf(rv) == "off" || rv == ""
			if !it.Match {
				it.Hint = spec.Hint + "（本机未写该键，gnb 默认 off）"
			}
		}
		items = append(items, it)
	}
	return items
}

func normalizeConf(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "1", "true", "yes":
		return "on"
	case "0", "false", "no":
		return "off"
	}
	return v
}

func toConfOnOff(v any) string {
	switch t := v.(type) {
	case bool:
		if t {
			return "on"
		}
		return "off"
	case string:
		return normalizeConf(t)
	default:
		return strings.ToLower(strings.TrimSpace(strings.TrimSpace(toStringAny(t))))
	}
}

func toStringAny(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// activeNodeID 从 mode.json 读激活节点（轻量，不引 config.Store 全套）。
func activeNodeID() string {
	b, err := os.ReadFile(filepath.Join(utils.GetMyNetHome(), "mode.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Active struct {
			NodeID string `json:"node_id"`
		} `json:"active"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return ""
	}
	return m.Active.NodeID
}
