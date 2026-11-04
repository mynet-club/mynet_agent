package driver

import (
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// VerifyUTCProjection checks local APPLIED evidence against an independently
// observed process boot/PID. It does not probe the remote path or emit EFFECTIVE.
func (d *GNBDriver) VerifyUTCProjection(expected UTCProjection, boot string, pid uint64, now time.Time) error {
	canonical, err := expected.canonical()
	if err != nil {
		return err
	}
	bootBytes, err := hex.DecodeString(boot)
	if err != nil || len(bootBytes) != 16 || hex.EncodeToString(bootBytes) != boot || pid == 0 || now.Unix() < 0 {
		return fmt.Errorf("invalid current process identity")
	}
	for _, peer := range expected.Peers {
		if peer.Mode == 1 && peer.ExpiresAt <= uint64(now.Unix()) {
			return fmt.Errorf("UTC expected policy expired")
		}
	}
	path := filepath.Join(d.ConfDir, "utc.status")
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 131072 {
		return fmt.Errorf("UTC status must be a bounded private file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return fmt.Errorf("UTC status is incomplete")
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2+2*len(expected.Peers) {
		return fmt.Errorf("UTC status record count mismatch")
	}
	header := strings.Fields(lines[0])
	if len(header) != 9 || header[0] != "status-v1" || header[1] != expected.Scope || header[2] != expected.Instance || header[3] != strconv.FormatUint(expected.LocalID, 10) || header[4] != boot || header[5] != strconv.FormatUint(expected.ConfigGen, 10) || header[8] != strconv.FormatUint(pid, 10) {
		return fmt.Errorf("UTC status scope/version/boot/PID mismatch")
	}
	generated, err := strconv.ParseUint(header[6], 10, 64)
	if err != nil || generated > uint64(now.Unix()) || uint64(now.Unix())-generated > 5 {
		return fmt.Errorf("UTC status stale or future dated")
	}
	metrics := strings.Fields(lines[1])
	if len(metrics) != 5 || metrics[0] != "metrics" {
		return fmt.Errorf("invalid UTC metrics row")
	}
	for _, field := range metrics[1:] {
		if _, err := strconv.ParseUint(field, 10, 64); err != nil {
			return fmt.Errorf("invalid UTC metric")
		}
	}
	policies := map[uint64]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(canonical), "\n"), "\n")[1:] {
		fields := strings.Fields(line)
		id, _ := strconv.ParseUint(fields[1], 10, 64)
		policies[id] = "policy" + strings.TrimPrefix(line, "peer")
	}
	for i := 2; i < len(lines); i += 2 {
		fields := strings.Fields(lines[i])
		if len(fields) != 11 || fields[0] != "policy" {
			return fmt.Errorf("invalid UTC policy row")
		}
		id, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return err
		}
		want, ok := policies[id]
		if !ok || want != lines[i] {
			return fmt.Errorf("UTC policy projection mismatch")
		}
		delete(policies, id)
		hash := sha512.Sum512([]byte(want + "\n"))
		reason := "POLICY_LOADED"
		if fields[4] == "0" {
			reason = "EXPLICIT_DIRECT"
		}
		peer := fmt.Sprintf("peer %s %s %s APPLIED %s %s %x", fields[1], fields[2], fields[3], reason, fields[10], hash)
		if lines[i+1] != peer {
			return fmt.Errorf("UTC peer not applied or digest mismatch")
		}
	}
	return nil
}
