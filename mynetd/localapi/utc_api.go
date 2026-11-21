package localapi

import (
	"encoding/json"
	"io"
	"net/http"
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
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data": UTCPairsResponse{
			Enabled:    st.Enabled,
			Configured: st.Configured,
			Running:    st.Running,
			Count:      len(st.Peers),
			Peers:      st.Peers,
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
