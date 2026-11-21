package endpoint

import (
	"bufio"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
)

const maxConfigBytes = 64 << 10

// PeerSummary is the non-secret per-peer wiring declared in utcd.conf.
type PeerSummary struct {
	Name        string
	TransportID uint64
	UDPBind     string
	UDPTarget   string
	PairID      uint64
	Side        string
}

// ConfigSummary is a read-only, non-secret summary of utcd.conf for status reporting.
type ConfigSummary struct {
	ConfigVersion  uint32
	StateDir       string
	EdgeCandidates []string
	Peers          []PeerSummary
}

// ParseConfig reads the non-secret topology fields from utcd.conf without
// opening any identity/trust/material secret files.
func ParseConfig(path string) (ConfigSummary, error) {
	var summary ConfigSummary
	info, err := os.Lstat(path)
	if err != nil {
		return summary, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return summary, errors.New("utcd.conf 必须是普通文件且不超过 64KB")
	}
	file, err := os.Open(path)
	if err != nil {
		return summary, err
	}
	defer file.Close()

	peers := make(map[string]*PeerSummary)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key == "" || val == "" {
			continue
		}
		switch key {
		case "config_version":
			if v, err := strconv.ParseUint(val, 10, 32); err == nil {
				summary.ConfigVersion = uint32(v)
			}
		case "state_dir":
			summary.StateDir = val
		case "edge_candidates":
			for _, part := range strings.Split(val, ",") {
				if c := strings.TrimSpace(part); c != "" {
					summary.EdgeCandidates = append(summary.EdgeCandidates, c)
				}
			}
		default:
			if strings.HasPrefix(key, "peer.") {
				rest := strings.TrimPrefix(key, "peer.")
				name, field, ok := strings.Cut(rest, ".")
				if !ok || name == "" || field == "" {
					continue
				}
				entry := peers[name]
				if entry == nil {
					entry = &PeerSummary{Name: name}
					peers[name] = entry
				}
				switch field {
				case "transport_id":
					if v, err := strconv.ParseUint(val, 10, 64); err == nil {
						entry.TransportID = v
					}
				case "udp_bind":
					entry.UDPBind = val
				case "udp_target":
					entry.UDPTarget = val
				case "pair_id":
					if v, err := strconv.ParseUint(val, 10, 64); err == nil {
						entry.PairID = v
					}
				case "side":
					entry.Side = val
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return summary, err
	}

	names := make([]string, 0, len(peers))
	for name := range peers {
		names = append(names, name)
	}
	sort.Strings(names)
	summary.Peers = make([]PeerSummary, 0, len(names))
	for _, name := range names {
		summary.Peers = append(summary.Peers, *peers[name])
	}
	return summary, nil
}
