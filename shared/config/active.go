package config

// mode.json 读写与 local / online 切换。
//
// 切换契约（调用方负责停隧道/Apply）：
//   1. 校验目标可切换
//   2. 写 mode.json（原子）
//   3. 返回新 ActiveTarget —— 执行器随后：停 → 清投影 → Apply → 起
//
// 这里**不**碰 driver/**，也不碰插件进程：纯配置态。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// Store 配置树读写（conf + mode.json）。
type Store struct {
	// Root 可测覆盖；空则用 utils.GetMyNetHome()。
	Root string
}

func (s *Store) root() string {
	if s.Root != "" {
		return s.Root
	}
	return utils.GetMyNetHome()
}

func (s *Store) modePath() string {
	if s.Root != "" {
		return filepath.Join(s.Root, "mode.json")
	}
	return utils.ModeFilePath()
}

// LoadMode 读 mode.json。不存在返回 (nil, nil)。
func (s *Store) LoadMode() (*domain.ModeFile, error) {
	raw, err := os.ReadFile(s.modePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return domain.DecodeModeFile(raw)
}

// SaveMode 原子写 mode.json（0600）。
func (s *Store) SaveMode(m *domain.ModeFile) error {
	if m == nil {
		return fmt.Errorf("mode 为空")
	}
	m.Schema = domain.ConfigSchemaVersion
	if !m.Mode.Valid() {
		return fmt.Errorf("非法 mode: %q", m.Mode)
	}
	if err := m.Active.Validate(m.Mode); err != nil {
		return err
	}
	raw, err := domain.EncodePretty(m)
	if err != nil {
		return err
	}
	return atomicWrite(s.modePath(), raw, 0o600)
}

// UseLocal 切到本地模式。
func (s *Store) UseLocal() (domain.ModeFile, error) {
	// 本地意图必须存在，否则切过去 Apply 会空转
	if _, err := s.LoadLocalIntent(); err != nil {
		return domain.ModeFile{}, fmt.Errorf("切换到 local 失败：%w", err)
	}
	m, err := domain.NewModeFile(domain.ModeLocal, domain.ActiveTarget{
		Driver: domain.DriverTypeGNB, // 覆盖为 intent 里的 driver
		Source: "conf/local",
	})
	if err != nil {
		return domain.ModeFile{}, err
	}
	// driver 以 intent 为准
	if li, err := s.LoadLocalIntent(); err == nil && li.Driver != "" {
		m.Active.Driver = li.Driver
	}
	if err := s.SaveMode(&m); err != nil {
		return domain.ModeFile{}, err
	}
	return m, nil
}

// UseOnline 切到某租户节点。profile 必须已在 conf/online/{tenant}/。
func (s *Store) UseOnline(tenantID, nodeID string) (domain.ModeFile, error) {
	if tenantID == "" || nodeID == "" {
		return domain.ModeFile{}, fmt.Errorf("切换到 online 需要 tenant_id 与 node_id")
	}
	prof, err := s.LoadOnlineProfile(tenantID)
	if err != nil {
		return domain.ModeFile{}, fmt.Errorf("切换到 online 失败：%w", err)
	}
	if prof.NodeID != "" && prof.NodeID != nodeID {
		// 允许同租户多节点时以请求为准；profile 是默认节点
	}
	src := "conf/online/" + tenantID
	if td, err := utils.OnlineTenantDir(tenantID); err == nil {
		_ = td
	}
	m, err := domain.NewModeFile(domain.ModeOnline, domain.ActiveTarget{
		TenantID: tenantID,
		NodeID:   nodeID,
		Driver:   prof.Driver,
		Source:   src,
	})
	if err != nil {
		return domain.ModeFile{}, err
	}
	if err := s.SaveMode(&m); err != nil {
		return domain.ModeFile{}, err
	}
	return m, nil
}

// ActiveSource 返回当前应读的配置目录（绝对路径）。
func (s *Store) ActiveSource() (string, *domain.ModeFile, error) {
	m, err := s.LoadMode()
	if err != nil {
		return "", nil, err
	}
	if m == nil {
		// 未初始化：约定落在 local（setup --local 会写全）
		return filepath.Join(s.root(), "conf", "local"), nil, nil
	}
	switch m.Mode {
	case domain.ModeLocal:
		return filepath.Join(s.root(), "conf", "local"), m, nil
	case domain.ModeOnline:
		td, err := domain.TenantDirName(m.Active.TenantID)
		if err != nil {
			return "", m, err
		}
		return filepath.Join(s.root(), "conf", "online", td), m, nil
	default:
		return "", m, fmt.Errorf("未知 mode: %s", m.Mode)
	}
}

// ── 本地意图 ─────────────────────────────────────────────────

// LoadLocalIntent conf/local/intent.json。
func (s *Store) LoadLocalIntent() (*domain.LocalIntent, error) {
	raw, err := os.ReadFile(filepath.Join(s.root(), "conf", "local", "intent.json"))
	if err != nil {
		return nil, err
	}
	return domain.DecodeLocalIntent(raw)
}

// SaveLocalIntent 写意图（0600）。
func (s *Store) SaveLocalIntent(li *domain.LocalIntent) error {
	if li == nil {
		return fmt.Errorf("intent 为空")
	}
	li.Schema = domain.ConfigSchemaVersion
	li.Mode = domain.ModeLocal
	if err := li.Validate(); err != nil {
		return err
	}
	raw, err := domain.EncodePretty(li)
	if err != nil {
		return err
	}
	dir := filepath.Join(s.root(), "conf", "local")
	if err := utils.SafeCreateDir(dir, 0o700); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "intent.json"), raw, 0o600)
}

// ── 联网 profile ─────────────────────────────────────────────

// OnlineProfilePath conf/online/{tenant}/profile.json。
func (s *Store) OnlineProfilePath(tenantID string) (string, error) {
	td, err := domain.TenantDirName(tenantID)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.root(), "conf", "online", td, "profile.json"), nil
}

// LoadOnlineProfile 读租户 profile。
func (s *Store) LoadOnlineProfile(tenantID string) (*domain.OnlineProfile, error) {
	p, err := s.OnlineProfilePath(tenantID)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return domain.DecodeOnlineProfile(raw)
}

// SaveOnlineProfile 写 profile（0600）。
func (s *Store) SaveOnlineProfile(p *domain.OnlineProfile) error {
	if p == nil {
		return fmt.Errorf("profile 为空")
	}
	p.Schema = domain.ConfigSchemaVersion
	p.Mode = domain.ModeOnline
	if err := p.Validate(); err != nil {
		return err
	}
	path, err := s.OnlineProfilePath(p.TenantID)
	if err != nil {
		return err
	}
	if err := utils.SafeCreateDir(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := domain.EncodePretty(p)
	if err != nil {
		return err
	}
	return atomicWrite(path, raw, 0o600)
}

// ListTenants conf/online/ 下已存在的租户 id。
func (s *Store) ListTenants() ([]string, error) {
	base := filepath.Join(s.root(), "conf", "online")
	ents, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if _, err := domain.TenantDirName(e.Name()); err != nil {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}

// ── 插件配置 ─────────────────────────────────────────────────

// LoadPlugin 从插件配置目录（…/plugins）读 {name}.json。
func (s *Store) LoadPlugin(pluginsDir, name string) (*domain.PluginConfig, error) {
	raw, err := os.ReadFile(filepath.Join(pluginsDir, name+".json"))
	if err != nil {
		return nil, err
	}
	return domain.DecodePluginConfig(raw)
}

// SavePlugin 写插件配置到 pluginsDir/{name}.json（0600）。
func (s *Store) SavePlugin(pluginsDir string, pc *domain.PluginConfig) error {
	if pc == nil {
		return fmt.Errorf("plugin config 为空")
	}
	if err := pc.Validate(); err != nil {
		return err
	}
	pc.Schema = domain.ConfigSchemaVersion
	if err := utils.SafeCreateDir(pluginsDir, 0o700); err != nil {
		return err
	}
	raw, err := domain.EncodePretty(pc)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(pluginsDir, pc.Plugin+".json"), raw, 0o600)
}

// ── 工具 ─────────────────────────────────────────────────────

// atomicWrite 先写临时文件再 rename，避免半截文件。
func atomicWrite(path string, body []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := utils.SafeCreateDir(dir, 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// MustJSON 调试用。
func MustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
