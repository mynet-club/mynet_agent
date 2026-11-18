package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func staticFixture(t *testing.T) (*server, string) {
	t.Helper()
	root := t.TempDir()
	pairs := filepath.Join(root, "pairs")
	for _, p := range []string{"200", "201"} {
		dir := filepath.Join(pairs, p)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{
			"lease-a.json":     `{"expires_at":4102444800}`,
			"lease-b.json":     `{"expires_at":4102444800}`,
			"revocations.json": `{"revision":1}`,
			"relay.trust":      "quota_account_id=acct",
			"client.trust":     "client-acct",
			"r1.floor":         "floor-bytes",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	spec := filepath.Join(root, "spec")
	var lines []string
	for _, p := range []uint64{200, 201} {
		for _, side := range []string{"A", "B"} {
			for i := 0; i < 12; i++ {
				lines = append(lines, fmt.Sprintf("t%d%s%d %d 5 %s", p, side, i, p, side))
			}
		}
	}
	if err := os.WriteFile(spec, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &server{
		cfg: config{PairsDir: pairs, SpecFile: spec, PoolSize: 3,
			PoolState:    filepath.Join(root, "pool.state"),
			PairUDPPorts: map[string]uint32{"200": 19701}},
		lastRev: make(map[uint64]uint64),
	}
	return s, pairs
}

func TestStaticPayloadServesMaterialAndPools(t *testing.T) {
	s, _ := staticFixture(t)
	m, err := s.buildStatic(200, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.ExpiresAt != 4102444800 || m.FloorName != "r1.floor" ||
		string(m.Floor) != "floor-bytes" || string(m.Trust) != "quota_account_id=acct" {
		t.Fatalf("material = %+v", m)
	}
	if got := string(m.TokensA); got != "t200A0\nt200A1\nt200A2\n" {
		t.Fatalf("tokens_a = %q", got)
	}
	if m.PoolSeq != 1 || len(m.NextA) == 0 {
		t.Fatalf("pool seq=%d next=%q", m.PoolSeq, m.NextA)
	}
	// second pair has its own offsets: pairs must not steal each other's pool
	m201, err := s.buildStatic(201, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if m201.PoolSeq != 1 {
		t.Fatalf("pair 201 seq = %d", m201.PoolSeq)
	}
}

func TestStaticHandlerRequiresEnrolledClient(t *testing.T) {
	s, _ := staticFixture(t)
	req := httptest.NewRequest("GET", "/v1/material?pair=200", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{
		{RawSubjectPublicKeyInfo: []byte("not-enrolled")}}}
	rec := httptest.NewRecorder()
	s.handleStatic(rec, req)
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	key := []byte("node-key")
	sum := sha256.Sum256(key)
	pin := hex.EncodeToString(sum[:])
	s.cfg.Transports = map[string]string{"node": pin}
	req = httptest.NewRequest("GET", "/v1/material?pair=200", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{
		{RawSubjectPublicKeyInfo: key}}}
	rec = httptest.NewRecorder()
	s.handleStatic(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["udp_port"] != float64(19701) || body["pool_seq"] != float64(1) {
		t.Fatalf("payload = %v", body)
	}
}

func TestStaticRevisionIsMonotonic(t *testing.T) {
	s, pairs := staticFixture(t)
	first, err := s.staticRevision(200, filepath.Join(pairs, "200"))
	if err != nil {
		t.Fatal(err)
	}
	if second, _ := s.staticRevision(200, filepath.Join(pairs, "200")); second != first {
		t.Fatalf("revision moved on read: %d -> %d", first, second)
	}
	if err := os.WriteFile(filepath.Join(pairs, "200", "revision"), []byte("9"), 0600); err != nil {
		t.Fatal(err)
	}
	if third, _ := s.staticRevision(200, filepath.Join(pairs, "200")); third < 9 {
		t.Fatalf("revision = %d, want >= 9", third)
	}
}

func TestSpecBaseSkipsSpacerHead(t *testing.T) {
	s, _ := staticFixture(t)
	s.cfg.SpecBase = 3
	m, err := s.buildStatic(200, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(m.TokensA); got != "t200A3\nt200A4\nt200A5\n" {
		t.Fatalf("tokens_a with spec_base = %q", got)
	}
	if got := string(m.NextA); got != "t200A6\nt200A7\nt200A8\n" {
		t.Fatalf("next_a with spec_base = %q", got)
	}
}

func TestSlotSlicesPartitionUsableRange(t *testing.T) {
	root := t.TempDir()
	var lines []string
	for _, p := range []string{"200"} {
		for _, side := range []string{"A", "B"} {
			for i := 0; i < 680; i++ {
				lines = append(lines, fmt.Sprintf("s%s%s%03d %s 5 %s", p, side, i, p, side))
			}
		}
	}
	spec := filepath.Join(root, "spec")
	if err := os.WriteFile(spec, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "200")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"lease-a.json":     `{"expires_at":4102444800}`,
		"lease-b.json":     `{"expires_at":4102444800}`,
		"revocations.json": `{"revision":1}`,
		"relay.trust":      "quota_account_id=acct",
		"r1.floor":         "floor",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := &server{
		cfg: config{PairsDir: root, SpecFile: spec, PoolSize: 30, SpecBase: 240,
			PoolState: filepath.Join(root, "pool.state")},
		lastRev: make(map[uint64]uint64),
	}
	seen := map[string]bool{}
	var prevNext []string
	for slot := 1; slot <= 13; slot++ {
		m, err := s.buildStatic(200, strconv.Itoa(slot), "", "")
		if err != nil {
			t.Fatalf("slot %d: %v", slot, err)
		}
		cur := strings.Fields(string(m.TokensA))
		nxt := strings.Fields(string(m.NextA))
		if len(cur) != 30 || len(nxt) != 30 {
			t.Fatalf("slot %d sizes cur=%d next=%d", slot, len(cur), len(nxt))
		}
		if prevNext != nil {
			if strings.Join(prevNext, ",") != strings.Join(cur, ",") {
				t.Fatalf("slot %d token set does not continue previous next", slot)
			}
		}
		// A slot's next is the following slot's live pool by design; only the
		// live (token) half must never repeat across slots.
		for _, tok := range cur {
			if seen[tok] {
				t.Fatalf("slot %d replays token %s", slot, tok)
			}
			seen[tok] = true
		}
		if cur[0] != fmt.Sprintf("s200A%03d", 240+(slot-1)*30) {
			t.Fatalf("slot %d starts at %s", slot, cur[0])
		}
		prevNext = nxt
	}
	if _, err := s.buildStatic(200, "14", "", ""); err == nil ||
		!strings.Contains(err.Error(), "out of range") {
		t.Fatalf("slot beyond usable range err = %v", err)
	}
}
