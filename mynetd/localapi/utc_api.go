package localapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// UTCPeerState describes one peer entry configured in the local utcd.conf.
type UTCPeerState struct {
	Name        string `json:"name"`
	TransportID uint64 `json:"transport_id,omitempty"`
	UDPBind     string `json:"udp_bind,omitempty"`
	UDPTarget   string `json:"udp_target,omitempty"`
	PairID      uint64 `json:"pair_id,omitempty"`
	Side        string `json:"side,omitempty"`
}

// UTCPairState is the per-peer TCP view: local intent (desire), projection
// mode (what the cloud authorized) and runtime bridge/lease health.
//
// State values: tcp | fallback | pending | degraded | rejected | revoking | off.
type UTCPairState struct {
	PeerID      uint64 `json:"peer_id"`
	PairID      uint64 `json:"pair_id,omitempty"`
	PairGen     uint64 `json:"pair_gen,omitempty"`
	Intent      string `json:"intent,omitempty"`
	IntentState string `json:"intent_state,omitempty"`
	Mode        uint8  `json:"mode"`
	BridgeUp    bool   `json:"bridge_up"`
	LeaseValid  bool   `json:"lease_valid"`
	ExpiresAt   uint64 `json:"expires_at,omitempty"`
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	UpdatedAt   uint64 `json:"updated_at,omitempty"`
}

// UTCLocalStatus is the local UTC acceleration snapshot exposed to local UIs.
// Under ADR-0037 (一端一配), utcd.conf is the sole client configuration source.
type UTCLocalStatus struct {
	Enabled        bool           `json:"enabled"`
	Configured     bool           `json:"configured"`
	Running        bool           `json:"running"`
	ConfigPath     string         `json:"config_path,omitempty"`
	ConfigVersion  uint32         `json:"config_version,omitempty"`
	StateDir       string         `json:"state_dir,omitempty"`
	EdgeCandidates []string       `json:"edge_candidates"`
	Peers          []UTCPeerState `json:"peers"`
	LastError      string         `json:"last_error,omitempty"`
}

// UTCPairsResponse is the payload returned by GET /api/v1/utc/pairs.
type UTCPairsResponse struct {
	Enabled    bool           `json:"enabled"`
	Configured bool           `json:"configured"`
	Running    bool           `json:"running"`
	Count      int            `json:"count"`
	Peers      []UTCPeerState `json:"peers"`
	Pairs      []UTCPairState `json:"pairs"`
}

// UTCSyncPeer is one peer entry submitted to POST /api/v1/utc/sync.
type UTCSyncPeer struct {
	Name        string `json:"name"`
	TransportID uint64 `json:"transport_id"`
	UDPBind     string `json:"udp_bind,omitempty"`
	UDPTarget   string `json:"udp_target,omitempty"`
}

// UTCSyncRequest is the JSON payload accepted by POST /api/v1/utc/sync.
type UTCSyncRequest struct {
	EdgeCandidates []string      `json:"edge_candidates,omitempty"`
	Peers          []UTCSyncPeer `json:"peers"`
}

func (s *Server) registerUTCRoutes() {
	s.mux.HandleFunc("/api/v1/utc/status", getOnly(s.handleUTCStatus))
	s.mux.HandleFunc("/api/v1/utc/pairs", getOnly(s.handleUTCPairs))
	s.mux.HandleFunc("/api/v1/utc/pairs/", s.handleUTCPairIntent)
	s.mux.HandleFunc("/api/v1/utc/acceleration", s.handleUTCAcceleration)
	s.mux.HandleFunc("/api/v1/utc/sync", s.handleUTCSync)
}

func (s *Server) handleUTCStatus(w http.ResponseWriter, r *http.Request) {
	if s.UTCStatus == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"success":     false,
			"reason_code": "UTC_UNAVAILABLE",
			"message":     "本机 UTC 运行时未装配",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    normalizeUTCStatus(s.UTCStatus()),
	})
}

func (s *Server) handleUTCPairs(w http.ResponseWriter, r *http.Request) {
	if s.UTCStatus == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"success":     false,
			"reason_code": "UTC_UNAVAILABLE",
			"message":     "本机 UTC 运行时未装配",
		})
		return
	}
	st := normalizeUTCStatus(s.UTCStatus())
	pairs, err := s.pairStates()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"success":     false,
			"reason_code": "UTC_PAIRS_UNAVAILABLE",
			"message":     err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data": UTCPairsResponse{
			Enabled:    st.Enabled,
			Configured: st.Configured,
			Running:    st.Running,
			Count:      len(st.Peers),
			Peers:      st.Peers,
			Pairs:      pairs,
		},
	})
}

func (s *Server) pairStates() ([]UTCPairState, error) {
	if s.UTCPairs == nil {
		return []UTCPairState{}, nil
	}
	pairs, err := s.UTCPairs()
	if err != nil {
		return nil, err
	}
	if pairs == nil {
		pairs = []UTCPairState{}
	}
	return pairs, nil
}

// handleUTCPairIntent serves GET /api/v1/utc/pairs/<peer_id> (current view) and
// PUT /api/v1/utc/pairs/<peer_id> (record local intent + ask the cloud).
func (s *Server) handleUTCPairIntent(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		pairs, err := s.pairStates()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"success":     false,
				"reason_code": "UTC_PAIRS_UNAVAILABLE",
				"message":     err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": pairs})
	case http.MethodPut:
		rawPath := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/utc/pairs/"), "/")
		if strings.HasSuffix(rawPath, "/result") {
			s.withConfigMutation(s.handleUTCPairResultPut)(w, r)
			return
		}
		s.withConfigMutation(s.handleUTCPairIntentPut)(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"success": false,
			"message": "GET|PUT only",
		})
	}
}

func (s *Server) handleUTCPairResultPut(w http.ResponseWriter, r *http.Request) {
	if s.UTCPairResult == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"success":     false,
			"reason_code": "UTC_UNAVAILABLE",
			"message":     "本机 UTC 配对结果未装配",
		})
		return
	}
	rawID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/utc/pairs/"), "/")
	rawID = strings.TrimSuffix(strings.TrimSuffix(rawID, "/result"), "/")
	peerID, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil || peerID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success":     false,
			"reason_code": "INVALID_PEER",
			"message":     "path must be /api/v1/utc/pairs/<peer_id>/result",
		})
		return
	}
	var body struct {
		Result string `json:"result"`
		Reason string `json:"reason"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success":     false,
			"reason_code": "INVALID_BODY",
			"message":     "body must be {\"result\":\"pending\"|\"rejected\",\"reason\":\"...\"}",
		})
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success":     false,
			"reason_code": "INVALID_BODY",
			"message":     "unexpected trailing json",
		})
		return
	}
	if body.Result != "pending" && body.Result != "rejected" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success":     false,
			"reason_code": "INVALID_BODY",
			"message":     "result must be \"pending\" or \"rejected\"",
		})
		return
	}
	if err := s.UTCPairResult(peerID, body.Result, body.Reason); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"success":     false,
			"reason_code": "UTC_PAIR_RESULT_FAILED",
			"message":     err.Error(),
		})
		return
	}
	pairs, pairErr := s.pairStates()
	if pairErr != nil {
		pairs = []UTCPairState{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    map[string]any{"pairs": pairs},
	})
}

func (s *Server) handleUTCPairIntentPut(w http.ResponseWriter, r *http.Request) {
	if s.UTCPairIntent == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"success":     false,
			"reason_code": "UTC_UNAVAILABLE",
			"message":     "本机 UTC 配对意图未装配",
		})
		return
	}
	rawID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/utc/pairs/"), "/")
	peerID, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil || peerID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success":     false,
			"reason_code": "INVALID_PEER",
			"message":     "path must be /api/v1/utc/pairs/<peer_id>",
		})
		return
	}
	var body struct {
		Intent string `json:"intent"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success":     false,
			"reason_code": "INVALID_BODY",
			"message":     "body must be {\"intent\":\"on\"|\"off\"}",
		})
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success":     false,
			"reason_code": "INVALID_BODY",
			"message":     "unexpected trailing json",
		})
		return
	}
	var on bool
	switch body.Intent {
	case "on":
		on = true
	case "off":
		on = false
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success":     false,
			"reason_code": "INVALID_BODY",
			"message":     "intent must be \"on\" or \"off\"",
		})
		return
	}
	status, err := s.UTCPairIntent(peerID, on)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"success":     false,
			"reason_code": "UTC_PAIR_INTENT_FAILED",
			"message":     err.Error(),
		})
		return
	}
	pairs, pairErr := s.pairStates()
	if pairErr != nil {
		pairs = []UTCPairState{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data": map[string]any{
			"status": normalizeUTCStatus(status),
			"pairs":  pairs,
		},
	})
}

func (s *Server) handleUTCAcceleration(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if s.UTCStatus == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"success":     false,
				"reason_code": "UTC_UNAVAILABLE",
				"message":     "本机 UTC 运行时未装配",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"data":    normalizeUTCStatus(s.UTCStatus()),
		})
		return
	}
	if r.Method != http.MethodPut {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"success": false,
			"message": "GET|PUT only",
		})
		return
	}
	s.withConfigMutation(s.handleUTCAccelerationPut)(w, r)
}

func (s *Server) handleUTCAccelerationPut(w http.ResponseWriter, r *http.Request) {
	if s.UTCSetEnabled == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"success":     false,
			"reason_code": "UTC_UNAVAILABLE",
			"message":     "本机 UTC 运行时未装配",
		})
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success":     false,
			"reason_code": "INVALID_BODY",
			"message":     "enabled (boolean) is required",
		})
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success":     false,
			"reason_code": "INVALID_BODY",
			"message":     "unexpected trailing json",
		})
		return
	}
	status, err := s.UTCSetEnabled(*body.Enabled)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"success":     false,
			"reason_code": "UTC_TOGGLE_FAILED",
			"message":     err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    normalizeUTCStatus(status),
	})
}

func (s *Server) handleUTCSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"success": false,
			"message": "POST only",
		})
		return
	}
	s.withConfigMutation(s.handleUTCSyncPost)(w, r)
}

func (s *Server) handleUTCSyncPost(w http.ResponseWriter, r *http.Request) {
	if s.UTCSync == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"success":     false,
			"reason_code": "UTC_UNAVAILABLE",
			"message":     "本机 UTC 配置同步未装配",
		})
		return
	}
	var req UTCSyncRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success":     false,
			"reason_code": "INVALID_BODY",
			"message":     "invalid json: " + err.Error(),
		})
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success":     false,
			"reason_code": "INVALID_BODY",
			"message":     "unexpected trailing json",
		})
		return
	}
	if len(req.Peers) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success":     false,
			"reason_code": "INVALID_BODY",
			"message":     "peers must be non-empty",
		})
		return
	}
	status, err := s.UTCSync(req)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"success":     false,
			"reason_code": "UTC_SYNC_FAILED",
			"message":     err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    normalizeUTCStatus(status),
	})
}

func normalizeUTCStatus(st UTCLocalStatus) UTCLocalStatus {
	if st.EdgeCandidates == nil {
		st.EdgeCandidates = []string{}
	}
	if st.Peers == nil {
		st.Peers = []UTCPeerState{}
	}
	return st
}
