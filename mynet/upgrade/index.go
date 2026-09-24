package upgrade

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ctl 的分发协议是**两步**（此前 Go 侧只有一个「拉 apps.json 得到 artifacts」的
// 桩，而 apps.json 里根本没有 artifacts —— 所以那个形状从一开始就对不上）：
//
//	GET {base}/d/apps.json                  → 产品清单（products 是 **map**）
//	GET {base}{manifest_url}                → 该产品的 versions[*].artifacts[]
//
// 形状以 **ctl 服务端为准**（ManifestService::getAppsIndex / getManifest）：
//
//	apps.json   : products 是 map，每项只有 name/description/latest/manifest_url
//	              —— 没有 releases[]、没有 checksum、没有 platform/arch
//	manifest    : versions 是 map（版本号 → 该版本的全部 artifacts），
//	              每个 artifact 才有 os/arch/file/url/sha256
//
// 注意 spec/fixtures/apps/apps.json 那份 fixture 写的是「products 是数组、
// 每项带 releases[] 与 checksum」——那是手写的理想形状，与服务端不符。
// 按服务端改 Go 侧，并让测试打真端点（不再读 fixture 后 skip）。

// appsIndex ctl 的 /d/apps.json
type appsIndex struct {
	SchemaVersion string                 `json:"schema_version"`
	Products      map[string]appsProduct `json:"products"`
	// apps 是 v1 扁平格式（外部名 → manifest URL），旧客户端兼容用
	Apps map[string]string `json:"apps"`
}

type appsProduct struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Latest      string `json:"latest"`
	ManifestURL string `json:"manifest_url"`
}

// productManifest ctl 的 /d/{product}/manifest.json
type productManifest struct {
	SchemaVersion string                     `json:"schema_version"`
	Project       string                     `json:"project"`
	LatestStable  string                     `json:"latest_stable"`
	Versions      map[string]manifestVersion `json:"versions"`
}

type manifestVersion struct {
	Channel   string             `json:"channel"`
	Artifacts []manifestArtifact `json:"artifacts"`
}

type manifestArtifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	File   string `json:"file"`
	URL    string `json:"url"`
	Ext    string `json:"ext"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// productToComponent 把 ctl 的产品（apps.json 的键，即**外部名**）映射到组件。
//
// ⚠️ 命名在这一带很乱，映射必须显式而不能靠猜：
//   - `tui` 是**旧** Go 终端客户端，其外部别名是 `mynet`（ManifestService::ALIASES），
//     且 ctl 已把它标记 deprecated、successor 指向 `client`；
//   - `mynet_agent` 才是本仓（mynetd + mynet + mynet_gateway）对应的产品。
//
// 所以 **`mynet` 不映射到 ComponentMynet** —— 否则 `mynet update` 会去装那个
// 早已废弃的 TUI。这条是显式决定，不是遗漏。
var productToComponent = map[string]Component{
	"mynet_agent": ComponentMynet,
	"gnb":         ComponentGNB,
	"wireguard":   ComponentWireGuard,
}

// ignoredProducts 明确列出「看得见但有意不映射」的产品，便于排障时分辨
// 「没映射」与「没发布」。
var ignoredProducts = map[string]string{
	"mynet":  "旧 TUI 客户端的外部别名（已 deprecated），agent 用 mynet_agent",
	"tui":    "旧 TUI 客户端",
	"client": "Node.js 客户端（计划中）",
	"proxy":  "Mihomo 代理管理器",
	"lua":    "OpenWrt LuCI 插件",
	"utc":    "UDP→TCP 隧道转换器",
}

// HTTPDoer 便于测试注入
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// indexClient 拉分发索引用的 HTTP 客户端。
type indexClient struct {
	hc      HTTPDoer
	base    string
	timeout time.Duration
}

const (
	indexTimeout   = 30 * time.Second
	maxIndexBytes  = 8 << 20 // 索引是元数据，8MB 足够且防异常响应撑爆内存
	maxManifestNum = 64      // 单次 Fetch 最多拉多少个产品的 manifest
)

// newIndexClient 从索引基址（如 https://ctl.mynet.club）构造。
func newIndexClient(rawURL string, hc HTTPDoer) (*indexClient, error) {
	base := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if base == "" {
		return nil, fmt.Errorf("索引地址为空")
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return nil, fmt.Errorf("索引地址必须是 http(s)://，得到 %q", base)
	}
	if hc == nil {
		hc = &http.Client{Timeout: indexTimeout}
	}
	return &indexClient{hc: hc, base: base, timeout: indexTimeout}, nil
}

func (c *indexClient) getJSON(path string, out any) error {
	url := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		url = c.base + path
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("构造请求: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexBytes))
	if err != nil {
		return fmt.Errorf("读 %s 响应: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s 返回 %d: %s", url, resp.StatusCode, truncate(string(raw), 200))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("解析 %s: %w（原文 %s）", url, err, truncate(string(raw), 200))
	}
	return nil
}

// fetchAll 按两步协议拉取全部分发件。
func (c *indexClient) fetchAll() ([]Artifact, error) {
	var idx appsIndex
	if err := c.getJSON("/d/apps.json", &idx); err != nil {
		return nil, fmt.Errorf("拉 apps.json: %w", err)
	}

	keys := make([]string, 0, len(idx.Products))
	for k := range idx.Products {
		keys = append(keys, k)
	}
	// 稳定顺序，便于测试与排障
	sortStrings(keys)

	if len(keys) > maxManifestNum {
		keys = keys[:maxManifestNum]
	}

	var out []Artifact
	var firstErr error
	for _, key := range keys {
		comp, mapped := productToComponent[key]
		if !mapped {
			continue // 有意忽略的产品，见 ignoredProducts
		}
		p := idx.Products[key]
		if p.ManifestURL == "" {
			// 该产品尚未发布（getAppsIndex 会给无 release 的产品留 latest=null）
			continue
		}
		arts, err := c.fetchManifest(comp, p.ManifestURL)
		if err != nil {
			// 单个产品拉失败不应让整体失败——其他产品仍可用。
			// 但要把错误带出去，否则「某产品静默缺失」很难查。
			if firstErr == nil {
				firstErr = fmt.Errorf("产品 %s: %w", key, err)
			}
			continue
		}
		out = append(out, arts...)
	}

	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// fetchManifest 拉单个产品的 manifest 并展平成 []Artifact。
func (c *indexClient) fetchManifest(comp Component, manifestURL string) ([]Artifact, error) {
	var m productManifest
	if err := c.getJSON(manifestURL, &m); err != nil {
		return nil, err
	}

	var out []Artifact
	for version, mv := range m.Versions {
		for _, a := range mv.Artifacts {
			if a.URL == "" {
				continue
			}
			arch := a.Arch
			// manifest 的 arch 可能是 `amd64` 或带 variant 的 `armv7-softfp`；
			// Platform 保留 ctl 的原始 os/arch 组合，便于排障时对上 ctl 的矩阵。
			platform := a.OS
			if arch != "" {
				platform = a.OS + "/" + arch
			}
			out = append(out, Artifact{
				Component: comp,
				Version:   version,
				URL:       a.URL,
				Checksum:  normalizeChecksum(a.SHA256),
				Platform:  platform,
			})
		}
	}
	return out, nil
}

// normalizeChecksum 统一校验和表示：去掉可能的 `sha256:` 前缀，转小写。
func normalizeChecksum(s string) string {
	v := strings.TrimSpace(s)
	v = strings.TrimPrefix(v, "sha256:")
	return strings.ToLower(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
