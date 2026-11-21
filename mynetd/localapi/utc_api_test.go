package localapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUTCRoutesUnavailableFailClosed(t *testing.T) {
	srv := NewServer(Config{})
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/utc/status", ""},
		{http.MethodGet, "/api/v1/utc/pairs", ""},
		{http.MethodGet, "/api/v1/utc/acceleration", ""},
		{http.MethodPut, "/api/v1/utc/acceleration", `{"enabled":true}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: got %d, want 503 (%s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestUTCStatusPairsAndToggle(t *testing.T) {
	srv := NewServer(Config{})
	state := UTCLocalStatus{
		Enabled:        false,
		Configured:     true,
		Running:        false,
		ConfigPath:     "conf/utc/utcd.conf",
		ConfigVersion:  3,
		StateDir:       "utc-state",
		EdgeCandidates: []string{"edge-a:4433"},
		Peers: []UTCPeerState{
			{
				Name:        "n182",
				TransportID: 1234567890123456,
				UDPBind:     "127.0.0.1:19001",
				UDPTarget:   "127.0.0.1:9001",
			},
		},
	}

	srv.UTCStatus = func() UTCLocalStatus { return state }
	srv.UTCSetEnabled = func(enabled bool) (UTCLocalStatus, error) {
		state.Enabled = enabled
		state.Running = enabled
		return state, nil
	}

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/utc/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status got %d: %s", rec.Code, rec.Body.String())
	}
	var statusResp struct {
		Success bool           `json:"success"`
		Data    UTCLocalStatus `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !statusResp.Success || statusResp.Data.Enabled || !statusResp.Data.Configured ||
		statusResp.Data.ConfigVersion != 3 || len(statusResp.Data.Peers) != 1 ||
		statusResp.Data.Peers[0].TransportID != 1234567890123456 {
		t.Fatalf("unexpected status: %+v", statusResp)
	}

	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/utc/pairs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("pairs got %d: %s", rec.Code, rec.Body.String())
	}
	var pairsResp struct {
		Success bool             `json:"success"`
		Data    UTCPairsResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pairsResp); err != nil {
		t.Fatalf("decode pairs: %v", err)
	}
	if pairsResp.Data.Count != 1 || pairsResp.Data.Peers[0].Name != "n182" {
		t.Fatalf("unexpected pairs: %+v", pairsResp)
	}

	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/utc/acceleration", bytes.NewBufferString(`{"enabled":true}`)))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST acceleration got %d, want 405", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/utc/acceleration", bytes.NewBufferString(`{"enabled":true,"pair_id":1}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT unknown field got %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/utc/acceleration", bytes.NewBufferString(`{"enabled":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT enabled=true got %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("decode put resp: %v", err)
	}
	if !statusResp.Data.Enabled || !statusResp.Data.Running {
		t.Fatalf("expected enabled+running, got %+v", statusResp.Data)
	}

	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/utc/acceleration", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET acceleration got %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"enabled":true`)) {
		t.Fatalf("expected enabled:true in %s", rec.Body.String())
	}

	srv.UTCSetEnabled = func(bool) (UTCLocalStatus, error) {
		return state, errors.New("UTC 未配置：未找到 utcd.conf")
	}
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/utc/acceleration", bytes.NewBufferString(`{"enabled":true}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("PUT toggle error got %d, want 409", rec.Code)
	}

	// POST /api/v1/utc/sync unwired -> 503
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/utc/sync", bytes.NewBufferString(`{"peers":[{"name":"peer_102","transport_id":102}]}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST sync unwired got %d, want 503", rec.Code)
	}

	// POST /api/v1/utc/sync wired -> 200
	srv.UTCSync = func(req UTCSyncRequest) (UTCLocalStatus, error) {
		state.Configured = true
		state.EdgeCandidates = req.EdgeCandidates
		state.Peers = []UTCPeerState{{Name: req.Peers[0].Name, TransportID: req.Peers[0].TransportID}}
		return state, nil
	}
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/utc/sync", bytes.NewBufferString(`{"edge_candidates":["edge-b:4433"],"peers":[{"name":"peer_102","transport_id":102}]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST sync got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"peer_102"`)) {
		t.Fatalf("expected peer_102 in %s", rec.Body.String())
	}

	// POST /api/v1/utc/sync empty peers -> 400
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/utc/sync", bytes.NewBufferString(`{"peers":[]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST sync empty peers got %d, want 400", rec.Code)
	}
}
