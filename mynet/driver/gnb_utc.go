package driver

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// utcHistoryPath is deliberately outside ConfDir: deleting driver state must
// never turn a historically managed instance into a native-only instance.
func (d *GNBDriver) utcHistoryPath() (string, string, error) {
	if d.ConfDir == "" {
		return "", "", fmt.Errorf("UTC requires a configuration directory")
	}
	conf, err := canonicalUTCPath(d.ConfDir)
	if err != nil {
		return "", "", err
	}
	conf = filepath.Clean(conf)
	sum := sha256.Sum256([]byte(conf))
	path := filepath.Join(utils.GetMyNetHome(), "state", "utc", fmt.Sprintf("%x.managed", sum))
	return path, "utc-managed-v1\n" + conf + "\n", nil
}

func (d *GNBDriver) utcManaged() (bool, error) {
	managed, _, err := d.utcHistory()
	return managed, err
}

func (d *GNBDriver) utcHistory() (bool, string, error) {
	if d.ConfDir == "" {
		return false, "", nil
	}
	path, expected, err := d.utcHistoryPath()
	if err != nil {
		return false, "", err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 8192 {
		return false, "", fmt.Errorf("UTC history is not a private regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return false, "", err
	}
	if string(content) == expected {
		return true, "", nil
	}
	// v2 is reserved for a trusted recovery workflow: it must establish the
	// complete authoritative watermarks and durable state before publishing
	// this marker atomically. This package does not perform that migration.
	if string(content) == strings.Replace(expected, "utc-managed-v1\n", "utc-managed-v2\n", 1) {
		return true, path + ".state", nil
	}
	return false, "", fmt.Errorf("UTC history scope mismatch or corruption")
}

func validateUTCStateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("UTC state directory unavailable: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("UTC state directory must be a private directory")
	}
	return nil
}

// MarkUTCManaged must succeed BEFORE the first UTC configuration is published.
// It is monotonic: explicit direct releases a pair, never this history marker.
// Caller holds the existing per-instance lifecycle lock during policy changes.
func (d *GNBDriver) MarkUTCManaged() error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("UTC GNB execution is unsupported on Windows")
	}
	path, content, err := d.utcHistoryPath()
	if err != nil {
		return err
	}
	if managed, err := d.utcManaged(); err != nil {
		return err
	} else if managed {
		return syncUTCHistory(path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	// A partial marker is intentionally retained: ambiguous history fails closed.
	_, writeErr := file.WriteString(content)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return syncUTCHistory(path)
}

func syncUTCHistory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	// Persist newly created directory entries as well as the marker itself.
	for current := filepath.Dir(path); ; current = filepath.Dir(current) {
		directory, err := os.Open(current)
		if err != nil {
			return err
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}

func (d *GNBDriver) utcStartArgs() ([]string, error) {
	args := append([]string{"-c", d.ConfDir}, d.gnbExplicitPathArgs()...)
	managed, stateDir, err := d.utcHistory()
	if err != nil {
		return nil, fmt.Errorf("UTC RECOVERY_REQUIRED: %w", err)
	}
	if !managed {
		return args, nil
	}
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("UTC managed GNB is unsupported on Windows")
	}
	if stateDir != "" {
		if err := validateUTCStateDirectory(stateDir); err != nil {
			return nil, fmt.Errorf("UTC RECOVERY_REQUIRED: %w", err)
		}
		if err := d.checkUTCFeatures("utc-conf-v1", "utc-state-dir-v1"); err != nil {
			return nil, err
		}
		args = append(args, "--utc-state-dir", stateDir)
	} else if err := d.checkUTCFeature(); err != nil {
		return nil, err
	}
	return append(args, "--utc-managed"), nil
}

// UTCLaunchArgs is shared by direct spawn and the service-owned exec launcher.
// A caller must replace itself rather than delegate back into Start.
func (d *GNBDriver) UTCLaunchArgs() ([]string, error) { return d.utcStartArgs() }

func (d *GNBDriver) utcCheckDelegation() error {
	var template []byte
	var err error
	if owner := platformServiceOwnerFn(); owner != "" {
		template, err = os.ReadFile(owner)
	} else if d.systemdManages() {
		// Inspect the effective command, not concatenated unit text: a legacy
		// drop-in can override ExecStart while leaving the new marker visible.
		template, err = d.Exec("systemctl", "show", d.ServiceUnit, "--property=ExecStart", "--value")
		if err != nil {
			return fmt.Errorf("UTC effective service command inspection: %w", err)
		}
		if !strings.Contains(string(template), " utc driver-run --binary ") || !strings.Contains(string(template), " -c "+d.ConfDir+" ") {
			return fmt.Errorf("UTC managed startup requires a guarded effective ExecStart for this configuration")
		}
		return nil
	} else {
		return nil
	}
	if err != nil {
		return fmt.Errorf("UTC service template inspection: %w", err)
	}
	if !strings.Contains(string(template), "MYNET_UTC_LAUNCH_V1") || !strings.Contains(string(template), "utc driver-run --binary") {
		return fmt.Errorf("UTC managed startup requires the UTC-aware service template; reinstall service units")
	}
	return nil
}

func (d *GNBDriver) checkUTCFeature() error {
	return d.checkUTCFeatures("utc-conf-v1")
}

func (d *GNBDriver) checkUTCFeatures(required ...string) error {
	output, err := d.Exec(d.gnbBinary(), "--version")
	if err != nil {
		return fmt.Errorf("UTC driver capability: %w", err)
	}
	features := map[string]bool{}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "Features:") {
			for _, feature := range strings.Fields(strings.TrimPrefix(line, "Features:")) {
				features[feature] = true
			}
		}
	}
	for _, feature := range required {
		if !features[feature] {
			return fmt.Errorf("UTC requires driver feature %s", feature)
		}
	}
	return nil
}

// Resolve existing ancestors too: /var/run and /run must not create different
// histories for the same instance, even after its configuration was deleted.
func canonicalUTCPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absolute)
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if info, statErr := os.Lstat(current); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("UTC configuration has a dangling symlink")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}
