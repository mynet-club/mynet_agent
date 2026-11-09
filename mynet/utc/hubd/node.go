package hubd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/utc/hub"
)

// ErrCountersUnavailable is returned by FileNode.Verify. GNB utc.status
// exposes only boot-lifetime counters, which cannot prove this operation's
// bidirectional traffic; fabricating VERIFIED would be false evidence.
var ErrCountersUnavailable = errors.New("GNB does not expose operation-bound UTC counters")

// FileNode is the real executor.Node seam for one local GNB: it atomically
// publishes utc.conf and reads utc.status. It never reports APPLIED unless the
// status file says so for the current process identity.
type FileNode struct {
	Identity     hub.Identity
	ConfDir      string
	StatusPath   string
	ApplyTimeout time.Duration
	Poll         time.Duration
	Now          func() time.Time

	// Writable allows tests or a future privileged command seam to intercept
	// the file write. Nil means hub.WriteFileAtomic.
	Writable func(path string, data []byte) error
}

func (n *FileNode) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *FileNode) write(path string, data []byte) error {
	if n.Writable != nil {
		return n.Writable(path, data)
	}
	return hub.WriteFileAtomic(path, data)
}

// Apply publishes the local side of plan and requires the matching APPLIED
// status row before returning. A non-local side is refused, never simulated.
func (n *FileNode) Apply(ctx context.Context, plan hub.Plan, side int) (hub.Receipt, error) {
	var receipt hub.Receipt
	if err := plan.Validate(); err != nil {
		return receipt, err
	}
	if side != 0 && side != 1 {
		return receipt, errors.New("hub plan side is invalid")
	}
	if plan.Sides[side].NodeID != n.Identity.LocalID {
		return receipt, errors.New("hub plan side is not this node")
	}
	peer := plan.Sides[1-side]
	endpoint, err := netip.ParseAddrPort(peer.Export)
	if err != nil || endpoint.Port() == 0 {
		return receipt, errors.New("hub plan peer export is invalid")
	}
	var address [16]byte
	family := uint8(6)
	if endpoint.Addr().Is4() {
		family = 4
		value := endpoint.Addr().As4()
		address[10], address[11] = 0xff, 0xff
		copy(address[12:], value[:])
	} else {
		if endpoint.Addr().Zone() != "" || endpoint.Addr().Is4In6() {
			return receipt, errors.New("hub plan peer export is invalid")
		}
		address = endpoint.Addr().As16()
	}
	projection := hub.Projection{
		Scope: n.Identity.Scope, Instance: n.Identity.Instance,
		LocalID: n.Identity.LocalID, ConfigGen: n.Identity.ConfigGen,
		BootID: n.Identity.BootID, PID: n.Identity.PID,
		Capabilities: n.Identity.Capabilities,
		Peers: []hub.Peer{{
			PeerID: peer.NodeID, PairID: plan.PairID, PairGen: plan.PairGen,
			Mode: 1, LeaseEpoch: plan.LeaseEpoch, PlacementEpoch: plan.PlacementEpoch,
			ExpiresAt: plan.ValidUntil, Family: family, Address: address,
			Port: endpoint.Port(), ScopeID: peer.ScopeID, Manifest: plan.Manifest,
		}},
	}
	text, err := projection.Canonical()
	if err != nil {
		return receipt, err
	}
	if err := n.write(filepath.Join(n.ConfDir, "utc.conf"), text); err != nil {
		return receipt, err
	}
	timeout, poll := n.ApplyTimeout, n.Poll
	if timeout <= 0 {
		timeout = defaultApplyTimeout
	}
	if poll <= 0 {
		poll = defaultStatusPoll
	}
	if err := n.waitApplied(ctx, plan.PairID, plan.PairGen, timeout, poll); err != nil {
		return receipt, err
	}
	sum := sha256.Sum256(text)
	return hub.Receipt{
		OperationID: plan.OperationID, Scope: plan.Scope, PairID: plan.PairID,
		PairGen: plan.PairGen, LeaseEpoch: plan.LeaseEpoch,
		PlacementEpoch: plan.PlacementEpoch, NodeID: n.Identity.LocalID,
		BootID: n.Identity.BootID, PID: n.Identity.PID,
		ObservedAt: uint64(n.now().Unix()), ValidUntil: plan.ValidUntil,
		State: hub.StateApplied, ProbeDigest: hex.EncodeToString(sum[:]),
	}, nil
}

// Verify refuses to fabricate VERIFIED. The executor must treat this error as
// "not yet verifiable", not as success.
func (n *FileNode) Verify(context.Context, hub.Plan, int) (hub.Receipt, error) {
	return hub.Receipt{}, ErrCountersUnavailable
}

func (n *FileNode) waitApplied(ctx context.Context, pairID, pairGen uint64, timeout, poll time.Duration) error {
	statusPath := n.StatusPath
	if statusPath == "" {
		statusPath = filepath.Join(n.ConfDir, "utc.status")
	}
	deadline := time.Now().Add(timeout)
	last := errors.New("utc.status is not available")
	for {
		raw, err := readStatusFile(statusPath)
		if err == nil {
			snapshot, parseErr := parseStatus(raw)
			if parseErr == nil {
				if appliedErr := snapshot.applied(n.Identity, pairID, pairGen, n.now(), defaultStatusMaxAge); appliedErr == nil {
					return nil
				} else {
					last = appliedErr
				}
			} else {
				last = parseErr
			}
		} else {
			last = err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("utc.status did not report APPLIED: %w", last)
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
