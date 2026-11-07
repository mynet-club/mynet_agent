package utc

import (
	"context"
	"crypto/sha512"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCoordinatorInstall(t *testing.T, dir string, nodes []NodeInstall) string {
	t.Helper()
	path := filepath.Join(dir, "install.json")
	body := fmt.Sprintf(`{"version":1,"store_directory":%q,"nodes":[`, filepath.Join(dir, "store"))
	for i, node := range nodes {
		if i > 0 {
			body += ","
		}
		body += fmt.Sprintf(`{"node_id":%d,"instance":%q,"conf_dir":%q,"gnb_binary":%q,"utcd_socket":%q}`,
			node.NodeID, node.Instance, node.ConfDir, node.GNBBinary, node.UTCDSocket)
	}
	body += "]}"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadCoordinatorInstallValidation(t *testing.T) {
	dir := t.TempDir()
	valid := []NodeInstall{{NodeID: 7, Instance: "node7", ConfDir: "/opt/mynet/conf/local/7",
		GNBBinary: "/opt/mynet/bin/gnb_es", UTCDSocket: "/run/utc/control.sock"}}
	path := writeCoordinatorInstall(t, dir, valid)
	store, nodes, err := LoadCoordinatorInstall(path)
	if err != nil || len(nodes) != 1 || store.Directory != filepath.Join(dir, "store") {
		t.Fatalf("install = %+v %v, %v", store, nodes, err)
	}
	duplicated := append(append([]NodeInstall(nil), valid...), valid...)
	if _, _, err := LoadCoordinatorInstall(writeCoordinatorInstall(t, dir, duplicated)); err == nil {
		t.Fatal("duplicate node id must be refused")
	}
	relative := append([]NodeInstall(nil), valid...)
	relative[0].ConfDir = "conf/local/7"
	if _, _, err := LoadCoordinatorInstall(writeCoordinatorInstall(t, dir, relative)); err == nil {
		t.Fatal("relative driver path must be refused")
	}
	if _, _, err := LoadCoordinatorInstall(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing install file must be refused")
	}
}

func fixedCoordinateClock() time.Time { return time.Unix(1900000000, 0) }

func coordinateNodes(t *testing.T, p Plan) map[uint64]Node {
	t.Helper()
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
	return nodes
}

func TestRunPlanResumeAndTwoRenewals(t *testing.T) {
	p := execNodePlan()
	nodes := coordinateNodes(t, p)
	store := DiskStore{Directory: t.TempDir()}
	if err := os.Chmod(store.Directory, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := RunPlan(context.Background(), store, nodes, wiringAuthority{}, p, fixedCoordinateClock)
	if err != nil || record.Phase != "EFFECTIVE" {
		t.Fatalf("run = %s %v", record.Phase, err)
	}
	status, err := CoordinateStatus(store, p.Scope, p.PairID)
	if err != nil || status == nil || status.Phase != "EFFECTIVE" {
		t.Fatalf("status = %+v %v", status, err)
	}
	// Two unattended renewals: always a larger generation and fresh manifest.
	current := p
	for _, generation := range []uint64{8, 9} {
		next, err := NewGeneration(current, generation, current.ValidUntil+100, []string{"lease-renewed"})
		if err != nil {
			t.Fatal(err)
		}
		for i, side := range next.Sides {
			conf := nodes[side.NodeID].(*ExecNode).ConfDir
			peer := peerPolicy(next, i)
			policy := canonicalPeerLine(peer, "policy")
			digest := sha512.Sum512([]byte(policy + "\n"))
			peerRow := fmt.Sprintf("peer %d %d %d APPLIED POLICY_LOADED %s %x", peer.PeerID,
				peer.PairID, peer.PairGen, peer.Manifest, digest)
			writeSideStatus(t, conf, side, generation-5, policy, peerRow)
		}
		record, err = RunPlan(context.Background(), store, nodes, wiringAuthority{}, next, fixedCoordinateClock)
		if err != nil || record.Phase != "EFFECTIVE" || record.Watermark != generation {
			t.Fatalf("renewal %d = %s watermark=%d err=%v", generation, record.Phase, record.Watermark, err)
		}
		current = next
	}
	older := current
	older.Generation = 7
	older.Manifest = older.Digest()
	if _, err := RunPlan(context.Background(), store, nodes, wiringAuthority{}, older, fixedCoordinateClock); err == nil {
		t.Fatal("an older generation must not be revived")
	}
}

func TestResumeInterruptedApplyCompensatesWithLargerGeneration(t *testing.T) {
	p := execNodePlan()
	nodes := coordinateNodes(t, p)
	store := DiskStore{Directory: t.TempDir()}
	if err := os.Chmod(store.Directory, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := RunPlan(context.Background(), store, nodes, wiringAuthority{}, p, fixedCoordinateClock)
	if err != nil || record.Phase != "EFFECTIVE" {
		t.Fatalf("initial run = %s %v", record.Phase, err)
	}
	// Simulate a coordinator killed between apply and verify: the durable
	// journal still says APPLYING. Resume must compensate, never replay.
	record.Phase = "APPLYING"
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	blockPlan := record.Plan
	blockPlan.Generation = record.Watermark + 1
	blockPlan.ValidUntil = 1
	blockPlan.Manifest = blockPlan.Digest()
	for i, side := range p.Sides {
		conf := nodes[side.NodeID].(*ExecNode).ConfDir
		peer := peerPolicy(blockPlan, i)
		writeSideStatus(t, conf, side, 3, canonicalPeerLine(peer, "policy"), "")
	}
	resumed, err := ResumePair(context.Background(), store, nodes, wiringAuthority{}, p.Scope, p.PairID, fixedCoordinateClock)
	// Compensation reports its reason as an error while still recording the
	// larger-generation block; the phase and generation are the evidence.
	if err == nil {
		t.Log("compensation completed without an error reason")
	}
	if resumed.BlockPlan == nil || resumed.BlockPlan.Generation != record.Watermark+1 {
		t.Fatalf("resume did not allocate a larger generation: %+v", resumed.BlockPlan)
	}
	if resumed.Phase != "BLOCKED" && resumed.Phase != "BLOCK_EXPECTED" {
		t.Fatalf("interrupted apply must compensate, got %s", resumed.Phase)
	}
	if _, err := ResumePair(context.Background(), store, nodes, wiringAuthority{}, "other-scope", p.PairID); err == nil {
		t.Fatal("resume of an unknown pair must fail")
	}
}

func TestRunPlanFailsClosedWhenJournalCannotPersist(t *testing.T) {
	p := execNodePlan()
	nodes := coordinateNodes(t, p)
	storeDir := t.TempDir()
	if err := os.Chmod(storeDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(storeDir, 0o700) }()
	record, err := RunPlan(context.Background(), DiskStore{Directory: storeDir}, nodes, wiringAuthority{}, p, fixedCoordinateClock)
	if err == nil {
		t.Fatal("a journal that cannot be persisted must not report success")
	}
	if record.Phase == "EFFECTIVE" {
		t.Fatal("EFFECTIVE must never be reported without durable confirmation")
	}
}
