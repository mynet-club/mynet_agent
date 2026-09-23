package shared

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SystemSnapshot 是 mynet detector 的产出（system.json），双端消费：
//   - mynet apply  —— 决定 exec 哪些工具
//   - mynetd 本机态 —— WebUI 直读，不 fork
//
// 指纹 Fingerprint 用于「网卡/内核变化 → 自动重探测」：
// 消费方比较当前接口指纹与快照指纹，不匹配则触发 mynet detector 重跑。
type SystemSnapshot struct {
	GeneratedAt  time.Time `json:"generated_at"`
	Hostname     string    `json:"hostname"`
	OS           string    `json:"os"`
	Distribution string    `json:"distribution"`
	Kernel       string    `json:"kernel"`
	Architecture string    `json:"architecture"`
	// Interfaces 简表，用于指纹（名称+MAC+up/down）
	Interfaces   []IfaceFingerprint         `json:"interfaces"`
	Fingerprint  string                     `json:"fingerprint"`
	Capabilities map[string]bool            `json:"capabilities"`     // iptables/nft/uci/ip/ss/procd/systemd…
	Detail       map[string]json.RawMessage `json:"detail,omitempty"` // 各探测域原始结果
}

// IfaceFingerprint 指纹输入（不进指纹的字段可另放 Detail）
type IfaceFingerprint struct {
	Name  string `json:"name"`
	MAC   string `json:"mac"`
	State string `json:"state"`
}

// DefaultSnapshotPath $MYNET_HOME/var/system.json
func DefaultSnapshotPath() string {
	return filepath.Join(VarDir(), "system.json")
}

// VarDir 兼容 utils 包（避免 shared→utils 循环，这里内联最小实现）
func VarDir() string {
	if v := os.Getenv("MYNET_HOME"); v != "" {
		return filepath.Join(v, "var")
	}
	if v := os.Getenv("MYNET_DATA_HOME"); v != "" {
		return filepath.Join(v, "var")
	}
	// 与 shared/utils.UserBaseDir 保持一致的兜底
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".mynet", "var")
	}
	return "var"
}

// ComputeFingerprint 对接口列表做稳定哈希（按名称排序后拼接）
func ComputeFingerprint(ifaces []IfaceFingerprint) string {
	// 插入排序，避免引入 sort 依赖差异
	sorted := append([]IfaceFingerprint(nil), ifaces...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Name < sorted[j-1].Name; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	h := sha256.New()
	for _, i := range sorted {
		fmt.Fprintf(h, "%s|%s|%s\n", i.Name, i.MAC, i.State)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// WriteSnapshot 原子写 system.json
func WriteSnapshot(path string, snap *SystemSnapshot) error {
	if snap.Fingerprint == "" {
		snap.Fingerprint = ComputeFingerprint(snap.Interfaces)
	}
	if snap.GeneratedAt.IsZero() {
		snap.GeneratedAt = time.Now().UTC()
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadSnapshot 读 system.json
func ReadSnapshot(path string) (*SystemSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s SystemSnapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// NeedsRedetect 当前接口指纹与快照不一致时应重跑 detector
func NeedsRedetect(snap *SystemSnapshot, current []IfaceFingerprint) bool {
	if snap == nil {
		return true
	}
	return snap.Fingerprint != ComputeFingerprint(current)
}
