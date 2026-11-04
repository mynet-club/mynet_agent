//go:build !windows

package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func utcProjection() UTCProjection {
	return UTCProjection{Scope: "lab", Instance: "node7", LocalID: 7, ConfigGen: 1, Peers: []UTCPeerPolicy{{PeerID: 8, PairID: 78, PairGen: 1, Mode: 1, ExpiresAt: 2000000000, Export: "127.0.0.1:14001", Manifest: strings.Repeat("1", 64)}}}
}
func TestUTCProjectionPersistsVersionsAndPublishesCanonicalConfig(t *testing.T) {
	d := utcTestDriver(t)
	p := utcProjection()
	if err := d.ApplyUTCProjection(p); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(d.ConfDir, "utc.conf"))
	if err != nil {
		t.Fatal(err)
	}
	want := "utc-v1 lab node7 7 1\npeer 8 78 1 1 2000000000 4 7f000001000000000000000000000000 14001 0 " + strings.Repeat("1", 64) + "\n"
	if string(config) != want {
		t.Fatalf("noncanonical projection: %q", config)
	}
	if err := d.ApplyUTCProjection(p); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	p.ConfigGen = 2
	p.Peers[0].PairGen = 2
	p.Peers[0].Mode = 0
	p.Peers[0].ExpiresAt = 0
	p.Peers[0].Export = ""
	if err := d.ApplyUTCProjection(p); err != nil {
		t.Fatal(err)
	}
	// Re-create a driver to prove the high water is read from disk.
	fresh := *d
	if err := fresh.ApplyUTCProjection(utcProjection()); err == nil {
		t.Fatal("old policy revived")
	}
	p.ConfigGen = 3
	p.Peers = nil
	if err := fresh.ApplyUTCProjection(p); err == nil {
		t.Fatal("history silently removed")
	}
}
func TestUTCProjectionFailedPublicationRetainsHighWaterForRetry(t *testing.T) {
	d := utcTestDriver(t)
	p := utcProjection()
	// A directory at utc.conf makes atomic publication fail after the journal.
	if err := os.Mkdir(filepath.Join(d.ConfDir, "utc.conf"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := d.ApplyUTCProjection(p); err == nil {
		t.Fatal("failed publication reported success")
	}
	if managed, err := d.utcManaged(); err != nil || !managed {
		t.Fatalf("lost management %v %v", managed, err)
	}
	if err := os.Remove(filepath.Join(d.ConfDir, "utc.conf")); err != nil {
		t.Fatal(err)
	}
	if err := d.ApplyUTCProjection(p); err != nil {
		t.Fatal(err)
	}
}
func TestUTCProjectionRejectsLostOrAmbiguousJournal(t *testing.T) {
	for _, variant := range []string{"missing", "trailing", "symlink"} {
		t.Run(variant, func(t *testing.T) {
			d := utcTestDriver(t)
			p := utcProjection()
			if err := d.ApplyUTCProjection(p); err != nil {
				t.Fatal(err)
			}
			history, _, _ := d.utcHistoryPath()
			ledger := history + ".projection"
			switch variant {
			case "missing":
				if err := os.Remove(ledger); err != nil {
					t.Fatal(err)
				}
			case "trailing":
				f, err := os.OpenFile(ledger, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString(" {}")
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(ledger, ledger+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(ledger+".old", ledger); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.ApplyUTCProjection(p); err == nil {
				t.Fatal("ambiguous history accepted")
			}
		})
	}
}
