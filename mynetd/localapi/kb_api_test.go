package localapi

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/tipskb"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

func kbTestManifest() *tipskb.Manifest {
	return &tipskb.Manifest{
		Version:     "v20261026.1",
		GeneratedAt: "2026-10-26T00:00:00Z",
		Lang:        "zh-CN",
		Chunks: []tipskb.Chunk{{
			ID:     "tc-overview",
			Title:  "适用症状与五步总览",
			Tags:   []string{"connectivity", "troubleshoot"},
			Body:   "正文",
			Source: tipskb.Source{File: "docs/knowledge-base/troubleshoot-connectivity.md", Section: "0.1"},
		}},
	}
}

// 形状：命中缓存时端点回合法 tips-kb.json 形状 + application/json；非 GET 405。
func TestTipsKBHandlerServesCachedManifest(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	dir := utils.LocalConfDir()
	want := kbTestManifest()
	if err := tipskb.Save(dir, want); err != nil {
		t.Fatal(err)
	}

	s := NewServer(Config{})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/kb/tips-kb.json", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type=%q", ct)
	}
	var got tipskb.Manifest
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != "v20261026.1" || got.Lang != "zh-CN" || len(got.Chunks) != 1 || got.Chunks[0].ID != "tc-overview" {
		t.Fatalf("有效集不对: %+v", got)
	}

	r = httptest.NewRequest(http.MethodPost, "/api/v1/kb/tips-kb.json", nil)
	w = httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("只读端点应 405，实为 %d", w.Code)
	}
}

// ⑥ 无任何缓存 + 拉取失败：回空 chunks 的合法 manifest（preferred），不回错。
func TestTipsKBHandlerEmptyManifestWithoutCache(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	ctl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	defer ctl.Close()
	t.Setenv("MYNET_APPS_INDEX", ctl.URL)

	s := NewServer(Config{})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/kb/tips-kb.json", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type=%q", ct)
	}
	var got tipskb.Manifest
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version == "" || got.GeneratedAt == "" || got.Lang != "zh-CN" {
		t.Fatalf("空载荷形状不合法: %+v", got)
	}
	if got.Chunks == nil || len(got.Chunks) != 0 {
		t.Fatalf("chunks 应为空数组: %+v", got.Chunks)
	}
}

// 缓存缺失按需拉：端点顺带拉一次 ctl 并落盘，响应即为拉回的有效集。
// 无 sidecar，校验和走响应头 X-KB-Checksum（§3.4 ① 的另一来源）。
func TestTipsKBHandlerPullsOnCacheMiss(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	payload, err := json.Marshal(kbTestManifest())
	if err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(payload))
	ctl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/d/kb/tips-kb.json":
			w.Header().Set("X-KB-Checksum", sum)
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ctl.Close()
	t.Setenv("MYNET_APPS_INDEX", ctl.URL)

	s := NewServer(Config{})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/kb/tips-kb.json", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got tipskb.Manifest
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Chunks) != 1 || got.Chunks[0].ID != "tc-overview" {
		t.Fatalf("按需拉取后应返回载荷: %+v", got)
	}
	if cached, err := tipskb.Load(utils.LocalConfDir()); err != nil || cached == nil {
		t.Fatalf("缓存未落盘: %v", err)
	}
}
