package tipskb

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// 载荷上限与域名清单拉取同口径（1 MiB）。
const maxPayloadBytes = 1 << 20

// CTLBaseURL 与域名清单拉取同口径（MYNET_APPS_INDEX，缺省 https://ctl.mynet.club），
// 目录 /d/kb/（tips-kb-contract §3.2）。不发明新配置项。
func CTLBaseURL() string {
	base := os.Getenv("MYNET_APPS_INDEX")
	if base == "" {
		base = "https://ctl.mynet.club"
	}
	return strings.TrimRight(base, "/") + "/d/kb/"
}

// Refresh 完整拉取 ctl 的 tips-kb.json 与校验和，合并后原子落盘（§3.4）。
//
//   - 校验全文 sha256（sidecar 或 X-KB-Checksum 头），不匹配**丢弃本次载荷**；
//   - id-keyed reconcile 后写缓存（空数组 = 权威撤回全部；幂等）；
//   - 拉取失败/超时/校验失败：**不碰缓存** —— 保留此前成功缓存，永不清空等重拉（§3.5）。
//
// ctlBase 形如 https://…/d/kb/；dir 为缓存目录（与 proxy_domain_baseline 同位）。
func Refresh(ctx context.Context, client *http.Client, ctlBase, dir string) error {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ctlBase+"tips-kb.json", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxPayloadBytes+1))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ctl tips-kb.json: HTTP %d", resp.StatusCode)
	}
	if readErr != nil {
		return readErr
	}
	if len(raw) > maxPayloadBytes {
		return fmt.Errorf("ctl tips-kb.json exceeds %d bytes", maxPayloadBytes)
	}

	// 校验和来源：sidecar 优先，缺则响应头 X-KB-Checksum（§3.4 ①）。
	expected := ""
	if sum, err := fetchSidecarChecksum(ctx, client, ctlBase); err == nil {
		expected = sum
	}
	if expected == "" {
		expected = strings.TrimSpace(strings.ToLower(resp.Header.Get("X-KB-Checksum")))
	}
	if expected == "" {
		return fmt.Errorf("tips-kb: 无校验和（sidecar 与 X-KB-Checksum 均缺），丢弃本次载荷")
	}
	if !shaPattern.MatchString(expected) {
		return fmt.Errorf("tips-kb: 校验和形状不对，丢弃本次载荷")
	}
	actual := fmt.Sprintf("%x", sha256.Sum256(raw))
	if actual != expected {
		return fmt.Errorf("tips-kb: 全文 sha256 不匹配（%s… != %s…），丢弃本次载荷", actual[:12], expected[:12])
	}

	var in Manifest
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("tips-kb: 载荷解析失败: %w", err)
	}
	if err := validate(&in); err != nil {
		return err
	}
	cur, err := Load(dir)
	if err != nil {
		cur = nil // 本地缓存不可读/形状坏：以本次通过校验的载荷为准
	}
	return Save(dir, Reconcile(cur, &in))
}

// fetchSidecarChecksum 拉 tips-kb.json.sha256，格式 `<sha256>  tips-kb.json\n`。
func fetchSidecarChecksum(ctx context.Context, client *http.Client, ctlBase string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ctlBase+"tips-kb.json.sha256", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ctl tips-kb.json.sha256: HTTP %d", resp.StatusCode)
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 1 || !shaPattern.MatchString(fields[0]) {
		return "", fmt.Errorf("ctl tips-kb.json.sha256: 格式不对（want <sha256>  tips-kb.json）")
	}
	if len(fields) >= 2 && fields[1] != "tips-kb.json" {
		return "", fmt.Errorf("ctl tips-kb.json.sha256: 指向 %q，非 tips-kb.json", fields[1])
	}
	return fields[0], nil
}
