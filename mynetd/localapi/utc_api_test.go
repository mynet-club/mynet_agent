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
		Enabled: false,
		Endpoint: UTCEndpointState{
			Configured: true,
			Running:    false,
			ConfigPath: "/etc/mynet/conf/utc/utcd.conf",
		},
		Hub: UTCHubState{
			Configured:  true,
			Running:     false,
			Scope:       "default",
			TransportID: "101",
			AccountID:   "acct-1",
			Pairs: []UTCPairState{
				{
					PairID:         1234567890123456,
					PeerID:         102,
					Side:           0,
					Mode:           "A-D",
					Applied:        true,
					PairGen:        3,
					LeaseEpoch:     7,
					PlacementEpoch: 2,
					EdgeID:         1001,
					ValidUntil:     1900000000,
					ObservedAt:     1899999000,
				},
			},
		},
	}

	srv.UTCStatus = func() UTCLocalStatus { return state }
	srv.UTCSetEnabled = func(enabled bool) (UTCLocalStatus, error) {
		state.Enabled = enabled
		state.Endpoint.Running = enabled
		state.Hub.Running = enabled
		return state, nil
	}

	// GET /api/v1/utc/status
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
	if !statusResp.Success || statusResp.Data.Enabled || len(statusResp.Data.Hub.Pairs) != 1 ||
		statusResp.Data.Hub.Pairs[0].PairID != 1234567890123456 {
		t.Fatalf("unexpected status: %+v", statusResp)
	}

	// GET /api/v1/utc/pairs
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/utc/pairs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("pairs got %d: %s", rec.Code, rec.Body.String())
	}
	var pairsResp struct {
		Success bool `json:"success"`
		Data    struct {
			Enabled bool           `json:"enabled"`
			Count   int            `json:"count"`
			Pairs   []UTCPairState `json:"pairs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pairsResp); err != nil {
		t.Fatalf("decode pairs: %v", err)
	}
	if pairsResp.Data.Count != 1 || !pairsResp.Data.Pairs[0].Applied {
		t.Fatalf("unexpected pairs: %+v", pairsResp)
	}

	// POST /api/v1/utc/acceleration -> 405
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/utc/acceleration", bytes.NewBufferString(`{"enabled":true}`)))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST acceleration got %d, want 405", rec.Code)
	}

	// PUT unknown field -> 400
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/utc/acceleration", bytes.NewBufferString(`{"enabled":true,"pair_id":1}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT unknown field got %d, want 400", rec.Code)
	}

	// PUT enabled=true -> 200
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/utc/acceleration", bytes.NewBufferString(`{"enabled":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT enabled=true got %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("decode put resp: %v", err)
	}
	if !statusResp.Data.Enabled || !statusResp.Data.Endpoint.Running || !statusResp.Data.Hub.Running {
		t.Fatalf("expected enabled+running, got %+v", statusResp.Data)
	}

	// GET /api/v1/utc/acceleration reflects updated state
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/utc/acceleration", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET acceleration got %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"enabled":true`)) {
		t.Fatalf("expected enabled:true in %s", rec.Body.String())
	}

	// Toggle error returns 409
	srv.UTCSetEnabled = func(bool) (UTCLocalStatus, error) {
		return state, errors.New("not configured")
	}
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/utc/acceleration", bytes.NewBufferString(`{"enabled":true}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("PUT toggle error got %d, want 409", rec.Code)
	}
}
