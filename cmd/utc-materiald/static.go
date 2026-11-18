package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Static pairs mode: the Hub keeps one directory per pair with the material it
// wants delivered (lease-a.json, lease-b.json, revocations.json, relay.trust,
// *.floor) and utc-materiald serves it with fresh token pools from the Core
// placement spec. Revision advances with the newest material file so any
// operator update installs once; unchanged material stays hot.

func (s *server) pairDir(pair uint64) string {
	return filepath.Join(s.cfg.PairsDir, strconv.FormatUint(pair, 10))
}

func (s *server) enrolledClient(pin string) bool {
	if len(s.cfg.Transports) == 0 {
		return false
	}
	for _, v := range s.cfg.Transports {
		if v == pin {
			return true
		}
	}
	return false
}

func (s *server) allowedPair(pair uint64) bool {
	if len(s.cfg.AllowedPairs) == 0 {
		return true
	}
	for _, p := range s.cfg.AllowedPairs {
		if p == pair {
			return true
		}
	}
	return false
}

func newestFloorFile(dir string) (string, []byte, error) {
	entries, err := filepath.Glob(filepath.Join(dir, "*.floor"))
	if err != nil || len(entries) == 0 {
		return "", nil, errors.New("no floor file")
	}
	newest := entries[0]
	var newestMod time.Time
	for _, e := range entries {
		st, err := os.Stat(e)
		if err != nil {
			continue
		}
		if st.ModTime().After(newestMod) {
			newest, newestMod = e, st.ModTime()
		}
	}
	data, err := os.ReadFile(newest)
	if err != nil {
		return "", nil, err
	}
	return filepath.Base(newest), data, nil
}

func leaseExpiry(raw []byte) (uint64, error) {
	var body struct {
		ExpiresAt uint64 `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return 0, err
	}
	if body.ExpiresAt == 0 {
		return 0, errors.New("lease has no expires_at")
	}
	return body.ExpiresAt, nil
}

// buildStatic assembles one pair payload. Everything except the token pools is
// read from disk on every call, so operator edits take effect on the next poll.
func (s *server) buildStatic(pair uint64, slotHint, side, baseHint string) (*material, error) {
	dir := s.pairDir(pair)
	leaseA, err := os.ReadFile(filepath.Join(dir, "lease-a.json"))
	if err != nil {
		return nil, err
	}
	leaseB, err := os.ReadFile(filepath.Join(dir, "lease-b.json"))
	if err != nil {
		return nil, err
	}
	revs, err := os.ReadFile(filepath.Join(dir, "revocations.json"))
	if err != nil {
		return nil, err
	}
	trustName := "relay.trust"
	if side == "a" {
		trustName = "client.trust"
	}
	trust, err := os.ReadFile(filepath.Join(dir, trustName))
	if err != nil {
		return nil, err
	}
	expires, err := leaseExpiry(leaseA)
	if err != nil {
		return nil, fmt.Errorf("pair %d: %w", pair, err)
	}
	revision, err := s.staticRevision(pair, dir)
	if err != nil {
		return nil, err
	}
	slot, err := strconv.ParseUint(slotHint, 10, 64)
	if err != nil || slot == 0 {
		slot = 1
	}
	raw, err := os.ReadFile(s.cfg.SpecFile)
	if err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}
	size := s.cfg.PoolSize
	if size <= 0 {
		size = 120
	}
	base, _ := strconv.ParseUint(baseHint, 10, 64)
	base += s.cfg.SpecBase
	tokensA, nextA, err := slotPools(specSideTokens(raw, pair, "A"), size, slot, base)
	if err != nil {
		return nil, fmt.Errorf("pair %d: %w", pair, err)
	}
	tokensB, nextB, err := slotPools(specSideTokens(raw, pair, "B"), size, slot, base)
	if err != nil {
		return nil, fmt.Errorf("pair %d: %w", pair, err)
	}
	seq := slot
	floorName, floor, err := newestFloorFile(dir)
	if err != nil {
		return nil, fmt.Errorf("pair %d: %w", pair, err)
	}
	return &material{
		Revision: revision, ExpiresAt: expires, LeaseA: leaseA, LeaseB: leaseB,
		Revocations: revs, FloorName: floorName, Floor: floor, Trust: trust,
		TokensA: tokensA, TokensB: tokensB, NextA: nextA, NextB: nextB,
		PoolSeq: seq,
	}, nil
}

// staticRevision uses an operator-maintained revision file when present,
// otherwise the newest material mtime. It never goes backwards within a
// process run, so a client cannot bounce between revisions.
func (s *server) staticRevision(pair uint64, dir string) (uint64, error) {
	rev := uint64(0)
	if raw, err := os.ReadFile(filepath.Join(dir, "revision")); err == nil {
		if v, convErr := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64); convErr == nil {
			rev = v
		}
	}
	names := []string{"lease-a.json", "lease-b.json", "revocations.json", "relay.trust"}
	for _, n := range names {
		st, err := os.Stat(filepath.Join(dir, n))
		if err != nil {
			return 0, err
		}
		if v := uint64(st.ModTime().Unix()); v > rev {
			rev = v
		}
	}
	if rev <= s.lastRev[pair] {
		rev = s.lastRev[pair]
	}
	s.lastRev[pair] = rev
	return rev, nil
}

func staticUDPPort(s *server, pair uint64) uint32 {
	if s.cfg.PairUDPPorts != nil {
		return s.cfg.PairUDPPorts[strconv.FormatUint(pair, 10)]
	}
	return s.cfg.UDPPort
}

// handleStatic serves /v1/material?pair=N. The mutual-TLS client certificate CN
// must be "pair-N": a node cannot ask for another pair's material.
func (s *server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pair, err := strconv.ParseUint(r.URL.Query().Get("pair"), 10, 64)
	if err != nil || pair == 0 {
		http.Error(w, "pair is required", http.StatusBadRequest)
		return
	}
	if !s.allowedPair(pair) {
		http.Error(w, "pair not served", http.StatusForbidden)
		return
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		http.Error(w, "client certificate required", http.StatusUnauthorized)
		return
	}
	// Authorization: an enrolled node certificate (SPKI pin). Pair selection is
	// explicit in the query; per-pair CN binding lands with the production PKI.
	sum := sha256.Sum256(r.TLS.PeerCertificates[0].RawSubjectPublicKeyInfo)
	if !s.enrolledClient(hex.EncodeToString(sum[:])) {
		http.Error(w, "client key is not enrolled", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	mat, err := s.buildStatic(pair, r.URL.Query().Get("slot"),
		r.URL.Query().Get("side"), r.URL.Query().Get("base"))
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	payload := map[string]any{
		"revision":    mat.Revision,
		"expires_at":  mat.ExpiresAt,
		"lease_a":     string(mat.LeaseA),
		"lease_b":     string(mat.LeaseB),
		"revocations": string(mat.Revocations),
		"trust":       string(mat.Trust),
		"floor_name":  mat.FloorName,
		"floor_b64":   base64.StdEncoding.EncodeToString(mat.Floor),
		"tokens_a":    string(mat.TokensA),
		"tokens_b":    string(mat.TokensB),
		"next_a":      string(mat.NextA),
		"next_b":      string(mat.NextB),
		"pool_seq":    mat.PoolSeq,
		"udp_port":    staticUDPPort(s, pair),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
