package endpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfigExtractsV3Summary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "utcd.conf")
	stateDir := filepath.Join(dir, "state")
	content := `# UTC single config (v3)
config_version = 3
state_dir = ` + stateDir + `
edge_candidates = edge-a:4433, edge-b:4433
peer.n182.transport_id = 1234567890123456
peer.n182.udp_bind = 127.0.0.1:19001
peer.n182.udp_target = 127.0.0.1:9001
peer.n178.transport_id = 2345678901234567
peer.n178.udp_bind = 127.0.0.1:19002
peer.n178.udp_target = 127.0.0.1:9002
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig failed: %v", err)
	}
	if summary.ConfigVersion != 3 {
		t.Fatalf("want config_version=3, got %d", summary.ConfigVersion)
	}
	if summary.StateDir != stateDir {
		t.Fatalf("unexpected state_dir: %q", summary.StateDir)
	}
	if len(summary.EdgeCandidates) != 2 || summary.EdgeCandidates[0] != "edge-a:4433" {
		t.Fatalf("unexpected edge_candidates: %+v", summary.EdgeCandidates)
	}
	if len(summary.Peers) != 2 {
		t.Fatalf("want 2 peers, got %d", len(summary.Peers))
	}
	if summary.Peers[0].Name != "n178" || summary.Peers[0].TransportID != 2345678901234567 {
		t.Fatalf("peers should be sorted by name: %+v", summary.Peers)
	}
	if summary.Peers[1].Name != "n182" || summary.Peers[1].TransportID != 1234567890123456 ||
		summary.Peers[1].UDPBind != "127.0.0.1:19001" || summary.Peers[1].UDPTarget != "127.0.0.1:9001" {
		t.Fatalf("unexpected peer[1]: %+v", summary.Peers[1])
	}
}

func TestLoadRefsRenderAndWriteV3Config(t *testing.T) {
	dir := t.TempDir()
	refsPath := filepath.Join(dir, "utcd.refs")
	stateDir := "/var/lib/mynet/utc"
	refsContent := "identity_ref=" + filepath.Join(dir, "id.ref") + "\n" +
		"trust_ref=" + filepath.Join(dir, "tr.ref") + "\n" +
		"material_ref=" + filepath.Join(dir, "mat.ref") + "\n" +
		"state_dir=" + stateDir + "\n" +
		"edge_candidates=edge-a:4433\n"
	if err := os.WriteFile(refsPath, []byte(refsContent), 0o600); err != nil {
		t.Fatal(err)
	}
	refs, err := LoadRefs(refsPath)
	if err != nil {
		t.Fatalf("LoadRefs failed: %v", err)
	}
	rendered, err := RenderV3Config(refs, []string{"edge-b:4433", "edge-c:4433"}, []PeerSummary{
		{Name: "peer_102", TransportID: 102},
		{Name: "peer_101", TransportID: 101},
	})
	if err != nil {
		t.Fatalf("RenderV3Config failed: %v", err)
	}
	outPath := filepath.Join(dir, "utcd.conf")
	if err := WriteConfigAtomic(outPath, rendered); err != nil {
		t.Fatalf("WriteConfigAtomic failed: %v", err)
	}
	parsed, err := ParseConfig(outPath)
	if err != nil {
		t.Fatalf("ParseConfig failed: %v", err)
	}
	if parsed.ConfigVersion != 3 || len(parsed.EdgeCandidates) != 2 || parsed.EdgeCandidates[0] != "edge-b:4433" {
		t.Fatalf("unexpected parsed summary: %+v", parsed)
	}
	if len(parsed.Peers) != 2 || parsed.Peers[0].Name != "peer_101" || parsed.Peers[0].UDPBind != "127.0.0.1:19100" {
		t.Fatalf("unexpected parsed peers: %+v", parsed.Peers)
	}
	if err := WriteConfigAtomic(filepath.Join(dir, "utc.conf"), rendered); err == nil {
		t.Fatal("expected utc.conf basename collision to be rejected")
	}
}
