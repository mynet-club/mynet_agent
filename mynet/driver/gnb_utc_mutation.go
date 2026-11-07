package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
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
func (d *GNBDriver) ReadUTCProjection(ctx context.Context) (*UTCProjection, error) {
	var projection *UTCProjection
	err := d.withLifecycleCtx(ctx, func() error { var err error; projection, err = d.readUTCProjectionLocked(); return err })
	return projection, err
}

// PublishUTCPeer merges a single pair under the same cross-process node lock
// used by lifecycle and full snapshots. Concurrent A-B/A-C changes cannot
// overwrite each other. The coordinator owns pair_gen; this writer owns config_gen.
// The lock wait and durable write honour the caller's budget via ctx.
func (d *GNBDriver) PublishUTCPeer(ctx context.Context, scope, instance string, localID uint64, peer UTCPeerPolicy) (UTCProjection, error) {
	var published UTCProjection
	err := d.withLifecycleCtx(ctx, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		old, err := d.readUTCProjectionLocked()
		if err != nil {
			return err
		}
		next := UTCProjection{Scope: scope, Instance: instance, LocalID: localID, ConfigGen: 1, Peers: []UTCPeerPolicy{peer}}
		if old == nil {
			// A fresh driver ledger starts at the pair's authorization
			// generation. Adopting an existing on-disk configuration requires
			// validating it and stepping its watermark — and every existing
			// peer is imported, because a publish must never drop peers it
			// does not own: GNB blocks managed EXPORT peers missing from the
			// configuration. Anything unrecognised is refused outright.
			next.ConfigGen = peer.PairGen
			raw, readErr := os.ReadFile(filepath.Join(d.ConfDir, "utc.conf"))
			if readErr == nil {
				existing, parseErr := parseUTCConf(string(raw))
				if parseErr != nil || existing.Scope != scope || existing.Instance != instance ||
					existing.LocalID != localID {
					return fmt.Errorf("UTC existing configuration cannot be adopted")
				}
				if existing.ConfigGen == math.MaxUint64 {
					return fmt.Errorf("UTC configuration generation exhausted")
				}
				if existing.ConfigGen >= next.ConfigGen {
					next.ConfigGen = existing.ConfigGen + 1
				}
				next.Peers = append([]UTCPeerPolicy(nil), existing.Peers...)
				found := false
				for i, entry := range next.Peers {
					if entry.PeerID != peer.PeerID {
						continue
					}
					found = true
					if entry.PairID != peer.PairID || peer.PairGen < entry.PairGen {
						return fmt.Errorf("UTC pair rewind/conflict")
					}
					next.Peers[i] = peer
				}
				if !found {
					next.Peers = append(next.Peers, peer)
				}
			} else if !os.IsNotExist(readErr) {
				return fmt.Errorf("UTC existing configuration cannot be adopted")
			}
		}
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
		if err := ctx.Err(); err != nil {
			// The durable write completed after the caller's budget. The ledger
			// forbids rewinding or mutating content at the same pair generation
			// (pair_gen belongs to the coordinator), so the write stays on disk
			// as an UNCERTIFIED publish and the caller must compensate through
			// the blocking-generation path. Certifying it here is forbidden.
			return fmt.Errorf("UTC apply exceeded budget: publish is on disk but uncertified, caller must compensate: %w", err)
		}
		return nil
	})
	return published, err
}
