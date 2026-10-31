// Package tipskb —— tips/小助手知识库（tips-kb.json）的拉取、校验、合并与本机缓存。
//
// 契约 SSOT：mynet_ctl/docs/knowledge-base/spec/tips-kb-contract.md
// （§2 JSON 形状、§3.4 合并算法、§3.5 失败与回退）。
// 载荷 = manifest + chunks 全量；manifest.chunks 的 id 集是权威全量（空数组 = 撤回全部）。
// 缓存落盘与 shared/config/proxy_domain_baseline 同目录风格：conf/local/tips_kb.json，原子写。
package tipskb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Manifest 顶层 manifest（tips-kb.schema.json §顶层）。
type Manifest struct {
	Version     string  `json:"version"`
	GeneratedAt string  `json:"generated_at"`
	Lang        string  `json:"lang"`
	Chunks      []Chunk `json:"chunks"`
}

// Chunk 单一主题知识块。id 发布后不改语义、不复用（改名 = 删旧增新）。
type Chunk struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Tags   []string `json:"tags"`
	Body   string   `json:"body"`
	Source Source   `json:"source"`
	SHA256 string   `json:"sha256,omitempty"`
}

// Source 蒸馏出处（证据可查）。
type Source struct {
	File    string `json:"file"`
	Section string `json:"section"`
}

var (
	idPattern  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	shaPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// EmptyManifest 无任何缓存时的合法空载荷：空 chunks 同样有权威性（schema 形状合法）。
func EmptyManifest() *Manifest {
	return &Manifest{
		Version:     "empty",
		GeneratedAt: "1970-01-01T00:00:00Z",
		Lang:        "zh-CN",
		Chunks:      []Chunk{},
	}
}

// Path 缓存文件：与 proxy_domain_baseline.json 同目录，独立文件。
func Path(dir string) string {
	return filepath.Join(dir, "tips_kb.json")
}

// Load 读缓存。不存在 → (nil, nil)；解析失败或形状不合法 → (nil, err)，
// 调用方可据此重拉（不当作有效缓存）。
func Load(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(Path(dir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if err := validate(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Save 原子落盘（tmp+rename，0600）。不改调用方的切片；chunks 缺省写 [] 不写 null。
func Save(dir string, m *Manifest) error {
	out := *m
	if out.Chunks == nil {
		out.Chunks = []Chunk{}
	}
	raw, err := json.Marshal(&out)
	if err != nil {
		return err
	}
	p := Path(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(p+".tmp", raw, 0600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// Reconcile 按 chunk id 收敛（tips-kb-contract §3.4）：
//   - 对 in.Chunks 逐 id upsert（同 id 以载荷为准）；
//   - cur 中 id 不在 in.Chunks 的条目删除 —— id 集权威全量，无 tombstone；
//   - in.Chunks 为空数组 = 权威撤回全部（清空）；
//   - 幂等：同一载荷重复合并不改变有效集。
//
// 返回 manifest 的元数据以载荷为准；chunks 取载荷顺序。
func Reconcile(cur, in *Manifest) *Manifest {
	byID := map[string]Chunk{}
	if cur != nil {
		for _, c := range cur.Chunks {
			byID[c.ID] = c
		}
	}
	for _, c := range in.Chunks {
		byID[c.ID] = c // upsert：同 id 以载荷为准
	}
	out := &Manifest{
		Version:     in.Version,
		GeneratedAt: in.GeneratedAt,
		Lang:        in.Lang,
		Chunks:      []Chunk{},
	}
	seen := map[string]bool{}
	for _, c := range in.Chunks {
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		out.Chunks = append(out.Chunks, byID[c.ID])
	}
	// cur 中不在 seen 的 id 不进 out.Chunks —— 删除生效（含空数组清空全部）。
	return out
}

// validate 按 tips-kb.schema.json 要点校验载荷形状（字段合法性的客户端子集）：
// 必填齐全、generated_at 为 ISO 8601、lang 固定 zh-CN、id/tag kebab-case 且 id 全库唯一、
// tags 1–8 个、sha256 形状、chunks 为数组（可为空数组）。
func validate(m *Manifest) error {
	if m.Version == "" {
		return fmt.Errorf("tips-kb: version 为空")
	}
	if m.GeneratedAt == "" {
		return fmt.Errorf("tips-kb: generated_at 为空")
	}
	if _, err := time.Parse(time.RFC3339, m.GeneratedAt); err != nil {
		return fmt.Errorf("tips-kb: generated_at 非 ISO 8601: %v", err)
	}
	if m.Lang != "zh-CN" {
		return fmt.Errorf("tips-kb: lang=%q（v1 固定 zh-CN）", m.Lang)
	}
	if m.Chunks == nil {
		return fmt.Errorf("tips-kb: chunks 缺失（须为数组，可为空数组）")
	}
	seen := map[string]bool{}
	for i, c := range m.Chunks {
		if !idPattern.MatchString(c.ID) {
			return fmt.Errorf("tips-kb: chunks[%d].id %q 不合法", i, c.ID)
		}
		if seen[c.ID] {
			return fmt.Errorf("tips-kb: chunk id %q 重复", c.ID)
		}
		seen[c.ID] = true
		if c.Title == "" {
			return fmt.Errorf("tips-kb: chunk %q title 为空", c.ID)
		}
		if len(c.Tags) < 1 || len(c.Tags) > 8 {
			return fmt.Errorf("tips-kb: chunk %q tags 数量 %d 不在 1–8", c.ID, len(c.Tags))
		}
		for _, t := range c.Tags {
			if !idPattern.MatchString(t) {
				return fmt.Errorf("tips-kb: chunk %q tag %q 不合法", c.ID, t)
			}
		}
		if c.Body == "" {
			return fmt.Errorf("tips-kb: chunk %q body 为空", c.ID)
		}
		if c.Source.File == "" || c.Source.Section == "" {
			return fmt.Errorf("tips-kb: chunk %q source 不完整", c.ID)
		}
		if c.SHA256 != "" && !shaPattern.MatchString(c.SHA256) {
			return fmt.Errorf("tips-kb: chunk %q sha256 形状不对", c.ID)
		}
	}
	return nil
}
