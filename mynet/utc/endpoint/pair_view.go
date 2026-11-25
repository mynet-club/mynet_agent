package endpoint

import (
	"os"
	"sort"

	"github.com/mynet-club/mynet_agent/mynet/utc/hub"
)

// Pair execution states exposed to the local UI. The UI must be able to say
// "你选择了走 TCP 但还没生效" (pending/degraded) and "你没选，正在走原生 UDP"
// (fallback/off) — the previous UI only showed the global switch, which made a
// peer that was never paired look identical to one that was healthy.
const (
	PairStateTCP      = "tcp"      // projection mode=2 + bridge up + lease valid
	PairStateFallback = "fallback" // effective native UDP (projection mode=0)
	PairStatePending  = "pending"  // user asked for TCP, no pair in the projection yet
	PairStateDegraded = "degraded" // pair exists but bridge/lease is not healthy
	PairStateRejected = "rejected" // cloud refused the intent (reason carries why)
	PairStateRevoking = "revoking" // user asked to disconnect, projection not refreshed yet
	PairStateOff      = "off"      // not selected, no pair
)

// PairView merges three sources for one peer: the local user intent, the gnb
// utc-v2 projection (what the cloud authorized), and the runtime lease/bridge
// (whether the TCP bridge is actually up).
type PairView struct {
	PeerID      uint64 `json:"peer_id"`
	PairID      uint64 `json:"pair_id,omitempty"`
	PairGen     uint64 `json:"pair_gen,omitempty"`
	Intent      string `json:"intent,omitempty"`       // on|off (empty = never selected)
	IntentState string `json:"intent_state,omitempty"` // pending|active|rejected|off
	Mode        uint8  `json:"mode"`
	BridgeUp    bool   `json:"bridge_up"`
	LeaseValid  bool   `json:"lease_valid"`
	ExpiresAt   uint64 `json:"expires_at,omitempty"`
	State       string `json:"state"` // tcp|fallback|pending|degraded|rejected|revoking|off
	Reason      string `json:"reason,omitempty"`
	UpdatedAt   uint64 `json:"updated_at,omitempty"`
}

func readProjection(path string) (hub.Projection, error) {
	var projection hub.Projection
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return projection, nil
	}
	if err != nil {
		return projection, err
	}
	return hub.Parse(raw)
}

// BuildPairViews returns one entry per peer that either has a local intent or
// appears in the current projection. Intents that never reached the projection
// stay visible as pending/rejected so the UI can explain the gap.
func BuildPairViews(stateDir, utcConfPath, myNetHome string, nowUnix uint64) ([]PairView, error) {
	intents, err := LoadPairIntents(myNetHome)
	if err != nil {
		return nil, err
	}
	projection, err := readProjection(utcConfPath)
	if err != nil {
		return nil, err
	}
	leases, _ := LoadInstalledPairLeases(stateDir)
	bridges, _ := loadBridgeStatusesWithAlive(stateDir, nil)

	byPeer := make(map[uint64]PairView, len(projection.Peers)+len(intents.List()))
	for _, peer := range projection.Peers {
		lease, hasLease := leases[peer.PairID]
		bridge, hasBridge := bridges[peer.PairID]
		view := PairView{
			PeerID:     peer.PeerID,
			PairID:     peer.PairID,
			PairGen:    peer.PairGen,
			Mode:       peer.Mode,
			ExpiresAt:  peer.ExpiresAt,
			LeaseValid: hasLease && lease.ExpiresAt > nowUnix,
			BridgeUp:   hasBridge && bridge.BridgeUp && bridge.Alive,
		}
		byPeer[peer.PeerID] = view
	}
	for _, pair := range intents.List() {
		view, ok := byPeer[pair.PeerID]
		if !ok {
			view = PairView{PeerID: pair.PeerID}
			byPeer[pair.PeerID] = view
		}
		view.Intent = pair.Intent
		if pair.RequestedAt > 0 {
			view.UpdatedAt = uint64(pair.RequestedAt)
		}
		byPeer[pair.PeerID] = view
	}

	views := make([]PairView, 0, len(byPeer))
	for peerID, view := range byPeer {
		pair, hasIntent := intents.Get(peerID)
		view = classifyPairView(view, pair, hasIntent)
		if view.State == "" {
			continue
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].PeerID < views[j].PeerID })
	return views, nil
}

// classifyPairView fills IntentState/State/Reason from the merged raw fields.
func classifyPairView(view PairView, pair PairIntent, hasIntent bool) PairView {
	exported := view.Mode == hub.PeerModeAD
	switch {
	case hasIntent && pair.LastResult == PairResultRejected:
		view.IntentState = "rejected"
		view.State = PairStateRejected
		if view.Reason == "" {
			view.Reason = pair.Reason
		}
		return view
	case hasIntent && !pair.Enabled():
		if exported && view.BridgeUp && view.LeaseValid {
			// Disconnect requested but the cloud has not dropped the pair yet.
			view.IntentState = "off"
			view.State = PairStateRevoking
			view.Reason = "revoke_pending"
			return view
		}
		view.IntentState = "off"
		view.State = PairStateOff
		return view
	case hasIntent && pair.Enabled():
		view.IntentState = "pending"
		switch {
		case view.PairID == 0:
			view.State = PairStatePending
			if view.Reason == "" {
				view.Reason = pair.Reason
			}
			if view.Reason == "" {
				view.Reason = "waiting_cloud"
			}
		case exported && view.BridgeUp && view.LeaseValid:
			view.IntentState = "active"
			view.State = PairStateTCP
		case exported:
			view.State = PairStateDegraded
			switch {
			case !view.LeaseValid:
				view.Reason = "lease_expired"
			case !view.BridgeUp:
				view.Reason = "bridge_down"
			default:
				view.Reason = "not_ready"
			}
		default:
			// Projection still says DIRECT: the cloud has not accepted yet.
			view.State = PairStateFallback
			view.Reason = "waiting_projection"
		}
		return view
	}

	// No local intent: report what the projection currently does.
	view.IntentState = "off"
	switch {
	case view.PairID == 0:
		return PairView{}
	case exported && view.BridgeUp && view.LeaseValid:
		view.State = PairStateTCP
	case exported:
		view.State = PairStateDegraded
		switch {
		case !view.LeaseValid:
			view.Reason = "lease_expired"
		case !view.BridgeUp:
			view.Reason = "bridge_down"
		}
	default:
		view.State = PairStateFallback
	}
	return view
}
