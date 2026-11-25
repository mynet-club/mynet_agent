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
		{http.MethodPut, "/api/v1/utc/pairs/123", `{"intent":"on"}`},
		{http.MethodPut, "/api/v1/utc/pairs/123/result", `{"result":"pending"}`},
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

func TestUTCPairIntentPutAndGet(t *testing.T) {
	srv := NewServer(Config{})
	state := UTCLocalStatus{Enabled: true, Configured: true, Running: true, Peers: []UTCPeerState{}}
	pairs := []UTCPairState{{PeerID: 2000, Intent: "", State: "fallback"}}
	var gotPeer uint64
	var gotOn bool
	var calls int

	srv.UTCStatus = func() UTCLocalStatus { return state }
	srv.UTCPairs = func() ([]UTCPairState, error) { return pairs, nil }
	srv.UTCPairIntent = func(peerID uint64, on bool) (UTCLocalStatus, error) {
		calls++
		gotPeer, gotOn = peerID, on
		pairs = []UTCPairState{{PeerID: peerID, Intent: "on", IntentState: "pending", State: "pending"}}
		return state, nil
	}

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/utc/pairs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET pairs got %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Success bool             `json:"success"`
		Data    UTCPairsResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Data.Pairs) != 1 || got.Data.Pairs[0].State != "fallback" {
		t.Fatalf("unexpected pairs payload: %+v", got.Data)
	}

	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/utc/pairs/2000", bytes.NewBufferString(`{"intent":"on"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT intent got %d: %s", rec.Code, rec.Body.String())
	}
	if calls != 1 || gotPeer != 2000 || !gotOn {
		t.Fatalf("intent not forwarded: calls=%d peer=%d on=%v", calls, gotPeer, gotOn)
	}
	var put struct {
		Success bool `json:"success"`
		Data    struct {
			Status UTCLocalStatus `json:"status"`
			Pairs  []UTCPairState `json:"pairs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &put); err != nil {
		t.Fatalf("decode put: %v", err)
	}
	if !put.Success || len(put.Data.Pairs) != 1 || put.Data.Pairs[0].Intent != "on" || put.Data.Pairs[0].State != "pending" {
		t.Fatalf("unexpected put payload: %s", rec.Body.String())
	}

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"bad peer", http.MethodPut, "/api/v1/utc/pairs/abc", `{"intent":"on"}`, http.StatusBadRequest},
		{"zero peer", http.MethodPut, "/api/v1/utc/pairs/0", `{"intent":"on"}`, http.StatusBadRequest},
		{"bad intent", http.MethodPut, "/api/v1/utc/pairs/2000", `{"intent":"maybe"}`, http.StatusBadRequest},
		{"unknown field", http.MethodPut, "/api/v1/utc/pairs/2000", `{"enabled":true}`, http.StatusBadRequest},
		{"wrong method", http.MethodPost, "/api/v1/utc/pairs/2000", `{}`, http.StatusMethodNotAllowed},
	} {
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body)))
		if rec.Code != tc.want {
			t.Fatalf("%s: got %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestUTCPairResultPut(t *testing.T) {
	srv := NewServer(Config{})
	state := UTCLocalStatus{Enabled: true, Configured: true, Running: true, Peers: []UTCPeerState{}}
	pairs := []UTCPairState{{PeerID: 2000, Intent: "on", IntentState: "pending", State: "pending"}}
	var gotID uint64
	var gotResult, gotReason string

	srv.UTCStatus = func() UTCLocalStatus { return state }
	srv.UTCPairs = func() ([]UTCPairState, error) { return pairs, nil }
	srv.UTCPairResult = func(peerID uint64, result, reason string) error {
		if peerID == 999 {
			return errors.New("no intent for peer")
		}
		gotID, gotResult, gotReason = peerID, result, reason
		pairs = []UTCPairState{{PeerID: peerID, Intent: "on", IntentState: "rejected", State: "rejected", Reason: reason}}
		return nil
	}

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/utc/pairs/2000/result",
		bytes.NewBufferString(`{"result":"rejected","reason":"需要管理员权限"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT result got %d: %s", rec.Code, rec.Body.String())
	}
	if gotID != 2000 || gotResult != "rejected" || gotReason != "需要管理员权限" {
		t.Fatalf("result not forwarded: id=%d result=%q reason=%q", gotID, gotResult, gotReason)
	}
	var got struct {
		Success bool `json:"success"`
		Data    struct {
			Pairs []UTCPairState `json:"pairs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Success || len(got.Data.Pairs) != 1 || got.Data.Pairs[0].State != "rejected" {
		t.Fatalf("unexpected payload: %s", rec.Body.String())
	}

	for _, tc := range []struct {
		name string
		path string
		body string
		want int
	}{
		{"bad result", "/api/v1/utc/pairs/2000/result", `{"result":"active"}`, http.StatusBadRequest},
		{"bad peer", "/api/v1/utc/pairs/abc/result", `{"result":"pending"}`, http.StatusBadRequest},
		{"unknown peer", "/api/v1/utc/pairs/999/result", `{"result":"pending"}`, http.StatusConflict},
		{"unknown field", "/api/v1/utc/pairs/2000/result", `{"state":"rejected"}`, http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, tc.path, bytes.NewBufferString(tc.body)))
		if rec.Code != tc.want {
			t.Fatalf("%s: got %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}
}
