package localapi

import (
	"encoding/json"
	"io"
	"net/http"
)

// UTCLocalStatus is the local UTC acceleration toggle and runtime snapshot
// exposed by mynetd's localapi for desktop/local UI consumers.
type UTCLocalStatus struct {
	Enabled  bool             `json:"enabled"`
	Endpoint UTCEndpointState `json:"endpoint"`
	Hub      UTCHubState      `json:"hub"`
}

// UTCEndpointState describes the supervised local utcd endpoint process.
type UTCEndpointState struct {
	Configured bool   `json:"configured"`
	Running    bool   `json:"running"`
	ConfigPath string `json:"config_path,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

// UTCHubState describes the resident local hubd pair runtime.
type UTCHubState struct {
	Configured  bool           `json:"configured"`
	Running     bool           `json:"running"`
	Scope       string         `json:"scope,omitempty"`
	TransportID string         `json:"transport_id,omitempty"`
	AccountID   string         `json:"account_id,omitempty"`
	ConfigPath  string         `json:"config_path,omitempty"`
	LastError   string         `json:"last_error,omitempty"`
	Pairs       []UTCPairState `json:"pairs"`
}

// UTCPairState is one locally configured UTC pair and its last verified
// APPLIED watermark (16-digit JS-safe Core pair_id compatible).
type UTCPairState struct {
	PairID         uint64 `json:"pair_id"`
	PeerID         uint64 `json:"peer_id"`
	Side           uint8  `json:"side"`
	Mode           string `json:"mode"`
	Applied        bool   `json:"applied"`
	PairGen        uint64 `json:"pair_gen,omitempty"`
	LeaseEpoch     uint64 `json:"lease_epoch,omitempty"`
	PlacementEpoch uint64 `json:"placement_epoch,omitempty"`
	EdgeID         uint32 `json:"edge_id,omitempty"`
	ValidUntil     uint64 `json:"valid_until,omitempty"`
	ObservedAt     uint64 `json:"observed_at,omitempty"`
}

func (s *Server) registerUTCRoutes() {
	s.mux.HandleFunc("/api/v1/utc/status", getOnly(s.handleUTCStatus))
	s.mux.HandleFunc("/api/v1/utc/pairs", getOnly(s.handleUTCPairs))
	s.mux.HandleFunc("/api/v1/utc/acceleration", s.handleUTCAcceleration)
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
	status := s.UTCStatus()
	if status.Hub.Pairs == nil {
		status.Hub.Pairs = []UTCPairState{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    status,
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
	status := s.UTCStatus()
	pairs := status.Hub.Pairs
	if pairs == nil {
		pairs = []UTCPairState{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data": map[string]any{
			"enabled":      status.Enabled,
			"scope":        status.Hub.Scope,
			"transport_id": status.Hub.TransportID,
			"count":        len(pairs),
			"pairs":        pairs,
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
		status := s.UTCStatus()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"data": map[string]any{
				"enabled":          status.Enabled,
				"configured":       status.Endpoint.Configured || status.Hub.Configured,
				"endpoint_running": status.Endpoint.Running,
				"hub_running":      status.Hub.Running,
			},
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
	dec := json.NewDecoder(r.Body)
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
	if status.Hub.Pairs == nil {
		status.Hub.Pairs = []UTCPairState{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    status,
	})
}
