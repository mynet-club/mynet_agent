package upgrade

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeCtl 起一个假 ctl，按**服务端真实形状**提供 apps.json 与 manifest。
//
// 刻意不用 spec/fixtures/apps/apps.json：那份 fixture 是手写的理想形状
// （products 是数组、带 releases[] 与 checksum），与服务端不符——
// 旧测试读它且在缺失时 t.Skip，所以形状分歧一直没被发现。
func fakeCtl(t *testing.T, apps appsIndex, manifests map[string]productManifest) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/d/apps.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apps)
	})
	for path, m := range manifests {
		m := m
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(m)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func sampleApps() appsIndex {
	return appsIndex{
		SchemaVersion: "2.1",
		Products: map[string]appsProduct{
			"mynet_agent": {Name: "MyNet Agent", Latest: "v0.2.0", ManifestURL: "/d/mynet_agent/manifest.json"},
			"gnb":         {Name: "GNB", Latest: "v1.6.8", ManifestURL: "/d/gnb/manifest.json"},
			"mynet":       {Name: "MyNet TUI", Latest: "v0.9.0", ManifestURL: "/d/mynet/manifest.json"},
			"lua":         {Name: "MyNet Lua", Latest: "", ManifestURL: "/d/lua/manifest.json"},
		},
	}
}

func sampleManifest(project string, versions map[string]manifestVersion) productManifest {
	return productManifest{
		SchemaVersion: "1.1",
		Project:       project,
		LatestStable:  "v0.2.0",
		Versions:      versions,
	}
}

func TestFetchUsesTwoStepProtocol(t *testing.T) {
	srv := fakeCtl(t, sampleApps(), map[string]productManifest{
		"/d/mynet_agent/manifest.json": sampleManifest("mynet_agent", map[string]manifestVersion{
			"v0.1.0": {Channel: "stable", Artifacts: []manifestArtifact{
				{OS: "linux", Arch: "amd64", File: "mynet_agent_linux_amd64_v0.1.0.tgz",
					URL: "http://ctl.example/d/mynet_agent/mynet_agent_linux_amd64_v0.1.0.tgz",
					Ext: "tgz", SHA256: "aa11"},
			}},
			"v0.2.0": {Channel: "stable", Artifacts: []manifestArtifact{
				{OS: "linux", Arch: "amd64", File: "f", URL: "http://x/f.tgz", Ext: "tgz", SHA256: "bb22"},
				{OS: "darwin", Arch: "arm64", File: "g", URL: "http://x/g.tgz", Ext: "tgz", SHA256: "cc33"},
			}},
		}),
		"/d/gnb/manifest.json": sampleManifest("gnb", map[string]manifestVersion{
			"v1.6.8": {Channel: "stable", Artifacts: []manifestArtifact{
				{OS: "linux", Arch: "amd64", File: "gnb", URL: "http://x/gnb.tgz", Ext: "tgz", SHA256: "dd44"},
			}},
		}),
	})

	svc := &Service{IndexURL: srv.URL}
	svc.Fetch = func(url string) ([]Artifact, error) {
		c, err := newIndexClient(url, srv.Client())
		if err != nil {
			return nil, err
		}
		return c.fetchAll()
	}

	all, err := svc.Fetch(srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// mynet_agent 3 条 + gnb 1 条；tui(mynet) 与 lua 有意不映射
	if len(all) != 4 {
		t.Fatalf("应展平出 4 个 artifact，实际 %d: %+v", len(all), all)
	}

	byComp := map[Component]int{}
	for _, a := range all {
		byComp[a.Component]++
		if a.URL == "" {
			t.Errorf("artifact URL 为空: %+v", a)
		}
		if a.Checksum == "" {
			t.Errorf("artifact 缺 checksum: %+v", a)
		}
	}
	if byComp[ComponentMynet] != 3 {
		t.Errorf("mynet 组件应有 3 个 artifact，实际 %d", byComp[ComponentMynet])
	}
	if byComp[ComponentGNB] != 1 {
		t.Errorf("gnb 组件应有 1 个 artifact，实际 %d", byComp[ComponentGNB])
	}
}

// ⚠️ 关键决定：`mynet` 是**旧 TUI** 的外部别名，不得映射到 ComponentMynet，
// 否则 `mynet update` 会去装那个已 deprecated 的 TUI。
func TestFetchDoesNotMapLegacyTuiAliasToAgent(t *testing.T) {
	if comp, ok := productToComponent["mynet"]; ok {
		t.Fatalf("`mynet` 不应映射到任何组件（会装成废弃的 TUI），实际映射到 %q", comp)
	}
	if _, ok := ignoredProducts["mynet"]; !ok {
		t.Error("`mynet` 应出现在 ignoredProducts 里，便于排障时分辨「没映射」与「没发布」")
	}
	if productToComponent["mynet_agent"] != ComponentMynet {
		t.Error("`mynet_agent` 必须映射到 ComponentMynet")
	}
}

// 版本展平后 LatestOf 必须挑出语义最新，而不是字典序最新。
func TestFetchThenLatestOfPicksSemanticLatest(t *testing.T) {
	all := []Artifact{
		{Component: ComponentMynet, Version: "v0.1.0", URL: "u1", Checksum: "c1"},
		{Component: ComponentMynet, Version: "v0.2.0", URL: "u2", Checksum: "c2"},
		{Component: ComponentMynet, Version: "v0.10.0", URL: "u3", Checksum: "c3"}, // 字典序会漏掉它
	}
	got := LatestOf(all, ComponentMynet)
	if got == nil || got.Version != "v0.10.0" {
		t.Errorf("LatestOf = %+v, want v0.10.0", got)
	}
}

// 未发布的产品（latest=null、无 manifest）不应让 Fetch 失败。
func TestFetchSkipsProductsWithoutManifest(t *testing.T) {
	apps := appsIndex{
		SchemaVersion: "2.1",
		Products: map[string]appsProduct{
			"mynet_agent": {Name: "a", ManifestURL: ""}, // 未发布
		},
	}
	srv := fakeCtl(t, apps, nil)

	c, err := newIndexClient(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	all, err := c.fetchAll()
	if err != nil {
		t.Fatalf("无已发布产品时不应报错: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("应为空，实际 %+v", all)
	}
}

// apps.json 不是合法 JSON 时必须报错并带上原文片段。
func TestFetchSurfacesMalformedAppsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>502</html>`))
	}))
	defer srv.Close()

	c, err := newIndexClient(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.fetchAll(); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

func TestNewIndexClientValidatesURL(t *testing.T) {
	for _, bad := range []string{"", "   ", "ctl.mynet.club", "ftp://x"} {
		if _, err := newIndexClient(bad, nil); err == nil {
			t.Errorf("newIndexClient(%q) 应报错", bad)
		}
	}
	for _, ok := range []string{"https://ctl.mynet.club", "http://127.0.0.1:8080", "https://ctl.mynet.club/"} {
		if _, err := newIndexClient(ok, nil); err != nil {
			t.Errorf("newIndexClient(%q) 不应报错: %v", ok, err)
		}
	}
}

func TestNormalizeChecksum(t *testing.T) {
	cases := map[string]string{
		"abc123":         "abc123",
		"ABC123":         "abc123",
		"sha256:abc123":  "abc123",
		"  sha256:ABC  ": "abc",
		"":               "",
	}
	for in, want := range cases {
		if got := normalizeChecksum(in); got != want {
			t.Errorf("normalizeChecksum(%q) = %q, want %q", in, got, want)
		}
	}
}
