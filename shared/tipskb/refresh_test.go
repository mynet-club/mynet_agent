package tipskb

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testChunk(id, title string) Chunk {
	return Chunk{
		ID:     id,
		Title:  title,
		Tags:   []string{"connectivity"},
		Body:   "正文 " + id,
		Source: Source{File: "docs/knowledge-base/troubleshoot-connectivity.md", Section: "1. 步骤"},
	}
}

func testManifest(version string, chunks ...Chunk) *Manifest {
	if chunks == nil {
		chunks = []Chunk{}
	}
	return &Manifest{
		Version:     version,
		GeneratedAt: "2026-10-26T00:00:00Z",
		Lang:        "zh-CN",
		Chunks:      chunks,
	}
}

func mustJSON(t *testing.T, m *Manifest) []byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sumHex(raw []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// newCtl 替身 ctl：/d/kb/tips-kb.json + tips-kb.json.sha256。sidecar 为空 → 404；
// headerSum 为空 → 不带 X-KB-Checksum 头。
func newCtl(payload []byte, sidecar, headerSum string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/d/kb/tips-kb.json":
			if headerSum != "" {
				w.Header().Set("X-KB-Checksum", headerSum)
			}
			_, _ = w.Write(payload)
		case "/d/kb/tips-kb.json.sha256":
			if sidecar == "" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(sidecar))
		default:
			http.NotFound(w, r)
		}
	}))
}

func refreshOK(t *testing.T, dir string, m *Manifest) {
	t.Helper()
	payload := mustJSON(t, m)
	ctl := newCtl(payload, sumHex(payload)+"  tips-kb.json\n", "")
	defer ctl.Close()
	if err := Refresh(context.Background(), ctl.Client(), ctl.URL+"/d/kb/", dir); err != nil {
		t.Fatal(err)
	}
}

// ① sidecar 校验：不匹配丢弃本次载荷，且不落盘。
func TestRefreshChecksumMismatchDiscardsPayload(t *testing.T) {
	dir := t.TempDir()
	refreshOK(t, dir, testManifest("v0", testChunk("tc-keep", "保留")))

	payload := mustJSON(t, testManifest("v1", testChunk("tc-new", "新")))
	ctl := newCtl(payload, strings.Repeat("0", 64)+"  tips-kb.json\n", "")
	defer ctl.Close()
	err := Refresh(context.Background(), ctl.Client(), ctl.URL+"/d/kb/", dir)
	if err == nil {
		t.Fatal("校验和不匹配应报错丢弃")
	}
	got, lerr := Load(dir)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if got == nil || got.Version != "v0" || len(got.Chunks) != 1 || got.Chunks[0].ID != "tc-keep" {
		t.Fatalf("校验失败后缓存被改动: %+v", got)
	}
}

// ① sidecar 缺失时接受 X-KB-Checksum 头；两者皆缺则丢弃。
func TestRefreshChecksumFallbacks(t *testing.T) {
	dir := t.TempDir()
	payload := mustJSON(t, testManifest("v1", testChunk("tc-a", "A")))

	ctl := newCtl(payload, "", sumHex(payload))
	defer ctl.Close()
	if err := Refresh(context.Background(), ctl.Client(), ctl.URL+"/d/kb/", dir); err != nil {
		t.Fatalf("X-KB-Checksum 头应可用: %v", err)
	}
	got, _ := Load(dir)
	if got == nil || got.Version != "v1" {
		t.Fatalf("头校验通过后应落盘: %+v", got)
	}

	dir2 := t.TempDir()
	ctl2 := newCtl(payload, "", "")
	defer ctl2.Close()
	if err := Refresh(context.Background(), ctl2.Client(), ctl2.URL+"/d/kb/", dir2); err == nil {
		t.Fatal("无任何校验和应丢弃")
	}
	if got, _ := Load(dir2); got != nil {
		t.Fatalf("无校验和不应落盘: %+v", got)
	}
}

// ② id-keyed reconcile：按 id upsert，缓存中 id 不在 manifest.chunks 的条目删除。
func TestRefreshReconcileUpsertsAndDeletesByID(t *testing.T) {
	dir := t.TempDir()
	refreshOK(t, dir, testManifest("v1", testChunk("tc-a", "A旧"), testChunk("tc-b", "B")))

	refreshOK(t, dir, testManifest("v2", testChunk("tc-b", "B新"), testChunk("tc-c", "C")))
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	var titles []string
	for _, c := range got.Chunks {
		ids = append(ids, c.ID)
		titles = append(titles, c.Title)
	}
	if !reflect.DeepEqual(ids, []string{"tc-b", "tc-c"}) {
		t.Fatalf("id 收敛不对（tc-a 应删、tc-c 应增）: %v", ids)
	}
	if titles[0] != "B新" {
		t.Fatalf("同 id 未以载荷为准 upsert: %q", titles[0])
	}
}

// ③ chunks 为空数组 = 权威撤回全部（清空）。
func TestRefreshEmptyChunksRevokesAll(t *testing.T) {
	dir := t.TempDir()
	refreshOK(t, dir, testManifest("v1", testChunk("tc-a", "A"), testChunk("tc-b", "B")))

	refreshOK(t, dir, testManifest("v2"))
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Chunks) != 0 {
		t.Fatalf("空数组未撤回全部: %+v", got)
	}
}

// ④ 幂等：同一 manifest 重复合并不改变有效集。
func TestRefreshIdempotent(t *testing.T) {
	dir := t.TempDir()
	m := testManifest("v1", testChunk("tc-a", "A"), testChunk("tc-b", "B"))
	refreshOK(t, dir, m)
	first, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	refreshOK(t, dir, m)
	second, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("重复合并改变了有效集:\n%+v\n%+v", first, second)
	}
}

// ⑤ 拉取失败/超时/校验失败：保留此前成功缓存，永不清空。
func TestRefreshFailuresKeepPreviousCache(t *testing.T) {
	dir := t.TempDir()
	refreshOK(t, dir, testManifest("v1", testChunk("tc-a", "A")))
	want, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("HTTP 失败", func(t *testing.T) {
		ctl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "offline", http.StatusServiceUnavailable)
		}))
		defer ctl.Close()
		if err := Refresh(context.Background(), ctl.Client(), ctl.URL+"/d/kb/", dir); err == nil {
			t.Fatal("HTTP 503 应报错")
		}
	})

	t.Run("超时/取消", func(t *testing.T) {
		ctl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer ctl.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := Refresh(ctx, ctl.Client(), ctl.URL+"/d/kb/", dir); err == nil {
			t.Fatal("超时应报错")
		}
	})

	t.Run("校验失败", func(t *testing.T) {
		payload := mustJSON(t, testManifest("v2", testChunk("tc-new", "新")))
		ctl := newCtl(payload, sumHex(mustJSON(t, testManifest("v-other")))+"  tips-kb.json\n", "")
		defer ctl.Close()
		if err := Refresh(context.Background(), ctl.Client(), ctl.URL+"/d/kb/", dir); err == nil {
			t.Fatal("校验和不匹配应报错")
		}
	})

	got, lerr := Load(dir)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("失败路径改动了缓存:\n%+v\n%+v", want, got)
	}
}
