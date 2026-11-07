//go:build !windows

package driver

import (
	"context"
	"sync"
	"testing"
)

func TestUTCConcurrentPairsMergeWithoutLostNodeUpdates(t *testing.T) {
	d := utcTestDriver(t)
	first := utcProjection().Peers[0]
	second := first
	second.PeerID = 9
	second.PairID = 79
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, peer := range []UTCPeerPolicy{first, second} {
		wg.Add(1)
		go func(peer UTCPeerPolicy) {
			defer wg.Done()
			_, err := d.PublishUTCPeer(context.Background(), "lab", "node7", 7, peer)
			errors <- err
		}(peer)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	projection, err := d.ReadUTCProjection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if projection.ConfigGen != 2 || len(projection.Peers) != 2 {
		t.Fatalf("lost concurrent update: %+v", projection)
	}
	// A-B's release must not rewrite A-C or its expiry.
	first.PairGen = 2
	first.Mode = 0
	first.ExpiresAt = 0
	first.Export = ""
	projectionValue, err := d.PublishUTCPeer(context.Background(), "lab", "node7", 7, first)
	if err != nil {
		t.Fatal(err)
	}
	if projectionValue.ConfigGen != 3 {
		t.Fatalf("wrong node generation %d", projectionValue.ConfigGen)
	}
	for _, peer := range projectionValue.Peers {
		if peer.PeerID == 9 && peer != second {
			t.Fatal("unrelated pair changed")
		}
	}
	retry, err := d.PublishUTCPeer(context.Background(), "lab", "node7", 7, first)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ConfigGen != 3 {
		t.Fatal("idempotent retry minted new generation")
	}
	stale := utcProjection().Peers[0]
	if _, err := d.PublishUTCPeer(context.Background(), "lab", "node7", 7, stale); err == nil {
		t.Fatal("pair rollback accepted")
	}
}

func TestPublishUTCPeerSeedsConfigGenerationFromAuthorization(t *testing.T) {
	d := utcTestDriver(t)
	peer := utcProjection().Peers[0]
	peer.PairGen = 2
	published, err := d.PublishUTCPeer(context.Background(), "lab", "node7", 7, peer)
	if err != nil {
		t.Fatal(err)
	}
	if published.ConfigGen != 2 {
		t.Fatalf("fresh ledger config generation = %d, want 2 from the pair authorization", published.ConfigGen)
	}
}
