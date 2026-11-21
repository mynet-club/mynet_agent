package endpoint

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	maxLineBytes         = 511
	maxStateSocketBytes  = 108
	defaultUDPBindBase   = 19100
	defaultUDPTargetBase = 19200
	maxV3Peers           = 8
	maxV3EdgeCandidates  = 8
)

var (
	peerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)
	absPathPattern  = regexp.MustCompile(`^/[A-Za-z0-9_./-]{1,254}$`)
	hostPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*(?:\.[A-Za-z0-9][A-Za-z0-9_-]*)*$`)
)

// Refs holds the local node-owned root references read from utcd.refs.
// Private keys and certificates remain local and are never transmitted over APIs.
type Refs struct {
	IdentityRef    string
	TrustRef       string
	MaterialRef    string
	StateDir       string
	EdgeCandidates []string
}

// LoadRefs parses a strict key=value utcd.refs file.
func LoadRefs(path string) (Refs, error) {
	var refs Refs
	info, err := os.Lstat(path)
	if err != nil {
		return refs, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return refs, errors.New("utcd.refs 必须是普通文件且不超过 64KB")
	}
	file, err := os.Open(path)
	if err != nil {
		return refs, err
	}
	defer file.Close()

	seen := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return refs, errors.New("utcd.refs 包含无效行")
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key == "" || val == "" || seen[key] {
			return refs, fmt.Errorf("utcd.refs 键非法或重复: %s", key)
		}
		seen[key] = true
		switch key {
		case "identity_ref":
			refs.IdentityRef = val
		case "trust_ref":
			refs.TrustRef = val
		case "material_ref":
			refs.MaterialRef = val
		case "state_dir":
			refs.StateDir = val
		case "edge_candidates":
			for _, part := range strings.Split(val, ",") {
				if c := strings.TrimSpace(part); c != "" {
					refs.EdgeCandidates = append(refs.EdgeCandidates, c)
				}
			}
		default:
			return refs, fmt.Errorf("utcd.refs 包含未知键: %s", key)
		}
	}
	if err := scanner.Err(); err != nil {
		return refs, err
	}
	if err := validateRefs(refs); err != nil {
		return refs, err
	}
	return refs, nil
}

func validateAbsRef(name, value string) error {
	if value == "" {
		return fmt.Errorf("utcd.refs 缺少必填字段: %s", name)
	}
	if !absPathPattern.MatchString(value) {
		return fmt.Errorf("utcd.refs %s 必须是合法绝对路径", name)
	}
	for _, seg := range strings.Split(value[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("utcd.refs %s 包含非法路径段", name)
		}
	}
	if filepath.Base(value) == "utc.conf" {
		return fmt.Errorf("utcd.refs %s 不得指向 GNB 投影文件名 utc.conf", name)
	}
	return nil
}

func validateRefs(refs Refs) error {
	if err := validateAbsRef("identity_ref", refs.IdentityRef); err != nil {
		return err
	}
	if err := validateAbsRef("trust_ref", refs.TrustRef); err != nil {
		return err
	}
	if err := validateAbsRef("material_ref", refs.MaterialRef); err != nil {
		return err
	}
	if err := validateAbsRef("state_dir", refs.StateDir); err != nil {
		return err
	}
	if len(refs.StateDir+"/pairs/18446744073709551615-a/control.sock") >= maxStateSocketBytes {
		return errors.New("state_dir 路径过长，无法容纳配对控制 socket")
	}
	return nil
}

func validateEndpoint(value string) (int, error) {
	host, portStr, err := net.SplitHostPort(value)
	if err != nil {
		return 0, fmt.Errorf("无效端点 %q", value)
	}
	if len(host) < 1 || len(host) >= 64 || !hostPattern.MatchString(host) || strings.HasSuffix(host, "-") || strings.HasSuffix(host, "_") {
		return 0, fmt.Errorf("无效主机名 %q", value)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("无效端口 %q", value)
	}
	return port, nil
}

// RenderV3Config validates and renders a strict key=value utcd.conf (config_version=3).
func RenderV3Config(refs Refs, edgeCandidates []string, peers []PeerSummary) (string, error) {
	if err := validateRefs(refs); err != nil {
		return "", err
	}
	candidates := edgeCandidates
	if len(candidates) == 0 {
		candidates = refs.EdgeCandidates
	}
	if len(candidates) < 1 || len(candidates) > maxV3EdgeCandidates {
		return "", fmt.Errorf("edge_candidates 数量须在 1..%d 之间", maxV3EdgeCandidates)
	}
	cleanCandidates := make([]string, 0, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if _, err := validateEndpoint(c); err != nil {
			return "", fmt.Errorf("edge_candidates %w", err)
		}
		cleanCandidates = append(cleanCandidates, c)
	}
	if len(peers) < 1 || len(peers) > maxV3Peers {
		return "", fmt.Errorf("peers 数量须在 1..%d 之间", maxV3Peers)
	}

	sortedPeers := make([]PeerSummary, len(peers))
	copy(sortedPeers, peers)
	sort.Slice(sortedPeers, func(i, j int) bool {
		return sortedPeers[i].Name < sortedPeers[j].Name
	})

	lines := []string{
		"config_version=3",
		"identity_ref=" + refs.IdentityRef,
		"trust_ref=" + refs.TrustRef,
		"material_ref=" + refs.MaterialRef,
		"state_dir=" + refs.StateDir,
		"edge_candidates=" + strings.Join(cleanCandidates, ","),
	}

	seenName := make(map[string]bool, len(sortedPeers))
	seenTID := make(map[uint64]bool, len(sortedPeers))
	seenPort := make(map[int]bool, len(sortedPeers))

	for i, peer := range sortedPeers {
		name := strings.TrimSpace(peer.Name)
		if !peerNamePattern.MatchString(name) {
			return "", fmt.Errorf("非法 peer 名称: %q", peer.Name)
		}
		if seenName[name] {
			return "", fmt.Errorf("重复 peer 名称: %q", name)
		}
		seenName[name] = true
		if peer.TransportID == 0 {
			return "", fmt.Errorf("peer %s 缺少 transport_id", name)
		}
		if seenTID[peer.TransportID] {
			return "", fmt.Errorf("重复 transport_id: %d", peer.TransportID)
		}
		seenTID[peer.TransportID] = true

		bind := strings.TrimSpace(peer.UDPBind)
		if bind == "" {
			bind = fmt.Sprintf("127.0.0.1:%d", defaultUDPBindBase+i)
		}
		target := strings.TrimSpace(peer.UDPTarget)
		if target == "" {
			target = fmt.Sprintf("127.0.0.1:%d", defaultUDPTargetBase+i)
		}
		bindPort, err := validateEndpoint(bind)
		if err != nil {
			return "", fmt.Errorf("peer %s udp_bind %w", name, err)
		}
		if seenPort[bindPort] {
			return "", fmt.Errorf("peer %s udp_bind 端口 %d 冲突", name, bindPort)
		}
		seenPort[bindPort] = true
		if _, err := validateEndpoint(target); err != nil {
			return "", fmt.Errorf("peer %s udp_target %w", name, err)
		}

		lines = append(lines,
			fmt.Sprintf("peer.%s.transport_id=%d", name, peer.TransportID),
			fmt.Sprintf("peer.%s.udp_bind=%s", name, bind),
			fmt.Sprintf("peer.%s.udp_target=%s", name, target),
		)
	}

	var b strings.Builder
	for _, line := range lines {
		if len(line)+1 > maxLineBytes {
			return "", fmt.Errorf("配置行超过 %d 字节上限", maxLineBytes)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// WriteConfigAtomic writes rendered utcd.conf via temp file + rename (0600).
func WriteConfigAtomic(path, content string) error {
	if filepath.Base(path) == "utc.conf" {
		return errors.New("禁止写入 GNB 投影文件名 utc.conf")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".utcd.conf.tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
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
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
