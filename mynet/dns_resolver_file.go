package mynet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

type resolverFileBackup struct {
	Path     string `json:"path"`
	Original []byte `json:"original"`
	Applied  []byte `json:"applied"`
	Link     string `json:"link,omitempty"`
	Mode     uint32 `json:"mode"`
}

func usesResolverFile(osName string) bool {
	switch osName {
	case "linux", "freebsd", "openbsd", "netbsd":
		return true
	}
	return false
}
func resolverFileContent(original []byte, servers []string) []byte {
	var lines []string
	for _, line := range strings.Split(string(original), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "nameserver" {
			continue
		}
		lines = append(lines, line)
	}
	text := strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
	for _, server := range servers {
		text += "nameserver " + server + "\n"
	}
	return []byte(text)
}
func writeResolverFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".mynet.tmp"
	if err := os.WriteFile(tmp, data, mode.Perm()); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode.Perm()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
func applyResolverFileDNS(path string, servers []string, probe func(string) error, verify func() error) error {
	if len(servers) == 0 {
		return fmt.Errorf("explicit DNS servers required")
	}
	for _, server := range servers {
		if net.ParseIP(server) == nil {
			return fmt.Errorf("DNS server must be an IP: %q", server)
		}
	}
	if prev := LoadDNSBackup(); prev != nil && prev.Applied {
		if err := RestoreSystemDNS(); err != nil {
			return err
		}
	}
	if err := probe(servers[0]); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	state := resolverFileBackup{Path: path, Original: original, Applied: resolverFileContent(original, servers), Mode: uint32(info.Mode().Perm())}
	if info.Mode()&os.ModeSymlink != 0 {
		state.Link, err = os.Readlink(path)
		if err != nil {
			return err
		}
		targetInfo, err := os.Stat(path)
		if err != nil {
			return err
		}
		state.Mode = uint32(targetInfo.Mode().Perm())
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	backup := &DNSBackup{Platform: runtimeOS(), Applied: true, Target: strings.Join(servers, ","), AppliedAt: time.Now().UTC().Format(time.RFC3339), Extra: map[string]string{"resolver_file": string(raw)}}
	if err := saveDNSBackup(backup); err != nil {
		return err
	}
	if err := writeResolverFile(path, state.Applied, os.FileMode(state.Mode)); err != nil {
		return errors.Join(err, restoreResolverFileDNS(backup))
	}
	if err := verify(); err != nil {
		return errors.Join(err, restoreResolverFileDNS(backup))
	}
	return nil
}
func restoreResolverFileDNS(backup *DNSBackup) error {
	var state resolverFileBackup
	if err := json.Unmarshal([]byte(backup.Extra["resolver_file"]), &state); err != nil {
		return err
	}
	current, err := os.ReadFile(state.Path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(state.Path)
	if err != nil {
		return err
	}
	// 只有仍是本次写入的普通文件才归我们；保留用户或网络管理器的新设置。
	if info.Mode()&os.ModeSymlink == 0 && bytes.Equal(current, state.Applied) {
		if state.Link != "" {
			tmp := state.Path + ".mynet.tmp"
			_ = os.Remove(tmp)
			if err := os.Symlink(state.Link, tmp); err != nil {
				return err
			}
			if err := os.Rename(tmp, state.Path); err != nil {
				_ = os.Remove(tmp)
				return err
			}
		} else if err := writeResolverFile(state.Path, state.Original, os.FileMode(state.Mode)); err != nil {
			return err
		}
	}
	backup.Applied = false
	backup.RestoredAt = time.Now().UTC().Format(time.RFC3339)
	return saveDNSBackup(backup)
}
