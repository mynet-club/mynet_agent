package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent-V2/shared"
	"github.com/mynet-club/mynet_agent-V2/shared/domain"
)

// bundleStore 把 back 下发的 BundleEnvelope 落到 $MYNET_HOME/var/bundle.json。
//
// ── 为什么这件事归 mynetd ─────────────────────────────────────
// 拉 bundle 是**节点面通信**（签名 + 经 gateway 的 /v1/relay），属 mynetd 的职责
// （AGENTS.md §1.2：mynetd 只做通信，系统能力全在执行器）。而 `mynet` 是执行器，
// 它只**读**这个文件（mynet/cmd_refresh.go 的 loadBundleEnvelope）。
// 两边路径口径必须一致 —— 都用 shared.VarDir()，否则一个写这儿一个读那儿。
//
// ── 此前断在哪 ───────────────────────────────────────────────
// 全仓没有任何代码**写** bundle.json，于是 `mynet refresh-config --auto` 与
// `refresh-config --auto` 一律报「读 bundle 失败: …/var/bundle.json: no such
// file or directory」；gateway 推来的 bundle_changed 事件也只是去跑那个注定
// 失败的 refresh（V1-10①）。现在改成**先拉后刷**。
type bundleStore struct {
	path     string // var/bundle.json —— 执行器读的就是这个
	etagPath string // var/bundle.etag —— 条件请求用，避免每次全量下发
}

func newBundleStore() *bundleStore {
	dir := shared.VarDir()
	return &bundleStore{
		path:     filepath.Join(dir, "bundle.json"),
		etagPath: filepath.Join(dir, "bundle.etag"),
	}
}

// bundleFetcher 是 sync 需要的唯一能力，抽成函数好让测试注入假的上游。
// 返回 (code, etag, body, err)；code 为 304 时 body 为空是**正常**的。
type bundleFetcher func(ctx context.Context, ifNoneMatch string) (int, string, []byte, error)

// ETag 读回上次存下的 etag。读不到（首次运行 / 文件被清）就返回空串，
// 于是 sync 会发一次无条件请求 —— 这是正确的降级，不是错误。
func (s *bundleStore) ETag() string {
	raw, err := os.ReadFile(s.etagPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// Sync 拉一次配置包并落盘。返回人类可读的结果，供日志使用：
//
//	"updated"  —— 拿到新版本并已写盘
//	"unchanged"—— back 返 304，本地已是最新，**不动文件**
//
// 304 不是失败。把它当失败会让「配置没变」这件事在日志里看起来像故障。
func (s *bundleStore) Sync(ctx context.Context, fetch bundleFetcher) (string, error) {
	code, etag, body, err := fetch(ctx, s.ETag())
	if err != nil {
		return "", err
	}

	switch code {
	case http.StatusNotModified:
		return "unchanged", nil
	case http.StatusOK:
		if err := s.Save(body, etag); err != nil {
			return "", err
		}
		return "updated", nil
	default:
		return "", fmt.Errorf("拉取 bundle 返回 %d: %s", code, truncateBytes(body, 200))
	}
}

// Save 校验后原子落盘。
//
// ⚠️ **先校验再写**：back 出错时返回的是 JSON 错误体（`{"success":false,…}`）
// 或经 relay 时的 HTML 错误页。不校验就写，会把一份好配置换成垃圾，
// 而且下一次条件请求还会带着旧 etag 拿到 304 —— 于是节点永久卡在坏配置上。
//
// ⚠️ **先写 bundle.json 再写 etag**：反过来的话，写完 etag 崩溃会留下
// 「etag 是新的、bundle 是旧的」，下次 304 就再也修不回来了。
func (s *bundleStore) Save(body []byte, etag string) error {
	if err := validateEnvelope(body); err != nil {
		return fmt.Errorf("拒绝写盘（免得覆盖掉好配置）: %w；原文 %s", err, truncateBytes(body, 200))
	}
	if err := atomicWrite(s.path, body, 0o644); err != nil {
		return fmt.Errorf("写 %s: %w", s.path, err)
	}
	// etag 缺失不算致命：bundle 已经落盘了，下次多发一次全量请求而已。
	if etag != "" {
		if err := atomicWrite(s.etagPath, []byte(etag+"\n"), 0o644); err != nil {
			return fmt.Errorf("写 %s: %w", s.etagPath, err)
		}
	}
	return nil
}

// validateEnvelope 按 schema 的 required 逐字段核。
//
// ⚠️ 光调 domain.DecodeBundleEnvelope **不够**：它只要求「非空 + 合法 JSON」，
// 于是 back 出错时返的 `{"success":false,"error":"…"}` 会被当成合法 envelope
// 写进 var/bundle.json（实测就是这样漏过去的）。必须按 SSOT 校字段 ——
// spec/schemas/bundle-envelope-v2.schema.json 的 required 是
// schema_version / generation / checksum / generated_at / node / driver / network，
// driver 限 gnb|wireguard。
//
// 这里**不验 checksum 的值**：两端 canonicalization 若有细微差异，验值会让
// 节点拒绝所有配置包 —— 那比拿到一份没校验的配置严重得多。校验和的核对
// 属应用配置那一侧（mynet apply）的职责。
func validateEnvelope(raw []byte) error {
	env, err := domain.DecodeBundleEnvelope(raw)
	if err != nil {
		return err
	}
	if env.SchemaVersion < 1 {
		return fmt.Errorf("schema_version 缺失或非法（%d）—— 这不是 BundleEnvelope，像是 back 的错误体", env.SchemaVersion)
	}
	if strings.TrimSpace(env.Generation) == "" {
		return fmt.Errorf("generation 为空（schema required）")
	}
	if strings.TrimSpace(env.Checksum) == "" {
		return fmt.Errorf("checksum 为空（schema required）")
	}
	if strings.TrimSpace(env.GeneratedAt) == "" {
		return fmt.Errorf("generated_at 为空（schema required）")
	}
	if strings.TrimSpace(env.Node.ID) == "" {
		return fmt.Errorf("node.id 为空（schema required）")
	}
	switch env.Driver {
	case domain.VPNDriverType("gnb"), domain.VPNDriverType("wireguard"):
	default:
		return fmt.Errorf("driver = %q，schema 只允许 gnb|wireguard", env.Driver)
	}
	return nil
}

// atomicWrite 临时文件 + rename。与 shared.WriteSnapshot 同一套做法：
// 读者（mynet refresh-*）任何时刻看到的都是完整文件，不会读到半截 JSON。
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
