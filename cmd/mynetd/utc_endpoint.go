package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

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

// PairSyncer writes one local pair intent back to the cloud and returns the
// authorization outcome. nil means the cloud path is not wired yet: intents
// stay "pending" and never flip gnb to mode=2 on their own.
type PairSyncer func(ctx context.Context, peerID uint64, on bool) (result, reason string, err error)

// utcManager owns the local UTC acceleration toggle and supervises the single
// utcd child process configured by utcd.conf (ADR-0037 一端一配).
type utcManager struct {
	mu           sync.Mutex
	parent       context.Context
	statePath    string
	enabled      bool
	endpointStop func()
	running      bool
	pairSyncer   PairSyncer
}

func newUTCManager(parent context.Context) *utcManager {
	return &utcManager{
		parent:    parent,
		statePath: filepath.Join(utils.LocalConfDir(), "utc_state.json"),
	}
}

// SetPairSyncer injects the cloud write-back path (composition root).
func (m *utcManager) SetPairSyncer(sync PairSyncer) {
	m.mu.Lock()
	m.pairSyncer = sync
	m.mu.Unlock()
}

// stateDir resolves the utcd state directory (pairs/bridge.status live there).
func (m *utcManager) stateDir() string {
	if summary, err := endpoint.ParseConfig(utcConfigPath()); err == nil {
		if dir := strings.TrimSpace(summary.StateDir); dir != "" {
			return dir
		}
	}
	for _, dir := range endpoint.DiscoverUTCDStateDirs(utcConfigPath(), utils.GetMyNetHome()) {
		return dir
	}
	return ""
}

// Pairs merges local intents, the gnb utc-v2 projection and the runtime
// lease/bridge state for every peer the node knows about.
func (m *utcManager) Pairs() ([]localapi.UTCPairState, error) {
	home := utils.GetMyNetHome()
	stateDir := m.stateDir()
	now := uint64(time.Now().Unix())
	paths := endpoint.DiscoverUTCConfPaths(home)
	if len(paths) == 0 {
		// No projection yet: still surface local intents as pending.
		paths = []string{filepath.Join(home, "conf", "local", ".utc.conf.absent")}
	}
	out := make([]localapi.UTCPairState, 0, 4)
	seen := make(map[uint64]bool, 4)
	for _, conf := range paths {
		views, err := endpoint.BuildPairViews(stateDir, conf, home, now)
		if err != nil {
			return nil, err
		}
		for _, view := range views {
			if seen[view.PeerID] {
				continue
			}
			seen[view.PeerID] = true
			out = append(out, localapi.UTCPairState{
				PeerID:      view.PeerID,
				PairID:      view.PairID,
				PairGen:     view.PairGen,
				Intent:      view.Intent,
				IntentState: view.IntentState,
				Mode:        view.Mode,
				BridgeUp:    view.BridgeUp,
				LeaseValid:  view.LeaseValid,
				ExpiresAt:   view.ExpiresAt,
				State:       view.State,
				Reason:      view.Reason,
				UpdatedAt:   view.UpdatedAt,
			})
		}
	}
	return out, nil
}

// SetPairIntentResult records the cloud outcome so the local view can show
// rejected/pending reasons without inventing an execution state.
func (m *utcManager) SetPairIntentResult(peerID uint64, result, reason string) error {
	if peerID == 0 {
		return errors.New("peer_id must not be 0")
	}
	if result != endpoint.PairResultPending && result != endpoint.PairResultRejected {
		return fmt.Errorf("result must be %q or %q", endpoint.PairResultPending, endpoint.PairResultRejected)
	}
	store, err := endpoint.LoadPairIntents(utils.GetMyNetHome())
	if err != nil {
		return err
	}
	_, err = store.SetResult(peerID, result, reason, time.Now().Unix())
	return err
}

// SetPairIntent records the user's desire locally, then asks the cloud to
// create/revoke the pair. Execution still comes from the next projection.
func (m *utcManager) SetPairIntent(peerID uint64, on bool) (localapi.UTCLocalStatus, error) {
	home := utils.GetMyNetHome()
	store, err := endpoint.LoadPairIntents(home)
	if err != nil {
		return m.Status(), err
	}
	now := time.Now().Unix()
	if _, err := store.Set(peerID, on, now); err != nil {
		return m.Status(), err
	}
	result, reason := endpoint.PairResultPending, "cloud_sync_pending"
	m.mu.Lock()
	sync := m.pairSyncer
	m.mu.Unlock()
	if sync != nil {
		ctx, cancel := context.WithTimeout(m.parent, 15*time.Second)
		gotResult, gotReason, syncErr := sync(ctx, peerID, on)
		cancel()
		switch {
		case syncErr != nil:
			reason = syncErr.Error()
		case gotResult != "":
			result, reason = gotResult, gotReason
		}
	}
	if _, err := store.SetResult(peerID, result, reason, time.Now().Unix()); err != nil {
		return m.Status(), err
	}
	return m.Status(), nil
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

func utcRefsPath() string {
	if path := os.Getenv("MYNET_UTC_REFS"); path != "" {
		return path
	}
	return filepath.Join(utils.GetMyNetHome(), "conf", "utc", "utcd.refs")
}

func (m *utcManager) Sync(req localapi.UTCSyncRequest) (localapi.UTCLocalStatus, error) {
	refsPath := utcRefsPath()
	refs, err := endpoint.LoadRefs(refsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return m.Status(), fmt.Errorf("缺少本机根引用文件 %s（需包含 identity_ref/trust_ref/material_ref/state_dir）", refsPath)
		}
		return m.Status(), fmt.Errorf("读取 utcd.refs 失败: %w", err)
	}
	peers := make([]endpoint.PeerSummary, 0, len(req.Peers))
	for _, p := range req.Peers {
		peers = append(peers, endpoint.PeerSummary{
			Name:        p.Name,
			TransportID: p.TransportID,
			UDPBind:     p.UDPBind,
			UDPTarget:   p.UDPTarget,
		})
	}
	rendered, err := endpoint.RenderV3Config(refs, req.EdgeCandidates, peers)
	if err != nil {
		return m.Status(), fmt.Errorf("渲染 utcd.conf 失败: %w", err)
	}
	if err := endpoint.WriteConfigAtomic(utcConfigPath(), rendered); err != nil {
		return m.Status(), fmt.Errorf("写入 utcd.conf 失败: %w", err)
	}

	m.mu.Lock()
	shouldRestart := m.enabled
	m.mu.Unlock()
	if shouldRestart {
		m.Stop()
		m.startRuntime()
	}
	return m.Status(), nil
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
