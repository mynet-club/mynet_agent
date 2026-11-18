package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func specLines(pair uint64, side string, n int) []string {
	var lines []string
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf("tok-%s-%03d %d x %s", side, i, pair, side))
	}
	return lines
}

func writeSpec(t *testing.T, dir string, pair uint64, a, b int) string {
	t.Helper()
	path := filepath.Join(dir, "spec")
	lines := append(specLines(pair, "A", a), specLines(pair, "B", b)...)
	lines = append(lines, fmt.Sprintf("tok-A-000 %d x unpaired", pair+1))
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNextPoolsSlicesAndAdvances(t *testing.T) {
	dir := t.TempDir()
	s := &server{cfg: config{
		SpecFile:  writeSpec(t, dir, 200, 12, 12),
		PairID:    200,
		PoolSize:  3,
		PoolState: filepath.Join(dir, "state.json"),
	}}
	a, b, nextA, nextB, seq, err := s.nextPools()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(a); got != "tok-A-000\ntok-A-001\ntok-A-002\n" {
		t.Fatalf("a pool = %q", got)
	}
	if got := string(nextA); got != "tok-A-003\ntok-A-004\ntok-A-005\n" {
		t.Fatalf("next a = %q", got)
	}
	if got := string(b); got != "tok-B-000\ntok-B-001\ntok-B-002\n" {
		t.Fatalf("b pool = %q", got)
	}
	if got := string(nextB); got != "tok-B-003\ntok-B-004\ntok-B-005\n" {
		t.Fatalf("next b = %q", got)
	}
	if seq != 1 {
		t.Fatalf("first rotation seq = %d, want 1", seq)
	}

	a2, _, _, _, seq2, err := s.nextPools()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(a2); got != "tok-A-006\ntok-A-007\ntok-A-008\n" {
		t.Fatalf("second a pool = %q", got)
	}
	if seq2 != 2 {
		t.Fatalf("second rotation seq = %d, want 2", seq2)
	}
}

func TestNextPoolsExhaustionNeverReplays(t *testing.T) {
	dir := t.TempDir()
	s := &server{cfg: config{
		SpecFile:  writeSpec(t, dir, 200, 6, 6),
		PairID:    200,
		PoolSize:  3,
		PoolState: filepath.Join(dir, "state.json"),
	}}
	if _, _, _, _, _, err := s.nextPools(); err != nil {
		t.Fatalf("first rotation: %v", err)
	}
	_, _, _, _, _, err := s.nextPools()
	if err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("second rotation err = %v, want exhaustion", err)
	}
}

func TestSpecSideTokensFiltersPairAndSide(t *testing.T) {
	raw := []byte("t1 200 x A\nt2 200 x B\nt3 201 x A\nbroken\nt4 200 x A\n")
	got := specSideTokens(raw, 200, "A")
	if strings.Join(got, ",") != "t1,t4" {
		t.Fatalf("side A = %v", got)
	}
	if got := specSideTokens(raw, 200, "C"); len(got) != 0 {
		t.Fatalf("side C = %v", got)
	}
}
