package endpoint

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
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

// SyncUTCConfFromStateDir atomically advances pair_gen and expires_at in gnb's
// utc.conf whenever utcd has installed a newer lease bundle in stateDir.
// Other peers and non-lease fields are preserved byte-identically.
func SyncUTCConfFromStateDir(stateDir, utcConfPath string) (int, uint64, error) {
	if strings.TrimSpace(stateDir) == "" || strings.TrimSpace(utcConfPath) == "" {
		return 0, 0, nil
	}
	installed, err := LoadInstalledPairLeases(stateDir)
	if err != nil || len(installed) == 0 {
		return 0, 0, err
	}

	info, err := os.Lstat(utcConfPath)
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, 0, fmt.Errorf("utc.conf is not a regular file: %s", utcConfPath)
	}

	raw, err := os.ReadFile(utcConfPath)
	if err != nil {
		return 0, 0, err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\r\n"), "\n")
	if len(lines) == 0 {
		return 0, 0, fmt.Errorf("empty utc.conf: %s", utcConfPath)
	}
	header := strings.Fields(lines[0])
	var isV2 bool
	switch {
	case len(header) == 5 && header[0] == "utc-v1":
		isV2 = false
	case len(header) >= 5 && header[0] == "utc-v2":
		isV2 = true
	default:
		return 0, 0, fmt.Errorf("invalid utc.conf header in %s", utcConfPath)
	}
	configGen, err := strconv.ParseUint(header[4], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid utc.conf config_gen: %w", err)
	}

	wantFields, modeIdx, expiresIdx, manifestIdx := 11, 4, 5, 10
	if isV2 {
		wantFields, modeIdx, expiresIdx, manifestIdx = 13, 6, 7, 12
	}

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
			return 0, configGen, fmt.Errorf("unrecognized utc.conf peer row in %s", utcConfPath)
		}
		pairID, err1 := strconv.ParseUint(fields[2], 10, 64)
		pairGen, err2 := strconv.ParseUint(fields[3], 10, 64)
		mode, err3 := strconv.ParseUint(fields[modeIdx], 10, 8)
		expiresAt, err4 := strconv.ParseUint(fields[expiresIdx], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			return 0, configGen, fmt.Errorf("invalid numeric field in utc.conf peer row")
		}

		if mode == 1 {
			if inst, ok := installed[pairID]; ok {
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
		}
		outLines = append(outLines, trimmed)
	}

	if updated == 0 {
		return 0, configGen, nil
	}

	nextConfigGen := configGen + 1
	header[4] = strconv.FormatUint(nextConfigGen, 10)
	outLines[0] = strings.Join(header, " ")
	payload := []byte(strings.Join(outLines, "\n") + "\n")

	if err := writeFileAtomic(utcConfPath, payload, 0o600); err != nil {
		return 0, configGen, err
	}
	return updated, nextConfigGen, nil
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
// deduplicating symlinked /var/run and /run paths.
func DiscoverUTCConfPaths(myNetHome string) []string {
	var patterns []string
	if explicit := strings.TrimSpace(os.Getenv("MYNET_UTC_CONF_PATH")); explicit != "" {
		patterns = append(patterns, explicit)
	}
	patterns = append(patterns,
		"/var/run/mynet/*/utc.conf",
		"/run/mynet/*/utc.conf",
	)
	if strings.TrimSpace(myNetHome) != "" {
		patterns = append(patterns,
			filepath.Join(myNetHome, "conf", "*", "*", "utc.conf"),
			filepath.Join(myNetHome, "var", "*", "utc.conf"),
		)
	}

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

// RunLeaseSyncLoop periodically reconciles utcd-installed leases into gnb's
// utc.conf files so gnb never stalls in BLOCKED EXPIRED after lease renewal.
// It operates independently of whether utcd is spawned by mynetd or procd/systemd.
func RunLeaseSyncLoop(ctx context.Context, utcdConfPath, myNetHome string, interval time.Duration, logf func(string, ...any)) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	syncOnce := func() {
		stateDirs := DiscoverUTCDStateDirs(utcdConfPath, myNetHome)
		if len(stateDirs) == 0 {
			return
		}
		utcConfs := DiscoverUTCConfPaths(myNetHome)
		if len(utcConfs) == 0 {
			return
		}
		for _, stateDir := range stateDirs {
			for _, utcConf := range utcConfs {
				updated, newGen, err := SyncUTCConfFromStateDir(stateDir, utcConf)
				if err != nil {
					logf("utc.conf sync error state_dir=%s path=%s err=%v", stateDir, utcConf, err)
					continue
				}
				if updated > 0 {
					logf("utc.conf synced from utcd state_dir=%s path=%s updated_peers=%d config_gen=%d",
						stateDir, utcConf, updated, newGen)
				}
			}
		}
	}

	syncOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			syncOnce()
		}
	}
}
