package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/utc/hub"
	"github.com/mynet-club/mynet_agent/mynetd/localapi"

	"github.com/mynet-club/mynet_agent/mynet/driver" // GUARD-OK 组合根：UTC Hub 投影注入与 GNB 受管标记
	"github.com/mynet-club/mynet_agent/mynet/utc/hubd"
	"github.com/mynet-club/mynet_agent/shared/logger"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// startUTCHub runs the resident Hub pair runtime inside mynetd. A long-lived
// renewal loop cannot live behind the 60s executor command timeout, and a
// separate daemon would duplicate lifecycle/identity ownership.
func startUTCHub(ctx context.Context) {
	path := os.Getenv("MYNET_UTC_HUB_CONFIG")
	if path == "" {
		path = filepath.Join(utils.GetMyNetHome(), "conf", "utc", "hub.json")
	}
	if _, err := os.Lstat(path); err != nil {
		if !os.IsNotExist(err) {
			logger.Error("UTC Hub 配置不可读，运行时未启动", "path", path, "err", err)
		}
		return
	}
	config, err := hubd.Load(path)
	if err != nil {
		logger.Error("UTC Hub 配置无效，运行时未启动（fail-closed）", "path", path, "err", err)
		return
	}
	if config.Bootstrap != nil {
		bootstrapCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = hubd.Bootstrap(bootstrapCtx, &config)
		cancel()
		if err != nil {
			logger.Error("UTC Hub Gateway 引导失败，运行时未启动（fail-closed）",
				"err", err)
			return
		}
	}
	// Hubd owns the GNB projection: mark the instance managed before the first
	// publish and inject a reload hook so each new generation is actually read.
	gnbDriver := driver.NewGNBDriver(int64(config.Identity.LocalID))
	gnbDriver.ConfDir = config.ConfDir
	if err := gnbDriver.MarkUTCManaged(); err != nil {
		logger.Error("UTC Hub 无法标记受管实例，运行时未启动（fail-closed）",
			"err", err)
		return
	}
	config.Apply = func(applyCtx context.Context) error {
		_ = applyCtx
		return gnbDriver.Reload()
	}
	runtime, err := hubd.New(config)
	if err != nil {
		logger.Error("UTC Hub 运行时初始化失败", "path", path, "err", err)
		return
	}
	runtime.Logf = func(format string, args ...any) {
		logger.Info("UTC Hub: " + fmt.Sprintf(format, args...))
	}
	logger.Info("UTC Hub 运行时启动", "pairs", len(config.Pairs),
		"conf_dir", config.ConfDir)
	go func() {
		if err := runtime.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("UTC Hub 运行时退出", "err", err)
		}
	}()
}

type utcToggleState struct {
	Enabled bool `json:"enabled"`
}

// utcManager owns the local UTC acceleration toggle and supervises both the
// utcd endpoint child and the resident hubd pair runtime inside mynetd.
type utcManager struct {
	mu              sync.Mutex
	parent          context.Context
	statePath       string
	enabled         bool
	endpointStop    func()
	endpointRunning bool
	hubCancel       context.CancelFunc
	hubRunning      bool
}

func newUTCManager(parent context.Context) *utcManager {
	return &utcManager{
		parent:    parent,
		statePath: filepath.Join(utils.LocalConfDir(), "utc_state.json"),
	}
}

func utcEndpointConfigPath() string {
	if path := os.Getenv("MYNET_UTC_CONFIG"); path != "" {
		return path
	}
	return filepath.Join(utils.GetMyNetHome(), "conf", "utc", "utcd.conf")
}

func utcHubConfigPath() string {
	if path := os.Getenv("MYNET_UTC_HUB_CONFIG"); path != "" {
		return path
	}
	return filepath.Join(utils.GetMyNetHome(), "conf", "utc", "hub.json")
}

func utcFileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func (m *utcManager) loadIntent() (bool, bool) {
	raw, err := os.ReadFile(m.statePath)
	if err != nil {
		return false, false
	}
	var st utcToggleState
	if err := json.Unmarshal(raw, &st); err != nil {
		return false, false
	}
	return st.Enabled, true
}

func (m *utcManager) saveIntent(enabled bool) error {
	raw, err := json.Marshal(utcToggleState{Enabled: enabled})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(m.statePath, raw, 0o600)
}

func (m *utcManager) StartIfEnabled() {
	configured := utcFileExists(utcEndpointConfigPath()) || utcFileExists(utcHubConfigPath())
	enabled, ok := m.loadIntent()
	if !ok {
		enabled = configured
	}
	m.mu.Lock()
	m.enabled = enabled
	m.mu.Unlock()
	if enabled && configured {
		m.startRuntimes()
	}
}

func (m *utcManager) SetEnabled(enabled bool) (localapi.UTCLocalStatus, error) {
	if enabled && !utcFileExists(utcEndpointConfigPath()) && !utcFileExists(utcHubConfigPath()) {
		return m.Status(), errors.New("UTC 未配置：未找到 utcd.conf 或 hub.json")
	}
	if err := m.saveIntent(enabled); err != nil {
		return m.Status(), fmt.Errorf("保存 UTC 开关状态失败: %w", err)
	}
	m.mu.Lock()
	m.enabled = enabled
	m.mu.Unlock()
	if enabled {
		m.startRuntimes()
	} else {
		m.Stop()
	}
	return m.Status(), nil
}

func (m *utcManager) startRuntimes() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.endpointRunning && utcFileExists(utcEndpointConfigPath()) {
		m.endpointStop = startUTCEndpoint(m.parent)
		m.endpointRunning = true
	}
	if !m.hubRunning && utcFileExists(utcHubConfigPath()) {
		hubCtx, cancel := context.WithCancel(m.parent)
		m.hubCancel = cancel
		m.hubRunning = true
		startUTCHub(hubCtx)
	}
}

func (m *utcManager) Stop() {
	m.mu.Lock()
	epStop := m.endpointStop
	hubCancel := m.hubCancel
	m.endpointStop = nil
	m.endpointRunning = false
	m.hubCancel = nil
	m.hubRunning = false
	m.mu.Unlock()
	if epStop != nil {
		epStop()
	}
	if hubCancel != nil {
		hubCancel()
	}
}

func (m *utcManager) Status() localapi.UTCLocalStatus {
	m.mu.Lock()
	enabled := m.enabled
	epRunning := m.endpointRunning
	hubRunning := m.hubRunning
	m.mu.Unlock()

	epPath := utcEndpointConfigPath()
	hubPath := utcHubConfigPath()
	status := localapi.UTCLocalStatus{
		Enabled: enabled,
		Endpoint: localapi.UTCEndpointState{
			Configured: utcFileExists(epPath),
			Running:    epRunning,
		},
		Hub: localapi.UTCHubState{
			Configured: utcFileExists(hubPath),
			Running:    hubRunning,
			Pairs:      []localapi.UTCPairState{},
		},
	}
	if status.Endpoint.Configured {
		status.Endpoint.ConfigPath = epPath
	}
	if status.Hub.Configured {
		status.Hub.ConfigPath = hubPath
		if cfg, err := hubd.Load(hubPath); err == nil {
			status.Hub.Scope = cfg.Scope
			status.Hub.TransportID = cfg.TransportID
			status.Hub.AccountID = cfg.AccountID
			for _, pair := range cfg.Pairs {
				mode := "A-D"
				if pair.Mode == hub.PeerModeAS {
					mode = "A-S"
				}
				entry := localapi.UTCPairState{
					PairID: pair.PairID,
					PeerID: pair.PeerID,
					Side:   pair.Side,
					Mode:   mode,
				}
				if verified, err := hubd.LoadVerified(pair.VerifiedPath); err == nil {
					entry.Applied = true
					entry.PairGen = verified.PairGen
					entry.LeaseEpoch = verified.LeaseEpoch
					entry.PlacementEpoch = verified.PlacementEpoch
					entry.EdgeID = verified.EdgeID
					entry.ValidUntil = verified.ValidUntil
					entry.ObservedAt = verified.ObservedAt
				}
				status.Hub.Pairs = append(status.Hub.Pairs, entry)
			}
		} else {
			status.Hub.LastError = err.Error()
		}
	}
	return status
}
