package hubd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusUsageAndMeterMapping(t *testing.T) {
	boot := strings.Repeat("ab", 16)
	manifest := strings.Repeat("cd", 32)
	digest := strings.Repeat("ef", 64)
	raw := "status-v1 scope instance 42 " + boot + " 1 1700000000 ok 123\n" +
		"metrics 0 0 0 0\n" +
		"peer 7 8 3 APPLIED POLICY_LOADED " + manifest + " " + digest + "\n" +
		"usage 7 1000 400\n"
	snapshot, err := parseStatus([]byte(raw))
	if err != nil {
		t.Fatalf("parseStatus: %v", err)
	}
	if got := snapshot.Usage[7]; got.Tx != 1000 || got.Rx != 400 {
		t.Fatalf("usage = %+v, want tx=1000 rx=400", got)
	}
	sideA := meterLines([]PairConfig{{PairID: 8, PeerID: 7, Side: 0}}, snapshot)
	if len(sideA) != 1 || sideA[0] != (meterLine{PairID: 8, PairGen: 3, BytesAB: 1000, BytesBA: 400}) {
		t.Fatalf("side A lines = %+v", sideA)
	}
	sideB := meterLines([]PairConfig{{PairID: 8, PeerID: 7, Side: 1}}, snapshot)
	if len(sideB) != 1 || sideB[0] != (meterLine{PairID: 8, PairGen: 3, BytesAB: 400, BytesBA: 1000}) {
		t.Fatalf("side B lines = %+v", sideB)
	}
	if lines := meterLines([]PairConfig{{PairID: 9, PeerID: 99}}, snapshot); len(lines) != 0 {
		t.Fatalf("unknown peer should be skipped, got %+v", lines)
	}
}

func TestWriteMeterFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "endpoint-counters")
	lines := []meterLine{{PairID: 8, PairGen: 3, BytesAB: 10, BytesBA: 20}}
	if err := writeMeterFile(path, lines); err != nil {
		t.Fatalf("writeMeterFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# utc-endpoint-counters v1\n8 3 10 20\n" {
		t.Fatalf("content = %q", data)
	}
}
