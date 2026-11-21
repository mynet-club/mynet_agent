package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/mynet-club/mynet_agent/mynet/utc/endpoint"
	"github.com/mynet-club/mynet_agent/mynetd/localapi"
	"github.com/mynet-club/mynet_agent/shared/logger"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

func utcConfigPath() string {
	if path := os.Getenv("MYNET_UTC_CONFIG"); path != "" {
		return path
	}
	return filepath.Join(utils.GetMyNetHome(), "conf", "utc", "utcd.conf")
}

func utcFileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func startUTCEndpoint(ctx context.Context) func() {
	noop := func() {}
	explicit := os.Getenv("MYNET_UTC_CONFIG") != ""
	path := utcConfigPath()
	info, err := os.Lstat(path)
	if err != nil {
		if explicit || !os.IsNotExist(err) {
			logger.Error("UTC endpoint 配置不可读，未启动", "path", path, "err", err)
		}
		return noop
	}
	if !info.Mode().IsRegular() {
		logger.Error("UTC endpoint 配置不是普通文件，未启动", "path", path)
		return noop
	}
	file, err := os.Open(path)
	if err != nil {
		logger.Error("UTC endpoint 配置不可读，未启动", "path", path, "err", err)
		return noop
	}
	if err := file.Close(); err != nil {
		logger.Error("UTC endpoint 配置关闭失败，未启动", "path", path, "err", err)
		return noop
	}
	name := "utcd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(utils.PrefixBinDir(), name)
	childCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := endpoint.Run(childCtx, binary, path, func(format string, args ...any) {
			logger.Info("UTC endpoint: " + fmt.Sprintf(format, args...))
		}); err != nil && childCtx.Err() == nil {
			logger.Error("UTC endpoint 退出", "err", err)
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

type utcToggleState struct {
	Enabled bool `json:"enabled"`
}

// utcManager owns the local UTC acceleration toggle and supervises the single
// utcd child process configured by utcd.conf (ADR-0037 一端一配).
type utcManager struct {
	mu           sync.Mutex
	parent       context.Context
	statePath    string
	enabled      bool
	endpointStop func()
	running      bool
}

func newUTCManager(parent context.Context) *utcManager {
	return &utcManager{
		parent:    parent,
		statePath: filepath.Join(utils.LocalConfDir(), "utc_state.json"),
	}
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
	configured := utcFileExists(utcConfigPath())
	enabled, ok := m.loadIntent()
	if !ok {
		enabled = configured
	}
	m.mu.Lock()
	m.enabled = enabled
	m.mu.Unlock()
	if enabled && configured {
		m.startRuntime()
	}
}

func (m *utcManager) SetEnabled(enabled bool) (localapi.UTCLocalStatus, error) {
	if enabled && !utcFileExists(utcConfigPath()) {
		return m.Status(), errors.New("UTC 未配置：未找到 utcd.conf")
	}
	if err := m.saveIntent(enabled); err != nil {
		return m.Status(), fmt.Errorf("保存 UTC 开关状态失败: %w", err)
	}
	m.mu.Lock()
	m.enabled = enabled
	m.mu.Unlock()
	if enabled {
		m.startRuntime()
	} else {
		m.Stop()
	}
	return m.Status(), nil
}

func (m *utcManager) startRuntime() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running && utcFileExists(utcConfigPath()) {
		m.endpointStop = startUTCEndpoint(m.parent)
		m.running = true
	}
}

func (m *utcManager) Stop() {
	m.mu.Lock()
	stop := m.endpointStop
	m.endpointStop = nil
	m.running = false
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (m *utcManager) Status() localapi.UTCLocalStatus {
	m.mu.Lock()
	enabled := m.enabled
	running := m.running
	m.mu.Unlock()

	path := utcConfigPath()
	configured := utcFileExists(path)
	status := localapi.UTCLocalStatus{
		Enabled:        enabled,
		Configured:     configured,
		Running:        running,
		EdgeCandidates: []string{},
		Peers:          []localapi.UTCPeerState{},
	}
	if !configured {
		return status
	}
	status.ConfigPath = path
	summary, err := endpoint.ParseConfig(path)
	if err != nil {
		status.LastError = err.Error()
		return status
	}
	status.ConfigVersion = summary.ConfigVersion
	status.StateDir = summary.StateDir
	if len(summary.EdgeCandidates) > 0 {
		status.EdgeCandidates = summary.EdgeCandidates
	}
	for _, p := range summary.Peers {
		status.Peers = append(status.Peers, localapi.UTCPeerState{
			Name:        p.Name,
			TransportID: p.TransportID,
			UDPBind:     p.UDPBind,
			UDPTarget:   p.UDPTarget,
			PairID:      p.PairID,
			Side:        p.Side,
		})
	}
	return status
}
