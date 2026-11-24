package endpoint

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	zeroAddrHex32        = "00000000000000000000000000000000"
	defaultFailOpenTicks = 2
	defaultSyncInterval  = 3 * time.Second
)

// ResolveBinary locates the utcd binary across MYNET_UTCD_BIN, prefixBinDir,
// standard Unix sbin/bin locations (including OpenWrt /usr/sbin/utcd), and PATH.
func ResolveBinary(prefixBinDir string) string {
	if explicit := strings.TrimSpace(os.Getenv("MYNET_UTCD_BIN")); explicit != "" {
		return explicit
	}
	name := "utcd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	candidates := []string{filepath.Join(prefixBinDir, name)}
	if runtime.GOOS != "windows" {
		candidates = append(candidates, "/usr/sbin/utcd", "/usr/bin/utcd", "/usr/local/bin/utcd")
	}
	for _, cand := range candidates {
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand
		}
	}
	if found, err := exec.LookPath(name); err == nil {
		return found
	}
	return candidates[0]
}

// InstalledPairLease captures the effective active lease window installed by utcd
// under <state_dir>/pairs/<pair_id>-<side>/lease-{a-to-b,b-to-a}.json.
type InstalledPairLease struct {
	PairID    uint64
	PairGen   uint64
	ExpiresAt uint64
	Manifest  string
}

// PairBridgeStatus captures the live TCP bridge establishment state written by
// utcd under <state_dir>/pairs/<pair_id>-<side>/bridge.status.
type PairBridgeStatus struct {
	BridgeUp  bool
	PairID    uint64
	PairGen   uint64
	ExpiresAt uint64
	UpdatedAt uint64
	PID       int
	Alive     bool
}

// ExportEndpoint represents a local loopback UDP endpoint exported by utcd
// for a given pair (e.g. 127.0.0.1:19711).
type ExportEndpoint struct {
	Family uint8  // 4 or 6
	IP     net.IP // 4-byte IPv4 or 16-byte IPv6
	Port   uint16
}

// FormatHex formats the endpoint address according to gnb's utc-v1 or utc-v2
// wire grammar:
//   - utc-v1 IPv4: 4 bytes IPv4 + 12 zero bytes (gnb_utc.c)
//   - utc-v2 IPv4: 10 zero bytes + 0xffff + 4 bytes IPv4 (gnb_utc_hub.c)
//   - IPv6: 16 bytes IPv6
func (e ExportEndpoint) FormatHex(isV2 bool) string {
	var raw [16]byte
	if e.Family == 4 {
		v4 := e.IP.To4()
		if v4 == nil {
			return zeroAddrHex32
		}
		if isV2 {
			raw[10] = 0xff
			raw[11] = 0xff
			copy(raw[12:16], v4)
		} else {
			copy(raw[0:4], v4)
		}
		return hex.EncodeToString(raw[:])
	}
	if e.Family == 6 {
		v6 := e.IP.To16()
		if v6 == nil {
			return zeroAddrHex32
		}
		copy(raw[:], v6)
		return hex.EncodeToString(raw[:])
	}
	return zeroAddrHex32
}

func parseExportEndpointFromHostPort(hostPort string) (ExportEndpoint, bool) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(hostPort))
	if err != nil {
		return ExportEndpoint{}, false
	}
	portNum, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || portNum == 0 {
		return ExportEndpoint{}, false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ExportEndpoint{}, false
	}
	if v4 := ip.To4(); v4 != nil {
		return ExportEndpoint{Family: 4, IP: v4, Port: uint16(portNum)}, true
	}
	if v6 := ip.To16(); v6 != nil {
		return ExportEndpoint{Family: 6, IP: v6, Port: uint16(portNum)}, true
	}
	return ExportEndpoint{}, false
}

func parseExportEndpointFromFields(family uint8, addrHex string, port uint16, isV2 bool) (ExportEndpoint, bool) {
	if port == 0 || (family != 4 && family != 6) {
		return ExportEndpoint{}, false
	}
	raw, err := hex.DecodeString(addrHex)
	if err != nil || len(raw) != 16 {
		return ExportEndpoint{}, false
	}
	if family == 4 {
		if isV2 {
			if raw[10] == 0xff && raw[11] == 0xff {
				ip := make(net.IP, 4)
				copy(ip, raw[12:16])
				return ExportEndpoint{Family: 4, IP: ip, Port: port}, true
			}
			return ExportEndpoint{}, false
		}
		ip := make(net.IP, 4)
		copy(ip, raw[0:4])
		return ExportEndpoint{Family: 4, IP: ip, Port: port}, true
	}
	ip := make(net.IP, 16)
	copy(ip, raw)
	return ExportEndpoint{Family: 6, IP: ip, Port: port}, true
}

type rawStaticLease struct {
	PairID    uint64 `json:"pair_id"`
	PairGen   uint64 `json:"pair_gen"`
	ExpiresAt uint64 `json:"expires_at"`
	Manifest  string `json:"manifest"`
}

func isCanonicalSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == s
}

func readRawLease(path string) (rawStaticLease, error) {
	var lease rawStaticLease
	raw, err := os.ReadFile(path)
	if err != nil {
		return lease, err
	}
	if err := json.Unmarshal(raw, &lease); err != nil {
		return lease, err
	}
	if lease.PairID == 0 || lease.PairGen == 0 || lease.ExpiresAt == 0 {
		return lease, fmt.Errorf("incomplete lease fields in %s", path)
	}
	return lease, nil
}

// LoadInstalledPairLeases scans <stateDir>/pairs/* and returns the newest valid
// bidirectional lease window per pair_id.
func LoadInstalledPairLeases(stateDir string) (map[uint64]InstalledPairLease, error) {
	pairsDir := filepath.Join(stateDir, "pairs")
	entries, err := os.ReadDir(pairsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	result := make(map[uint64]InstalledPairLease)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(pairsDir, entry.Name())
		a2b, errA := readRawLease(filepath.Join(dir, "lease-a-to-b.json"))
		b2a, errB := readRawLease(filepath.Join(dir, "lease-b-to-a.json"))
		if errA != nil || errB != nil {
			continue
		}
		if a2b.PairID != b2a.PairID || a2b.PairGen != b2a.PairGen {
			continue
		}
		expiresAt := a2b.ExpiresAt
		if b2a.ExpiresAt < expiresAt {
			expiresAt = b2a.ExpiresAt
		}
		manifest := ""
		if a2b.Manifest == b2a.Manifest && isCanonicalSHA256Hex(a2b.Manifest) {
			manifest = a2b.Manifest
		}
		candidate := InstalledPairLease{
			PairID:    a2b.PairID,
			PairGen:   a2b.PairGen,
			ExpiresAt: expiresAt,
			Manifest:  manifest,
		}
		if prev, ok := result[candidate.PairID]; ok {
			if candidate.PairGen < prev.PairGen ||
				(candidate.PairGen == prev.PairGen && candidate.ExpiresAt <= prev.ExpiresAt) {
				continue
			}
		}
		result[candidate.PairID] = candidate
	}
	return result, nil
}

// LoadBridgeStatuses scans <stateDir>/pairs/*/bridge.status and returns the
// latest per-pair TCP bridge status published by utcd.
func LoadBridgeStatuses(stateDir string) (map[uint64]PairBridgeStatus, error) {
	return loadBridgeStatusesWithAlive(stateDir, isProcessAlive)
}

func loadBridgeStatusesWithAlive(stateDir string, aliveFn func(int) bool) (map[uint64]PairBridgeStatus, error) {
	pairsDir := filepath.Join(stateDir, "pairs")
	entries, err := os.ReadDir(pairsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if aliveFn == nil {
		aliveFn = isProcessAlive
	}

	result := make(map[uint64]PairBridgeStatus)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		statusPath := filepath.Join(pairsDir, entry.Name(), "bridge.status")
		st, err := parseBridgeStatusFile(statusPath, aliveFn)
		if err != nil || st.PairID == 0 {
			continue
		}
		if prev, ok := result[st.PairID]; ok {
			prevUp := prev.BridgeUp && prev.Alive
			curUp := st.BridgeUp && st.Alive
			if prevUp && !curUp {
				continue
			}
			if prevUp == curUp && st.UpdatedAt < prev.UpdatedAt {
				continue
			}
		}
		result[st.PairID] = st
	}
	return result, nil
}

func parseBridgeStatusFile(path string, aliveFn func(int) bool) (PairBridgeStatus, error) {
	var st PairBridgeStatus
	file, err := os.Open(path)
	if err != nil {
		return st, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "bridge_up":
			st.BridgeUp = val == "1" || strings.EqualFold(val, "true")
		case "pair_id":
			if v, err := strconv.ParseUint(val, 10, 64); err == nil {
				st.PairID = v
			}
		case "pair_gen":
			if v, err := strconv.ParseUint(val, 10, 64); err == nil {
				st.PairGen = v
			}
		case "expires_at":
			if v, err := strconv.ParseUint(val, 10, 64); err == nil {
				st.ExpiresAt = v
			}
		case "updated_at":
			if v, err := strconv.ParseUint(val, 10, 64); err == nil {
				st.UpdatedAt = v
			}
		case "pid":
			if v, err := strconv.Atoi(val); err == nil {
				st.PID = v
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return st, err
	}
	st.Alive = aliveFn(st.PID)
	return st, nil
}

type utcToggleStateFile struct {
	Enabled bool `json:"enabled"`
}

// LoadUserUTCEnabled reads the user's UTC acceleration intent from
// $MYNET_HOME/conf/local/utc_state.json. When the state file does not exist
// yet, it falls back to whether utcd.conf exists (matching utcManager.StartIfEnabled).
func LoadUserUTCEnabled(myNetHome, utcdConfPath string) bool {
	if strings.TrimSpace(myNetHome) != "" {
		statePath := filepath.Join(myNetHome, "conf", "local", "utc_state.json")
		if raw, err := os.ReadFile(statePath); err == nil {
			var st utcToggleStateFile
			if err := json.Unmarshal(raw, &st); err == nil {
				return st.Enabled
			}
		}
	}
	confPath := strings.TrimSpace(utcdConfPath)
	if confPath == "" {
		confPath = ResolveUTCDConfigPath(myNetHome)
	}
	if confPath == "" {
		return false
	}
	info, err := os.Lstat(confPath)
	return err == nil && info.Mode().IsRegular()
}

// resolveExportEndpoints maps pair_id -> loopback UDP ExportEndpoint using
// utcd.conf peer definitions and (for v3 configs without explicit pair_id)
// pair trust files under <stateDir>/pairs/*/trust.
func resolveExportEndpoints(utcdConfPath, stateDir string) map[uint64]ExportEndpoint {
	out := make(map[uint64]ExportEndpoint)
	if strings.TrimSpace(utcdConfPath) == "" {
		return out
	}
	summary, err := ParseConfig(utcdConfPath)
	if err != nil || len(summary.Peers) == 0 {
		return out
	}

	byTransport := make(map[uint64]ExportEndpoint)
	var singleEP ExportEndpoint
	singleCount := 0
	for _, p := range summary.Peers {
		ep, ok := parseExportEndpointFromHostPort(p.UDPBind)
		if !ok {
			continue
		}
		singleEP = ep
		singleCount++
		if p.PairID != 0 {
			out[p.PairID] = ep
		}
		if p.TransportID != 0 {
			byTransport[p.TransportID] = ep
		}
	}

	if strings.TrimSpace(stateDir) == "" {
		return out
	}
	pairsDir := filepath.Join(stateDir, "pairs")
	entries, err := os.ReadDir(pairsDir)
	if err != nil {
		return out
	}
	var unmatchedPairs []uint64
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pairID, peerTransport := readTrustPairTransport(filepath.Join(pairsDir, entry.Name(), "trust"))
		if pairID == 0 {
			// Fallback to directory name prefix "<pair_id>-<side>"
			if prefix, _, ok := strings.Cut(entry.Name(), "-"); ok {
				if v, err := strconv.ParseUint(prefix, 10, 64); err == nil {
					pairID = v
				}
			}
		}
		if pairID == 0 {
			continue
		}
		if _, exists := out[pairID]; exists {
			continue
		}
		if peerTransport != 0 {
			if ep, ok := byTransport[peerTransport]; ok {
				out[pairID] = ep
				continue
			}
		}
		unmatchedPairs = append(unmatchedPairs, pairID)
	}
	if singleCount == 1 && len(unmatchedPairs) == 1 {
		out[unmatchedPairs[0]] = singleEP
	}
	return out
}

func readTrustPairTransport(trustPath string) (pairID uint64, peerTransport uint64) {
	file, err := os.Open(trustPath)
	if err != nil {
		return 0, 0
	}
	defer file.Close()
	var fallbackTarget uint64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "pair_id", "pair.0.pair_id":
			if v, err := strconv.ParseUint(val, 10, 64); err == nil && pairID == 0 {
				pairID = v
			}
		// utcd pair_issuance.c writes peer_transport= in [trust] (authoritative on both
		// side-a and side-b); keep peer_transport_id for legacy compatibility and
		// target_peer_id only as a last-resort fallback.
		case "peer_transport", "peer_transport_id":
			if v, err := strconv.ParseUint(val, 10, 64); err == nil && peerTransport == 0 {
				peerTransport = v
			}
		case "target_peer_id":
			if v, err := strconv.ParseUint(val, 10, 64); err == nil && fallbackTarget == 0 {
				fallbackTarget = v
			}
		}
	}
	if peerTransport == 0 {
		peerTransport = fallbackTarget
	}
	return pairID, peerTransport
}

// GNBIdentity represents the live instance identity published by gnb in
// <dir(utc.conf)>/utc.identity: "identity-v1 <local_id> <boot_id(32hex)> <process_id>".
type GNBIdentity struct {
	LocalID   uint64
	BootID    string
	ProcessID uint64
}

func isCanonicalBootIDHex(s string) bool {
	if len(s) != 32 {
		return false
	}
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == 16 && hex.EncodeToString(raw) == s
}

// LoadGNBIdentity reads and validates utc.identity next to utcConfPath.
func LoadGNBIdentity(utcConfPath string, aliveFn func(int) bool) (GNBIdentity, bool) {
	id, present, pidAlive, reason := loadGNBIdentityDetailed(utcConfPath, aliveFn)
	if !present || reason != "" || !pidAlive {
		return GNBIdentity{}, false
	}
	return id, true
}

func loadGNBIdentityDetailed(utcConfPath string, aliveFn func(int) bool) (id GNBIdentity, present bool, pidAlive bool, reason string) {
	identityPath := filepath.Join(filepath.Dir(utcConfPath), "utc.identity")
	raw, err := os.ReadFile(identityPath)
	if err != nil {
		if os.IsNotExist(err) {
			return id, false, false, ""
		}
		return id, true, false, fmt.Sprintf("read error: %v", err)
	}
	fields := strings.Fields(strings.TrimSpace(string(raw)))
	if len(fields) != 4 || fields[0] != "identity-v1" {
		return id, true, false, fmt.Sprintf("invalid format (%q)", strings.TrimSpace(string(raw)))
	}
	localID, err1 := strconv.ParseUint(fields[1], 10, 64)
	pid, err2 := strconv.ParseUint(fields[3], 10, 64)
	if err1 != nil || err2 != nil || localID == 0 || pid == 0 || !isCanonicalBootIDHex(fields[2]) {
		return id, true, false, "invalid local_id/boot_id/pid field"
	}
	if aliveFn == nil {
		aliveFn = isProcessAlive
	}
	id = GNBIdentity{LocalID: localID, BootID: fields[2], ProcessID: pid}
	return id, true, aliveFn(int(pid)), ""
}

// ReconcileState holds per-peer anti-flap debounce counters and cached loopback
// export endpoints across poll ticks.
type ReconcileState struct {
	mu              sync.Mutex
	downTicks       map[string]int
	cachedEndpoints map[string]ExportEndpoint
}

func NewReconcileState() *ReconcileState {
	return &ReconcileState{
		downTicks:       make(map[string]int),
		cachedEndpoints: make(map[string]ExportEndpoint),
	}
}

func peerStateKey(utcConfPath string, pairID uint64) string {
	return fmt.Sprintf("%s#%d", utcConfPath, pairID)
}

// ReconcileOptions configures Sync/Fail-Open reconciliation for a single utc.conf.
type ReconcileOptions struct {
	StateDir          string
	UTCConfPath       string
	UTCDConfPath      string
	MyNetHome         string
	NowUnix           uint64
	UserEnabled       *bool
	DownDebounceTicks int
	DisableFailOpen   bool
	ProcessAliveFn    func(int) bool
	Logf              func(string, ...any)
}

// ReconcileUTCConf reconciles utcd-installed leases and live TCP bridge status
// into gnb's utc.conf with dual-gate Fail-Open hot-switching:
//   - Only when userEnabled && bridgeUp && now < expiresAt does a peer run in
//     EXPORT mode (mode=2 for utc-v2, mode=1 for utc-v1).
//   - Otherwise (user disabled, pairing/TCP bridge down past debounce, utcd
//     stopped, or lease expired), the peer automatically falls back to native
//     UDP (mode=0 DIRECT), bumping pair_gen & config_gen so gnb hot-reloads
//     cleanly without touching gnb core.
func ReconcileUTCConf(opts ReconcileOptions, state *ReconcileState) (int, uint64, error) {
	if strings.TrimSpace(opts.StateDir) == "" || strings.TrimSpace(opts.UTCConfPath) == "" {
		return 0, 0, nil
	}
	installed, err := LoadInstalledPairLeases(opts.StateDir)
	if err != nil {
		return 0, 0, err
	}
	if opts.DisableFailOpen && len(installed) == 0 {
		return 0, 0, nil
	}

	info, err := os.Lstat(opts.UTCConfPath)
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, 0, fmt.Errorf("utc.conf is not a regular file: %s", opts.UTCConfPath)
	}

	raw, err := os.ReadFile(opts.UTCConfPath)
	if err != nil {
		return 0, 0, err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\r\n"), "\n")
	if len(lines) == 0 {
		return 0, 0, fmt.Errorf("empty utc.conf: %s", opts.UTCConfPath)
	}
	header := strings.Fields(lines[0])
	var isV2 bool
	switch {
	case len(header) == 5 && header[0] == "utc-v1":
		isV2 = false
	case len(header) == 8 && header[0] == "utc-v2":
		isV2 = true
	default:
		return 0, 0, fmt.Errorf("invalid utc.conf header in %s", opts.UTCConfPath)
	}
	configGen, err := strconv.ParseUint(header[4], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid utc.conf config_gen: %w", err)
	}

	// Grammar verified against mynet_gnb/src/gnb_utc.c (utc-v1) & gnb_utc_hub.c (utc-v2):
	// utc-v1 (11 fields): peer <peer_id> <pair_id> <pair_gen> <mode> <expires_at> <family> <addr> <port> <scope> <manifest>
	// utc-v2 (13 fields): peer <peer_id> <pair_id> <pair_gen> <mode> <lease_epoch> <placement_epoch> <expires_at> <family> <addr> <port> <scope> <manifest>
	wantFields, modeIdx, expiresIdx, familyIdx, addrIdx, portIdx, scopeIdx, manifestIdx := 11, 4, 5, 6, 7, 8, 9, 10
	exportMode := uint64(1)
	if isV2 {
		wantFields, modeIdx, expiresIdx, familyIdx, addrIdx, portIdx, scopeIdx, manifestIdx = 13, 4, 7, 8, 9, 10, 11, 12
		exportMode = 2
	}

	nowUnix := opts.NowUnix
	if nowUnix == 0 {
		nowUnix = uint64(time.Now().Unix())
	}
	debounceTicks := opts.DownDebounceTicks
	if debounceTicks <= 0 {
		debounceTicks = defaultFailOpenTicks
	}

	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	// P1-a / P2: rebind utc-v2 header boot_id/process_id when gnb restarts and
	// publishes a new utc.identity. Never fail silently when utc.identity is
	// present but malformed or carries a non-live parent PID (e.g. daemonized gnb).
	headerRebound := false
	if isV2 && !opts.DisableFailOpen {
		headerLocalID, errLocal := strconv.ParseUint(header[3], 10, 64)
		if errLocal == nil && headerLocalID != 0 {
			ident, present, pidAlive, reason := loadGNBIdentityDetailed(opts.UTCConfPath, opts.ProcessAliveFn)
			if present {
				switch {
				case reason != "":
					logf("utc.conf WARN ignoring utc.identity path=%s reason=%s", opts.UTCConfPath, reason)
				case ident.LocalID != headerLocalID:
					logf("utc.conf WARN ignoring utc.identity path=%s reason=local_id_mismatch identity=%d header=%d",
						opts.UTCConfPath, ident.LocalID, headerLocalID)
				default:
					pidStr := strconv.FormatUint(ident.ProcessID, 10)
					if header[5] != ident.BootID || header[6] != pidStr {
						if pidAlive {
							logf("utc.conf rebinding utc-v2 identity path=%s boot_id=%s->%s pid=%s->%s",
								opts.UTCConfPath, header[5], ident.BootID, header[6], pidStr)
							header[5] = ident.BootID
							header[6] = pidStr
							headerRebound = true
						} else if header[5] != ident.BootID {
							logf("utc.conf WARN utc.identity pid=%d not alive (daemonized gnb?), rebinding on boot_id change path=%s boot_id=%s->%s pid=%s->%s",
								ident.ProcessID, opts.UTCConfPath, header[5], ident.BootID, header[6], pidStr)
							header[5] = ident.BootID
							header[6] = pidStr
							headerRebound = true
						} else {
							logf("utc.conf WARN utc.identity pid=%d not alive and boot_id unchanged (%s), skipping rebind path=%s",
								ident.ProcessID, ident.BootID, opts.UTCConfPath)
						}
					}
				}
			}
		}
	}

	var bridges map[uint64]PairBridgeStatus
	var endpoints map[uint64]ExportEndpoint
	userEnabled := true
	if !opts.DisableFailOpen {
		if opts.UserEnabled != nil {
			userEnabled = *opts.UserEnabled
		} else {
			userEnabled = LoadUserUTCEnabled(opts.MyNetHome, opts.UTCDConfPath)
		}
		bridges, _ = loadBridgeStatusesWithAlive(opts.StateDir, opts.ProcessAliveFn)
		endpoints = resolveExportEndpoints(opts.UTCDConfPath, opts.StateDir)
	}
	if state == nil {
		state = NewReconcileState()
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	updated := 0
	outLines := make([]string, 0, len(lines))
	outLines = append(outLines, "") // header placeholder
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) != wantFields || fields[0] != "peer" {
			return 0, configGen, fmt.Errorf("unrecognized utc.conf peer row in %s", opts.UTCConfPath)
		}
		pairID, err1 := strconv.ParseUint(fields[2], 10, 64)
		pairGen, err2 := strconv.ParseUint(fields[3], 10, 64)
		mode, err3 := strconv.ParseUint(fields[modeIdx], 10, 8)
		expiresAt, err4 := strconv.ParseUint(fields[expiresIdx], 10, 64)
		family, err5 := strconv.ParseUint(fields[familyIdx], 10, 8)
		port, err6 := strconv.ParseUint(fields[portIdx], 10, 16)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || err6 != nil {
			return 0, configGen, fmt.Errorf("invalid numeric field in utc.conf peer row")
		}

		key := peerStateKey(opts.UTCConfPath, pairID)
		if mode != 0 {
			if ep, ok := parseExportEndpointFromFields(uint8(family), fields[addrIdx], uint16(port), isV2); ok {
				state.cachedEndpoints[key] = ep
			}
		}

		inst, hasLease := installed[pairID]

		if opts.DisableFailOpen {
			if mode == exportMode && hasLease {
				if inst.PairGen > pairGen || (inst.PairGen == pairGen && inst.ExpiresAt > expiresAt) {
					fields[3] = strconv.FormatUint(inst.PairGen, 10)
					fields[expiresIdx] = strconv.FormatUint(inst.ExpiresAt, 10)
					if inst.Manifest != "" {
						fields[manifestIdx] = inst.Manifest
					}
					outLines = append(outLines, strings.Join(fields, " "))
					updated++
					continue
				}
			}
			outLines = append(outLines, trimmed)
			continue
		}

		// Dual-gate Fail-Open state machine:
		// Gate 1: userEnabled (utc_state.json)
		// Gate 2: utcd bridge_up==1 && pid alive && lease unexpired
		leaseValid := hasLease && inst.ExpiresAt > nowUnix
		st, hasBridge := bridges[pairID]
		bridgeUp := hasBridge && st.BridgeUp && st.Alive
		wantExport := userEnabled && bridgeUp && leaseValid

		if wantExport {
			state.downTicks[key] = 0
			if mode == 0 {
				// Switch DIRECT (UDP) -> EXPORT (UTC TCP)
				ep, ok := state.cachedEndpoints[key]
				if !ok {
					ep, ok = endpoints[pairID]
				}
				if !ok {
					logf("utc.conf cannot restore export mode for pair_id=%d path=%s: no loopback udp_bind resolved from utcd.conf=%s",
						pairID, opts.UTCConfPath, opts.UTCDConfPath)
				} else {
					nextPairGen := pairGen + 1
					if inst.PairGen > nextPairGen {
						nextPairGen = inst.PairGen
					}
					fields[3] = strconv.FormatUint(nextPairGen, 10)
					fields[modeIdx] = strconv.FormatUint(exportMode, 10)
					fields[expiresIdx] = strconv.FormatUint(inst.ExpiresAt, 10)
					fields[familyIdx] = strconv.FormatUint(uint64(ep.Family), 10)
					fields[addrIdx] = ep.FormatHex(isV2)
					fields[portIdx] = strconv.FormatUint(uint64(ep.Port), 10)
					fields[scopeIdx] = "0"
					if inst.Manifest != "" {
						fields[manifestIdx] = inst.Manifest
					}
					outLines = append(outLines, strings.Join(fields, " "))
					updated++
					continue
				}
			} else if mode == exportMode {
				// Already in EXPORT: keep lease window fresh, and if gnb just restarted
				// (headerRebound), bump pair_gen once to release gnb's REQUIRES_NEW_GENERATION latch.
				manifestChanged := inst.Manifest != "" && inst.Manifest != fields[manifestIdx]
				if headerRebound || manifestChanged || inst.PairGen > pairGen || inst.ExpiresAt > expiresAt {
					nextPairGen := pairGen
					if inst.PairGen > nextPairGen {
						nextPairGen = inst.PairGen
					}
					// Changing manifest or unlocking post-restart recovery requires strictly newer pair_gen (gnb_utc.c).
					if (headerRebound || manifestChanged) && nextPairGen <= pairGen {
						nextPairGen = pairGen + 1
					}
					fields[3] = strconv.FormatUint(nextPairGen, 10)
					if inst.ExpiresAt > expiresAt {
						fields[expiresIdx] = strconv.FormatUint(inst.ExpiresAt, 10)
					}
					if inst.Manifest != "" {
						fields[manifestIdx] = inst.Manifest
					}
					outLines = append(outLines, strings.Join(fields, " "))
					updated++
					continue
				}
			}
			outLines = append(outLines, trimmed)
			continue
		}

		// wantExport == false: fall back to native UDP (mode=0 DIRECT)
		if mode == exportMode {
			immediateFallback := !userEnabled || !leaseValid
			shouldFallback := immediateFallback
			if !immediateFallback {
				state.downTicks[key]++
				if state.downTicks[key] >= debounceTicks {
					shouldFallback = true
				}
			}
			if shouldFallback {
				state.downTicks[key] = 0
				// Mode change requires strictly newer pair_gen (gnb_utc.c ADR-0005).
				nextPairGen := pairGen + 1
				fields[3] = strconv.FormatUint(nextPairGen, 10)
				fields[modeIdx] = "0"
				fields[expiresIdx] = "0"
				fields[familyIdx] = "0"
				fields[addrIdx] = zeroAddrHex32
				fields[portIdx] = "0"
				fields[scopeIdx] = "0"
				outLines = append(outLines, strings.Join(fields, " "))
				updated++
				continue
			}
		} else if mode == 0 {
			state.downTicks[key] = 0
		}
		outLines = append(outLines, trimmed)
	}

	if updated == 0 && !headerRebound {
		return 0, configGen, nil
	}

	nextConfigGen := configGen + 1
	header[4] = strconv.FormatUint(nextConfigGen, 10)
	outLines[0] = strings.Join(header, " ")
	payload := []byte(strings.Join(outLines, "\n") + "\n")

	if err := writeFileAtomic(opts.UTCConfPath, payload, 0o600); err != nil {
		return 0, configGen, err
	}
	return updated, nextConfigGen, nil
}

// SyncUTCConfFromStateDir atomically advances pair_gen and expires_at in gnb's
// utc.conf whenever utcd has installed a newer lease bundle in stateDir.
// Other peers and non-lease fields are preserved byte-identically.
func SyncUTCConfFromStateDir(stateDir, utcConfPath string) (int, uint64, error) {
	return ReconcileUTCConf(ReconcileOptions{
		StateDir:        stateDir,
		UTCConfPath:     utcConfPath,
		DisableFailOpen: true,
	}, nil)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".utc-sync-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// ResolveUTCDConfigPath returns the canonical utcd.conf path under MYNET_HOME
// ($MYNET_HOME/conf/utc/utcd.conf), honouring MYNET_UTC_CONFIG and falling back
// to legacy OpenWrt $MYNET_HOME/utc/utcd.conf when the canonical file is absent.
func ResolveUTCDConfigPath(myNetHome string) string {
	if explicit := strings.TrimSpace(os.Getenv("MYNET_UTC_CONFIG")); explicit != "" {
		return explicit
	}
	if strings.TrimSpace(myNetHome) != "" {
		canonical := filepath.Join(myNetHome, "conf", "utc", "utcd.conf")
		if info, err := os.Lstat(canonical); err == nil && info.Mode().IsRegular() {
			return canonical
		}
		legacy := filepath.Join(myNetHome, "utc", "utcd.conf")
		if info, err := os.Lstat(legacy); err == nil && info.Mode().IsRegular() {
			return legacy
		}
		return canonical
	}
	return ""
}

// DiscoverUTCDStateDirs returns candidate utcd state_dir paths ordered by
// authority: utcd.conf state_dir -> $MYNET_HOME/state/utc/utcd -> legacy /var/lib/mynet-utc/utcd.
func DiscoverUTCDStateDirs(utcdConfPath, myNetHome string) []string {
	var raw []string
	confPath := strings.TrimSpace(utcdConfPath)
	if confPath == "" {
		confPath = ResolveUTCDConfigPath(myNetHome)
	}
	if confPath != "" {
		if summary, err := ParseConfig(confPath); err == nil && strings.TrimSpace(summary.StateDir) != "" {
			raw = append(raw, strings.TrimSpace(summary.StateDir))
		}
	}
	if strings.TrimSpace(myNetHome) != "" {
		raw = append(raw, filepath.Join(myNetHome, "state", "utc", "utcd"))
	}
	raw = append(raw, "/var/lib/mynet-utc/utcd")

	seen := make(map[string]struct{}, len(raw))
	var out []string
	for _, dir := range raw {
		if dir == "" {
			continue
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		canon := filepath.Clean(dir)
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			canon = filepath.Clean(resolved)
		}
		if _, ok := seen[canon]; ok {
			continue
		}
		seen[canon] = struct{}{}
		out = append(out, dir)
	}
	return out
}

// DiscoverUTCConfPaths locates existing gnb utc.conf files on the host,
// prioritizing the canonical single config tree ($MYNET_HOME/conf/<form>/<nodeId>/utc.conf)
// while keeping legacy /var/run/mynet/*/utc.conf as a transition fallback.
func DiscoverUTCConfPaths(myNetHome string) []string {
	var patterns []string
	if explicit := strings.TrimSpace(os.Getenv("MYNET_UTC_CONF_PATH")); explicit != "" {
		patterns = append(patterns, explicit)
	}
	if strings.TrimSpace(myNetHome) != "" {
		patterns = append(patterns,
			filepath.Join(myNetHome, "conf", "*", "*", "utc.conf"),
		)
	}
	// Transition fallback for nodes not yet restarted off legacy /var/run/mynet projection.
	patterns = append(patterns,
		"/var/run/mynet/*/utc.conf",
		"/run/mynet/*/utc.conf",
	)

	seen := make(map[string]struct{})
	var paths []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			info, err := os.Lstat(match)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			canon := filepath.Clean(match)
			if resolved, err := filepath.EvalSymlinks(match); err == nil {
				canon = filepath.Clean(resolved)
			}
			if _, ok := seen[canon]; ok {
				continue
			}
			seen[canon] = struct{}{}
			paths = append(paths, match)
		}
	}
	return paths
}

// ReconcileAllOnce runs a single pass of lease + Fail-Open reconciliation
// across all discovered utcd state dirs and gnb utc.conf files.
func ReconcileAllOnce(utcdConfPath, myNetHome string, state *ReconcileState, logf func(string, ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	stateDirs := DiscoverUTCDStateDirs(utcdConfPath, myNetHome)
	if len(stateDirs) == 0 {
		return
	}
	utcConfs := DiscoverUTCConfPaths(myNetHome)
	if len(utcConfs) == 0 {
		return
	}
	resolvedUTCDConf := strings.TrimSpace(utcdConfPath)
	if resolvedUTCDConf == "" {
		resolvedUTCDConf = ResolveUTCDConfigPath(myNetHome)
	}
	for _, stateDir := range stateDirs {
		for _, utcConf := range utcConfs {
			updated, newGen, err := ReconcileUTCConf(ReconcileOptions{
				StateDir:     stateDir,
				UTCConfPath:  utcConf,
				UTCDConfPath: resolvedUTCDConf,
				MyNetHome:    myNetHome,
				Logf:         logf,
			}, state)
			if err != nil {
				logf("utc.conf reconcile error state_dir=%s path=%s err=%v", stateDir, utcConf, err)
				continue
			}
			if updated > 0 {
				logf("utc.conf reconciled state_dir=%s path=%s updated_peers=%d config_gen=%d",
					stateDir, utcConf, updated, newGen)
			}
		}
	}
}

// RunLeaseSyncLoop periodically reconciles utcd-installed leases and TCP bridge
// health into gnb's utc.conf files with Fail-Open hot-switching.
// It operates independently of whether utcd is spawned by mynetd or procd/systemd.
func RunLeaseSyncLoop(ctx context.Context, utcdConfPath, myNetHome string, interval time.Duration, logf func(string, ...any)) {
	RunLeaseSyncLoopWithState(ctx, utcdConfPath, myNetHome, interval, NewReconcileState(), logf)
}

// RunLeaseSyncLoopWithState is like RunLeaseSyncLoop but shares a ReconcileState
// (endpoint cache + anti-flap debounce) with the caller.
func RunLeaseSyncLoopWithState(ctx context.Context, utcdConfPath, myNetHome string, interval time.Duration, state *ReconcileState, logf func(string, ...any)) {
	if interval <= 0 {
		interval = defaultSyncInterval
	}
	if state == nil {
		state = NewReconcileState()
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	ReconcileAllOnce(utcdConfPath, myNetHome, state, logf)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ReconcileAllOnce(utcdConfPath, myNetHome, state, logf)
		}
	}
}
