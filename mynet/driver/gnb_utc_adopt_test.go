//go:build !windows

package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A first publish against an existing on-disk configuration must keep every
// peer it does not own: GNB blocks managed EXPORT peers missing from utc.conf,
// so dropping one would break live business.
func TestPublishUTCPeerAdoptionKeepsUnrelatedPeers(t *testing.T) {
	d := utcTestDriver(t)
	unrelated := UTCPeerPolicy{PeerID: 9, PairID: 79, PairGen: 3, Mode: 1,
		ExpiresAt: uint64(time.Now().Unix() + 600), Export: "127.0.0.1:14009",
		Manifest: strings.Repeat("a", 64)}
	target := UTCPeerPolicy{PeerID: 8, PairID: 78, PairGen: 1, Mode: 1,
		ExpiresAt: uint64(time.Now().Unix() + 600), Export: "127.0.0.1:14008",
		Manifest: strings.Repeat("b", 64)}
	existing := UTCProjection{Scope: "lab", Instance: "node7", LocalID: 7, ConfigGen: 7,
		Peers: []UTCPeerPolicy{target, unrelated}}
	text, err := existing.canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.ConfDir, "utc.conf"), text, 0600); err != nil {
		t.Fatal(err)
	}
	target.PairGen = 2
	target.Manifest = strings.Repeat("c", 64)
	if _, err := d.PublishUTCPeer(context.Background(), "lab", "node7", 7, target); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(d.ConfDir, "utc.conf"))
	if err != nil {
		t.Fatal(err)
	}
	published, err := parseUTCConf(string(raw))
	if err != nil {
		t.Fatalf("published utc.conf does not parse: %v\n%s", err, raw)
	}
	if published.ConfigGen != 8 {
		t.Fatalf("adopted config_gen = %d, want 8", published.ConfigGen)
	}
	kept, ok := published.Peer(9)
	if !ok || kept != unrelated {
		t.Fatalf("unrelated peer 9 lost or altered: %+v", published.Peers)
	}
	current, ok := published.Peer(8)
	if !ok || current.PairGen != 2 || current.Manifest != target.Manifest {
		t.Fatalf("target peer not merged: %+v", published.Peers)
	}
}

// A pair rewind on the replaced peer must be refused, matching the ledger path.
func TestPublishUTCPeerAdoptionRefusesPairRewind(t *testing.T) {
	d := utcTestDriver(t)
	target := UTCPeerPolicy{PeerID: 8, PairID: 78, PairGen: 3, Mode: 1,
		ExpiresAt: uint64(time.Now().Unix() + 600), Export: "127.0.0.1:14008",
		Manifest: strings.Repeat("b", 64)}
	existing := UTCProjection{Scope: "lab", Instance: "node7", LocalID: 7, ConfigGen: 7,
		Peers: []UTCPeerPolicy{target}}
	text, err := existing.canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.ConfDir, "utc.conf"), text, 0600); err != nil {
		t.Fatal(err)
	}
	target.PairGen = 2
	if _, err := d.PublishUTCPeer(context.Background(), "lab", "node7", 7, target); err == nil {
		t.Fatal("publish must refuse a pair generation rewind")
	}
}

// A configuration that cannot be imported losslessly must refuse the publish
// instead of silently dropping rows.
func TestPublishUTCPeerAdoptionRefusesUnimportableConfig(t *testing.T) {
	d := utcTestDriver(t)
	garbled := "utc-v1 lab node7 7 7\n" +
		"peer 9 79 3 1 9999999 4 7f000000000000000000000000000001 14009 0 " + strings.Repeat("a", 64) + "\n" +
		"garbage row\n"
	if err := os.WriteFile(filepath.Join(d.ConfDir, "utc.conf"), []byte(garbled), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := d.PublishUTCPeer(context.Background(), "lab", "node7", 7, UTCPeerPolicy{
		PeerID: 8, PairID: 78, PairGen: 1, Mode: 1,
		ExpiresAt: uint64(time.Now().Unix() + 600), Export: "127.0.0.1:14008",
		Manifest: strings.Repeat("b", 64)})
	if err == nil {
		t.Fatal("publish must refuse an unimportable configuration")
	}
	raw, err := os.ReadFile(filepath.Join(d.ConfDir, "utc.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != garbled {
		t.Fatalf("refused publish must not rewrite utc.conf:\n%s", raw)
	}
}
