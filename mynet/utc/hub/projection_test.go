package hub

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func hubFixture(env, name string) string {
	if path := os.Getenv(env); path != "" {
		return path
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	candidate := filepath.Join(filepath.Dir(file), "..", "..", "..", "..",
		"mynet_utc", "tests", "fixtures", "hub_v2", name)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func sampleProjection() Projection {
	address := [16]byte{}
	address[10], address[11] = 0xff, 0xff
	address[12], address[13], address[14], address[15] = 203, 0, 113, 7
	return Projection{
		Scope:        "scope.test",
		Instance:     "instance-a",
		LocalID:      10,
		ConfigGen:    5,
		BootID:       strings.Repeat("ab", 16),
		PID:          123,
		Capabilities: CapabilityEpoch | CapabilityExplicitActivate,
		Peers: []Peer{{
			PeerID:         20,
			PairID:         30,
			PairGen:        2,
			Mode:           1,
			LeaseEpoch:     7,
			PlacementEpoch: 3,
			ExpiresAt:      200,
			Family:         4,
			Address:        address,
			Port:           9443,
			Manifest:       strings.Repeat("cd", 32),
		}},
	}
}

func TestProjectionCanonicalRoundTrip(t *testing.T) {
	projection := sampleProjection()
	raw, err := projection.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.BootID != projection.BootID || parsed.PID != projection.PID ||
		parsed.Capabilities != projection.Capabilities || len(parsed.Peers) != 1 {
		t.Fatalf("roundtrip mismatch: %+v", parsed)
	}
	if parsed.Peers[0].LeaseEpoch != 7 || parsed.Peers[0].PlacementEpoch != 3 {
		t.Fatalf("epochs lost: %+v", parsed.Peers[0])
	}
}

func TestProjectionGoldenFixture(t *testing.T) {
	path := hubFixture("HUB_V2_FIXTURE", "projection.txt")
	if path == "" {
		t.Fatal("cross-language fixture is required (set HUB_V2_FIXTURE or keep the sibling UTC worktree)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := projection.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, raw) {
		t.Fatal("golden fixture did not roundtrip")
	}
}

func TestProjectionNegativeFixture(t *testing.T) {
	path := hubFixture("HUB_V2_NEGATIVES", "negatives.txt")
	if path == "" {
		t.Fatal("cross-language fixture is required (set HUB_V2_NEGATIVES or keep the sibling UTC worktree)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cases := strings.Split(string(raw), "\n\n")
	if len(cases) < 13 {
		t.Fatalf("negative fixture has %d cases", len(cases))
	}
	for index, text := range cases {
		if text == "" {
			t.Fatalf("negative fixture case %d is empty", index)
		}
		if _, err := Parse([]byte(text)); err == nil {
			t.Fatalf("negative fixture case %d was accepted: %q", index, text)
		}
	}
}

func TestProjectionRejectsUnsafeRows(t *testing.T) {
	projection := sampleProjection()
	raw, err := projection.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(strings.Replace(string(raw), "utc-v2", "utc-v1", 1))); err == nil {
		t.Fatal("accepted v1 header")
	}
	if _, err := Parse([]byte(strings.Replace(string(raw), " 1 7 3 ", " 1 0 3 ", 1))); err == nil {
		t.Fatal("accepted non-canonical row")
	}
	bad := projection
	bad.Peers[0].Manifest = "00"
	if _, err := bad.Canonical(); err == nil {
		t.Fatal("accepted short manifest")
	}
	bad = projection
	bad.Peers[0].Mode = 0
	if _, err := bad.Canonical(); err == nil {
		t.Fatal("accepted direct peer with export fields")
	}
	bad = projection
	bad.Peers[0].PeerID = bad.LocalID
	if _, err := bad.Canonical(); err == nil {
		t.Fatal("accepted local peer")
	}
	bad = projection
	bad.Capabilities = 4
	if _, err := bad.Canonical(); err == nil {
		t.Fatal("accepted unknown capability")
	}
}

func TestProjectionRejectsUnusableExportAddresses(t *testing.T) {
	cases := map[string]struct {
		family  uint8
		address [16]byte
	}{
		"ipv4 unspecified": {4, [16]byte{10: 0xff, 11: 0xff}},
		"ipv4 multicast":   {4, [16]byte{10: 0xff, 11: 0xff, 12: 224}},
		"ipv6 unspecified": {6, [16]byte{}},
		"ipv6 multicast":   {6, [16]byte{0: 0xff, 1: 0x02, 15: 1}},
	}
	for name, test := range cases {
		projection := sampleProjection()
		projection.Peers[0].Family = test.family
		projection.Peers[0].Address = test.address
		if _, err := projection.Canonical(); err == nil {
			t.Fatalf("%s export address was accepted", name)
		}
	}
}
