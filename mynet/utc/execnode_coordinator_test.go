package utc

import (
	"context"
	"crypto/sha512"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/driver"
)

// The authority adapter is deliberately out of scope for this wiring test: real
// signature/revocation validation is covered by static_authority_test.go. This
// test proves the coordinator drives the real ExecNode methods end to end.
type wiringAuthority struct{}

func (wiringAuthority) Validate(context.Context, Plan) error { return nil }

func execNodeSideFixture(t *testing.T, root string, side Side) (*ExecNode, string) {
	t.Helper()
	conf := filepath.Join(root, "conf", "local", fmt.Sprint(side.NodeID))
	if err := os.MkdirAll(conf, 0o700); err != nil {
		t.Fatal(err)
	}
	node := &ExecNode{
		NodeID: side.NodeID, Instance: side.Instance, ConfDir: conf, GNBBinary: "/opt/mynet/bin/gnb_es",
		Now:        func() time.Time { return time.Unix(1900000000, 0) },
		DriverExec: func(string, ...string) ([]byte, error) { return []byte("GNB\nFeatures: utc-conf-v1\n"), nil },
		ProcessCommand: func(pid uint64) (string, error) {
			return "/opt/mynet/bin/gnb_es -c " + conf, nil
		},
	}
	if err := node.driver().ApplyUTCProjection(driver.UTCProjection{Scope: "lab",
		Instance: side.Instance, LocalID: side.NodeID, ConfigGen: 1}); err != nil {
		t.Fatal(err)
	}
	return node, conf
}

func writeSideStatus(t *testing.T, conf string, side Side, configGen uint64, policy, peerRow string) {
	t.Helper()
	boot := strings.Repeat("a", 32)
	body := fmt.Sprintf("status-v1 lab %s %d %s %d 1900000000 OK 4242\nmetrics 1 0 0 0\n",
		side.Instance, side.NodeID, boot, configGen)
	if policy != "" {
		body += policy + "\n"
	}
	if peerRow != "" {
		body += peerRow + "\n"
	}
	if err := os.WriteFile(filepath.Join(conf, "utc.status"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorRunWithRealExecNodes(t *testing.T) {
	p := execNodePlan()
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	nodes := map[uint64]Node{}
	for i, side := range p.Sides {
		node, conf := execNodeSideFixture(t, root, side)
		counter := uint64(0)
		node.UTCDSocket = controlServer(t, func() map[string]string {
			counter++
			return map[string]string{"status": "running", "security": "static-mtls", "role": "client",
				"udp_to_tcp": fmt.Sprint(counter), "tcp_to_udp": fmt.Sprint(counter),
				"session0_epoch": "5", "session0_from_udp": fmt.Sprint(counter),
				"session0_to_udp": fmt.Sprint(counter), "session0_from_tcp": "0",
				"session0_to_tcp": "0"}
		})
		nodes[side.NodeID] = node
		peer := peerPolicy(p, i)
		policy := canonicalPeerLine(peer, "policy")
		digest := sha512.Sum512([]byte(policy + "\n"))
		peerRow := fmt.Sprintf("peer %d %d %d APPLIED POLICY_LOADED %s %x", peer.PeerID,
			peer.PairID, peer.PairGen, peer.Manifest, digest)
		writeSideStatus(t, conf, side, 2, policy, peerRow)
	}
	storeDir := t.TempDir()
	if err := os.Chmod(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	coordinator := Coordinator{
		Store: DiskStore{Directory: storeDir}, Authority: wiringAuthority{}, Nodes: nodes,
		Now: func() time.Time { return time.Unix(1900000000, 0) },
	}
	record, err := coordinator.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("coordinator run: %v (phase=%s reason=%s)", err, record.Phase, record.Reason)
	}
	if record.Phase != "EFFECTIVE" {
		t.Fatalf("phase = %s, reason = %s", record.Phase, record.Reason)
	}
	for i, side := range p.Sides {
		if record.Prepared[i].State != "PREPARED" || record.Applied[i].State != "APPLIED" ||
			record.Verified[i].State != "VERIFIED" {
			t.Fatalf("side %d evidence incomplete: %+v", side.NodeID, record)
		}
	}
	persisted, err := (DiskStore{Directory: storeDir}).Load(p.Scope, p.PairID)
	if err != nil || persisted == nil || persisted.Phase != "EFFECTIVE" {
		t.Fatalf("durable EFFECTIVE record = %+v, %v", persisted, err)
	}
}
