package hub

import (
	"errors"
	"fmt"
	"sort"
)

// PeerResponse binds one accepted control reply to the numeric peer id it
// represents. Grant v2 carries no numeric peer id, so the local installation
// supplies it for each pair.
type PeerResponse struct {
	PeerID   uint64
	Response Response
	// LocalEndpoint is required for an A-D reply and forbidden for A-S.
	LocalEndpoint *LocalEndpoint
}

// BuildProjectionMulti builds one utc-v2 projection from several accepted
// replies of the same installation. Peers are ordered by peer id; duplicate
// peer or pair ids, more than maxPeers peers and a mismatched scope are
// rejected. Manifests, epochs and expiry still come from each signed
// grant/binding; A-D takes its local client listener from PeerResponse.
func BuildProjectionMulti(identity Identity, responses []PeerResponse) (Projection, []byte, error) {
	if len(responses) == 0 {
		return Projection{}, nil, errors.New("Hub projection requires at least one peer")
	}
	if len(responses) > maxPeers {
		return Projection{}, nil, fmt.Errorf("Hub projection exceeds %d peers", maxPeers)
	}
	peers := make([]Peer, 0, len(responses))
	for _, item := range responses {
		if item.Response.Grant.Scope != identity.Scope {
			return Projection{}, nil, errors.New("projection scope does not match the grant")
		}
		peer, err := buildPeer(identity, item.PeerID, item.Response, item.LocalEndpoint)
		if err != nil {
			return Projection{}, nil, err
		}
		peers = append(peers, peer)
	}
	projection := projectionHeader(identity)
	projection.Peers = peers
	return finalizeProjection(projection)
}

// MergeProjections combines projections that share one utc-v2 header. It is
// for callers that already hold per-session projections instead of raw
// replies; the combined peer set is validated by Projection.Canonical.
func MergeProjections(projections []Projection) (Projection, []byte, error) {
	if len(projections) == 0 {
		return Projection{}, nil, errors.New("Hub projection requires at least one input")
	}
	merged := projections[0]
	merged.Peers = append([]Peer(nil), projections[0].Peers...)
	for _, projection := range projections[1:] {
		if !sameHeader(merged, projection) {
			return Projection{}, nil, errors.New("Hub projection headers differ")
		}
		merged.Peers = append(merged.Peers, projection.Peers...)
	}
	if len(merged.Peers) > maxPeers {
		return Projection{}, nil, fmt.Errorf("Hub projection exceeds %d peers", maxPeers)
	}
	return finalizeProjection(merged)
}

func sameHeader(a, b Projection) bool {
	return a.Scope == b.Scope && a.Instance == b.Instance && a.LocalID == b.LocalID &&
		a.ConfigGen == b.ConfigGen && a.BootID == b.BootID && a.PID == b.PID &&
		a.Capabilities == b.Capabilities
}

func finalizeProjection(projection Projection) (Projection, []byte, error) {
	sort.Slice(projection.Peers, func(i, j int) bool {
		return projection.Peers[i].PeerID < projection.Peers[j].PeerID
	})
	text, err := projection.Canonical()
	if err != nil {
		return Projection{}, nil, err
	}
	return projection, text, nil
}
