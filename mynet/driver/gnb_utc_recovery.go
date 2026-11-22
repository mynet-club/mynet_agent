package driver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// PublishUTCRecoveryV2 is the trusted v2 recovery marker publisher paired with
// utcHistory(). It verifies that the target GNB binary supports
// utc-state-dir-v1, validates or advances the authoritative projection ledger
// monotonically without dropping historical peers, ensures the private 0700
// state directory exists without disturbing any existing watermarks, and then
// atomically publishes the utc-managed-v2 marker.
func (d *GNBDriver) PublishUTCRecoveryV2(next *UTCProjection) (string, error) {
	var stateDir string
	err := d.withLifecycle(func() error {
		var innerErr error
		stateDir, innerErr = d.publishUTCRecoveryV2Locked(next)
		return innerErr
	})
	return stateDir, err
}

func loadUTCProjectionLedger(ledgerPath string) (*UTCProjection, error) {
	info, err := os.Lstat(ledgerPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 131072 {
		return nil, fmt.Errorf("UTC projection history must be a bounded private file")
	}
	raw, err := os.ReadFile(ledgerPath)
	if err != nil {
		return nil, err
	}
	var old UTCProjection
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&old); err != nil {
		return nil, fmt.Errorf("UTC projection history corrupt: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("UTC projection history has trailing data")
	}
	if _, err := old.canonical(); err != nil {
		return nil, err
	}
	return &old, nil
}

func (d *GNBDriver) publishUTCRecoveryV2Locked(next *UTCProjection) (string, error) {
	if runtime.GOOS == "windows" {
		return "", fmt.Errorf("UTC GNB execution unsupported on Windows")
	}
	if err := d.checkUTCFeatures("utc-conf-v1", "utc-state-dir-v1"); err != nil {
		return "", err
	}
	history, expectedV1, err := d.utcHistoryPath()
	if err != nil {
		return "", err
	}
	// Fail closed if an existing history marker is corrupt, symlinked, or non-private.
	if _, _, err := d.utcHistory(); err != nil {
		return "", err
	}

	ledgerPath := history + ".projection"
	old, err := loadUTCProjectionLedger(ledgerPath)
	if err != nil {
		return "", err
	}

	var effective UTCProjection
	var canonicalText []byte
	if next != nil {
		effective = *next
		canonicalText, err = effective.canonical()
		if err != nil {
			return "", err
		}
		if d.NodeID > 0 && uint64(d.NodeID) != effective.LocalID {
			return "", fmt.Errorf("UTC local node mismatch")
		}
		if old != nil {
			oldText, err := old.canonical()
			if err != nil {
				return "", err
			}
			if old.Scope != effective.Scope || old.Instance != effective.Instance || old.LocalID != effective.LocalID {
				return "", fmt.Errorf("UTC projection scope mismatch")
			}
			if effective.ConfigGen < old.ConfigGen || (effective.ConfigGen == old.ConfigGen && !bytes.Equal(canonicalText, oldText)) {
				return "", fmt.Errorf("UTC configuration rewind/conflict")
			}
			byPeer := map[uint64]UTCPeerPolicy{}
			for _, peer := range effective.Peers {
				byPeer[peer.PeerID] = peer
			}
			for _, peer := range old.Peers {
				current, ok := byPeer[peer.PeerID]
				if !ok {
					return "", fmt.Errorf("UTC projection must retain historical peer %d", peer.PeerID)
				}
				if current.PairID != peer.PairID || current.PairGen < peer.PairGen || (current.PairGen == peer.PairGen && current != peer) {
					return "", fmt.Errorf("UTC pair rewind/conflict")
				}
			}
		}
	} else {
		if old == nil {
			return "", fmt.Errorf("UTC recovery v2 requires an authoritative projection ledger")
		}
		effective = *old
		canonicalText, err = effective.canonical()
		if err != nil {
			return "", err
		}
		if d.NodeID > 0 && uint64(d.NodeID) != effective.LocalID {
			return "", fmt.Errorf("UTC local node mismatch")
		}
	}

	if err := os.MkdirAll(filepath.Dir(history), 0700); err != nil {
		return "", err
	}

	stateDir := history + ".state"
	if _, statErr := os.Lstat(stateDir); os.IsNotExist(statErr) {
		if err := os.Mkdir(stateDir, 0700); err != nil {
			return "", err
		}
	} else if statErr != nil {
		return "", statErr
	}
	if err := validateUTCStateDirectory(stateDir); err != nil {
		return "", err
	}
	if err := syncUTCHistory(stateDir); err != nil {
		return "", err
	}

	if next != nil {
		encoded, err := json.Marshal(effective)
		if err != nil {
			return "", err
		}
		if err := durableUTCWrite(ledgerPath, encoded); err != nil {
			return "", err
		}
	}

	if err := durableUTCWrite(filepath.Join(d.ConfDir, "utc.conf"), canonicalText); err != nil {
		return "", err
	}

	v2Content := strings.Replace(expectedV1, "utc-managed-v1\n", "utc-managed-v2\n", 1)
	if err := durableUTCWrite(history, []byte(v2Content)); err != nil {
		return "", err
	}
	return stateDir, nil
}
