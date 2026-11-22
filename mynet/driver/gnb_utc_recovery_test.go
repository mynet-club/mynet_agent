//go:build !windows

package driver

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testRecoveryProjection(localID, configGen, pairGen uint64) UTCProjection {
	return UTCProjection{
		Scope:     "mynet-prod",
		Instance:  "n178",
		LocalID:   localID,
		ConfigGen: configGen,
		Peers: []UTCPeerPolicy{
			{
				PeerID:    8283289760467957,
				PairID:    3960905695327723,
				PairGen:   pairGen,
				Mode:      1,
				ExpiresAt: 1900000000,
				Export:    "127.0.0.1:41001",
				Manifest:  strings.Repeat("a", 64),
			},
		},
	}
}

func TestPublishUTCRecoveryV2FromExistingV1LedgerAndPreservesWatermarks(t *testing.T) {
	d := utcTestDriver(t)
	d.Exec = func(string, ...string) ([]byte, error) {
		return []byte("GNB\nFeatures: proxy utc-conf utc-conf-v1 utc-state-dir-v1\n"), nil
	}
	proj := testRecoveryProjection(3722810996882027, 3, 1)
	if err := d.ApplyUTCProjection(proj); err != nil {
		t.Fatal(err)
	}

	stateDir, err := d.PublishUTCRecoveryV2(nil)
	if err != nil {
		t.Fatalf("PublishUTCRecoveryV2(nil) failed: %v", err)
	}
	info, err := os.Lstat(stateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("stateDir invalid: info=%v err=%v", info, err)
	}

	args, err := d.UTCLaunchArgs()
	if err != nil {
		t.Fatalf("UTCLaunchArgs after v2 recovery failed: %v", err)
	}
	want := append([]string{"-c", d.ConfDir}, d.gnbExplicitPathArgs()...)
	want = append(want, "--utc-state-dir", stateDir, "--utc-managed")
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("got args=%v want=%v", args, want)
	}

	// Pre-existing watermark inside .state must survive idempotent re-runs & upgrades.
	wmPath := filepath.Join(stateDir, "watermark.bin")
	if err := os.WriteFile(wmPath, []byte("keep-watermark"), 0600); err != nil {
		t.Fatal(err)
	}
	next := testRecoveryProjection(3722810996882027, 4, 2)
	stateDir2, err := d.PublishUTCRecoveryV2(&next)
	if err != nil || stateDir2 != stateDir {
		t.Fatalf("monotonic upgrade failed: dir=%s err=%v", stateDir2, err)
	}
	gotWM, err := os.ReadFile(wmPath)
	if err != nil || string(gotWM) != "keep-watermark" {
		t.Fatalf("watermark not preserved: %q err=%v", string(gotWM), err)
	}
}

func TestPublishUTCRecoveryV2FailsClosed(t *testing.T) {
	t.Run("missing-state-dir-feature", func(t *testing.T) {
		d := utcTestDriver(t)
		proj := testRecoveryProjection(3722810996882027, 1, 1)
		if err := d.ApplyUTCProjection(proj); err != nil {
			t.Fatal(err)
		}
		if _, err := d.PublishUTCRecoveryV2(nil); err == nil {
			t.Fatal("accepted binary without utc-state-dir-v1")
		}
	})

	t.Run("missing-authoritative-projection", func(t *testing.T) {
		d := utcTestDriver(t)
		d.Exec = func(string, ...string) ([]byte, error) {
			return []byte("Features: utc-conf-v1 utc-state-dir-v1\n"), nil
		}
		if _, err := d.PublishUTCRecoveryV2(nil); err == nil {
			t.Fatal("published v2 without authoritative projection")
		}
	})

	t.Run("rewind-and-dropped-peer-rejected", func(t *testing.T) {
		d := utcTestDriver(t)
		d.Exec = func(string, ...string) ([]byte, error) {
			return []byte("Features: utc-conf-v1 utc-state-dir-v1\n"), nil
		}
		proj := testRecoveryProjection(3722810996882027, 5, 2)
		if err := d.ApplyUTCProjection(proj); err != nil {
			t.Fatal(err)
		}
		rewind := testRecoveryProjection(3722810996882027, 4, 2)
		if _, err := d.PublishUTCRecoveryV2(&rewind); err == nil {
			t.Fatal("accepted config_gen rewind")
		}
		pairRewind := testRecoveryProjection(3722810996882027, 6, 1)
		if _, err := d.PublishUTCRecoveryV2(&pairRewind); err == nil {
			t.Fatal("accepted pair_gen rewind")
		}
		dropped := proj
		dropped.ConfigGen = 6
		dropped.Peers = nil
		if _, err := d.PublishUTCRecoveryV2(&dropped); err == nil {
			t.Fatal("accepted dropped historical peer")
		}
	})

	t.Run("public-or-symlink-state-dir-rejected", func(t *testing.T) {
		d := utcTestDriver(t)
		d.Exec = func(string, ...string) ([]byte, error) {
			return []byte("Features: utc-conf-v1 utc-state-dir-v1\n"), nil
		}
		proj := testRecoveryProjection(3722810996882027, 1, 1)
		if err := d.ApplyUTCProjection(proj); err != nil {
			t.Fatal(err)
		}
		history, _, err := d.utcHistoryPath()
		if err != nil {
			t.Fatal(err)
		}
		stateDir := history + ".state"
		if err := os.Mkdir(stateDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(stateDir, 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := d.PublishUTCRecoveryV2(nil); err == nil {
			t.Fatal("accepted world-readable state dir")
		}
	})
}
