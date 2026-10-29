package mynet

// parseWindowsIfaceBytes 解析 Get-NetAdapterStatistics 的 JSON（跨平台可测）。

import (
	"encoding/json"
	"strings"
)

func parseWindowsIfaceBytes(out string) (rx, tx int64, mtu int, ok bool) {
	var raw struct {
		Rx  int64 `json:"rx"`
		Tx  int64 `json:"tx"`
		Mtu int   `json:"mtu"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &raw); err != nil {
		return 0, 0, 0, false
	}
	return raw.Rx, raw.Tx, raw.Mtu, true
}
