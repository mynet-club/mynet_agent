package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A publish that cannot take the lifecycle lock must give up at the caller's
// budget instead of waiting out the full lifecycle lock timeout.
func TestPublishUTCPeerLockWaitHonoursBudget(t *testing.T) {
	conf := t.TempDir()
	holder := NewGNBDriver(7)
	holder.ConfDir = conf
	release, err := holder.lockLifecycleCtx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	d := NewGNBDriver(7)
	d.ConfDir = conf
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = d.PublishUTCPeer(ctx, "lab", "node7", 7, UTCPeerPolicy{
		PeerID: 2, PairID: 12, PairGen: 1, Mode: 1,
		ExpiresAt: uint64(time.Now().Unix() + 600),
		Export:    "127.0.0.1:14001",
		Manifest:  strings.Repeat("1", 64),
	})
	if err == nil {
		t.Fatal("publish must fail while the lifecycle lock is held")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("lock wait outlived the caller budget: %s", elapsed)
	}
}

// A publish without budget must refuse before touching the disk.
func TestPublishUTCPeerExpiredBudgetWritesNothing(t *testing.T) {
	d := utcTestDriver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	time.Sleep(60 * time.Millisecond)
	_, err := d.PublishUTCPeer(ctx, "lab", "node7", 7, UTCPeerPolicy{
		PeerID: 2, PairID: 12, PairGen: 1, Mode: 1,
		ExpiresAt: uint64(time.Now().Unix() + 600),
		Export:    "127.0.0.1:14001",
		Manifest:  strings.Repeat("1", 64),
	})
	if err == nil {
		t.Fatal("publish must refuse without budget")
	}
	if _, statErr := os.Stat(filepath.Join(d.ConfDir, "utc.conf")); !os.IsNotExist(statErr) {
		t.Fatal("a refused publish must not write utc.conf")
	}
}

// The compensation for a late publish is the coordinator's blocking shape: a
// NEW pair generation whose permit deadline is in the past. Every unrelated
// peer survives, the pair goes inert, and the ledger stays monotone.
func TestPublishUTCPeerBlockingCompensationKeepsPeers(t *testing.T) {
	d := utcTestDriver(t)
	unrelated := UTCPeerPolicy{PeerID: 9, PairID: 79, PairGen: 3, Mode: 1,
		ExpiresAt: uint64(time.Now().Unix() + 600), Export: "127.0.0.1:14009",
		Manifest: strings.Repeat("a", 64)}
	target := UTCPeerPolicy{PeerID: 8, PairID: 78, PairGen: 2, Mode: 1,
		ExpiresAt: uint64(time.Now().Unix() + 600), Export: "127.0.0.1:14008",
		Manifest: strings.Repeat("b", 64)}
	if _, err := d.PublishUTCPeer(context.Background(), "lab", "node7", 7, unrelated); err != nil {
		t.Fatal(err)
	}
	published, err := d.PublishUTCPeer(context.Background(), "lab", "node7", 7, target)
	if err != nil {
		t.Fatal(err)
	}
	// The over-budget write is on disk and uncertified; compensate the way the
	// coordinator does (ExecNode.Block): larger generation, expired deadline.
	block := target
	block.PairGen = target.PairGen + 1
	block.ExpiresAt = 1
	compensated, err := d.PublishUTCPeer(context.Background(), "lab", "node7", 7, block)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(d.ConfDir, "utc.conf"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := parseUTCConf(string(raw))
	if err != nil {
		t.Fatalf("compensated utc.conf does not parse: %v\n%s", err, raw)
	}
	if state.ConfigGen != compensated.ConfigGen {
		t.Fatalf("config generation = %d, want %d", state.ConfigGen, compensated.ConfigGen)
	}
	blocked, ok := state.Peer(target.PeerID)
	if !ok || blocked.ExpiresAt != 1 || blocked.PairGen != block.PairGen {
		t.Fatalf("target pair not blocked: %+v", state.Peers)
	}
	kept, ok := state.Peer(unrelated.PeerID)
	if !ok || kept != unrelated {
		t.Fatalf("unrelated peer lost or altered: %+v", state.Peers)
	}
	if published.ConfigGen >= compensated.ConfigGen {
		t.Fatal("compensation must advance the configuration generation")
	}
}
