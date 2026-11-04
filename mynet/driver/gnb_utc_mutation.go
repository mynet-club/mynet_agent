package driver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
)

func (d *GNBDriver) readUTCProjectionLocked() (*UTCProjection, error) {
	history, _, err := d.utcHistoryPath()
	if err != nil {
		return nil, err
	}
	path := history + ".projection"
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		managed, err := d.utcManaged()
		if err != nil {
			return nil, err
		}
		if managed {
			return nil, fmt.Errorf("UTC projection history missing: recovery required")
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 131072 {
		return nil, fmt.Errorf("UTC projection history must be a bounded private file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var projection UTCProjection
	if err := decoder.Decode(&projection); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("UTC history trailing data")
	}
	if _, err := projection.canonical(); err != nil {
		return nil, err
	}
	return &projection, nil
}

// ReadUTCProjection reads the persistent node watermark, never live APPLIED.
func (d *GNBDriver) ReadUTCProjection() (*UTCProjection, error) {
	var projection *UTCProjection
	err := d.withLifecycle(func() error { var err error; projection, err = d.readUTCProjectionLocked(); return err })
	return projection, err
}

// PublishUTCPeer merges a single pair under the same cross-process node lock
// used by lifecycle and full snapshots. Concurrent A-B/A-C changes cannot
// overwrite each other. The coordinator owns pair_gen; this writer owns config_gen.
func (d *GNBDriver) PublishUTCPeer(scope, instance string, localID uint64, peer UTCPeerPolicy) (UTCProjection, error) {
	var published UTCProjection
	err := d.withLifecycle(func() error {
		old, err := d.readUTCProjectionLocked()
		if err != nil {
			return err
		}
		next := UTCProjection{Scope: scope, Instance: instance, LocalID: localID, ConfigGen: 1, Peers: []UTCPeerPolicy{peer}}
		if old != nil {
			if old.Scope != scope || old.Instance != instance || old.LocalID != localID {
				return fmt.Errorf("UTC scope mismatch")
			}
			next = *old
			next.Peers = append([]UTCPeerPolicy(nil), old.Peers...)
			found := false
			for i, existing := range next.Peers {
				if existing.PeerID != peer.PeerID {
					continue
				}
				found = true
				if existing == peer {
					// Retry the durable projection after an interrupted configuration publish.
					if err := d.applyUTCProjectionLocked(next); err != nil {
						return err
					}
					published = next
					return nil
				}
				next.Peers[i] = peer
			}
			if !found {
				next.Peers = append(next.Peers, peer)
			}
			if old.ConfigGen == math.MaxUint64 {
				return fmt.Errorf("UTC configuration generation exhausted")
			}
			next.ConfigGen++
		}
		if err := d.applyUTCProjectionLocked(next); err != nil {
			return err
		}
		published = next
		return nil
	})
	return published, err
}
