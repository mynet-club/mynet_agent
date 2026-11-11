package hubd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// meterLine is the endpoint counter row consumed by `utc-meter watch`:
// pair_id pair_gen bytes_ab bytes_ba.
type meterLine struct {
	PairID  uint64
	PairGen uint64
	BytesAB uint64
	BytesBA uint64
}

// meterLines maps GNB per-peer usage onto the two directions from the
// configured side. A sends A->B and receives B->A; B mirrors that.
func meterLines(pairs []PairConfig, snapshot statusSnapshot) []meterLine {
	lines := make([]meterLine, 0, len(pairs))
	for _, pair := range pairs {
		peer, ok := snapshot.Peers[pair.PeerID]
		if !ok {
			continue
		}
		usage := snapshot.Usage[pair.PeerID]
		line := meterLine{PairID: pair.PairID, PairGen: peer.PairGen}
		if pair.Side == 0 {
			line.BytesAB = usage.Tx
			line.BytesBA = usage.Rx
		} else {
			line.BytesAB = usage.Rx
			line.BytesBA = usage.Tx
		}
		lines = append(lines, line)
	}
	return lines
}

// writeMeterFile replaces the counter file atomically with 0600 permissions.
func writeMeterFile(path string, lines []meterLine) error {
	if path == "" {
		return nil
	}
	var text strings.Builder
	text.WriteString("# utc-endpoint-counters v1\n")
	for _, line := range lines {
		fmt.Fprintf(&text, "%d %d %d %d\n", line.PairID, line.PairGen,
			line.BytesAB, line.BytesBA)
	}
	staging := fmt.Sprintf("%s.new.%d", path, os.Getpid())
	file, err := os.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(text.String()); err != nil {
		file.Close()
		os.Remove(staging)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(staging)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(staging)
		return err
	}
	if err := os.Rename(staging, path); err != nil {
		os.Remove(staging)
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
