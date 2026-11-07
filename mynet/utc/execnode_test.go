package utc

import (
	"context"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/driver"
)

func controlServer(t *testing.T, fields func() map[string]string) string {
	t.Helper()
	// Unix socket paths are short; the Go tempdir hierarchy exceeds the limit.
	short, err := os.MkdirTemp("/tmp", "uctl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	socket := filepath.Join(short, "c.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			header := make([]byte, 16)
			if _, err := io.ReadFull(conn, header); err != nil {
				_ = conn.Close()
				continue
			}
			var payload strings.Builder
			for key, value := range fields() {
				fmt.Fprintf(&payload, "%s=%s\n", key, value)
			}
			body := payload.String()
			binary.BigEndian.PutUint32(header[0:4], utcdControlMagic)
			binary.BigEndian.PutUint16(header[4:6], utcdControlVersion)
			binary.BigEndian.PutUint16(header[6:8], utcdControlReply)
			binary.BigEndian.PutUint32(header[8:12], 1)
			binary.BigEndian.PutUint32(header[12:16], uint32(len(body)))
			_, _ = conn.Write(append(header, []byte(body)...))
			_ = conn.Close()
		}
	}()
	return socket
}

func execNodePlan() Plan {
	p := Plan{
		OperationID: "op-1", Scope: "lab", PairID: 9, Generation: 7, ValidUntil: 1900000100,
		LeaseRefs: []string{"lease-A"},
		Sides: [2]Side{
			{NodeID: 7, Instance: "node7", Export: "127.0.0.1:14001"},
			{NodeID: 8, Instance: "node8", Export: "127.0.0.1:14002"},
		},
	}
	p.Manifest = p.Digest()
	return p
}

func execNodeFixture(t *testing.T) (*ExecNode, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	conf := filepath.Join(root, "conf", "local", "7")
	if err := os.MkdirAll(conf, 0o700); err != nil {
		t.Fatal(err)
	}
	node := &ExecNode{
		NodeID: 7, Instance: "node7", ConfDir: conf, GNBBinary: "/opt/mynet/bin/gnb_es",
		Now:        func() time.Time { return time.Unix(1900000000, 0) },
		DriverExec: func(string, ...string) ([]byte, error) { return []byte("GNB\nFeatures: utc-conf-v1\n"), nil },
		ProcessCommand: func(pid uint64) (string, error) {
			return "/opt/mynet/bin/gnb_es -c " + conf, nil
		},
	}
	d := node.driver()
	// The first full projection publish also marks the instance managed. Calling
	// MarkUTCManaged first would make the missing ledger recovery-required.
	if err := d.ApplyUTCProjection(driver.UTCProjection{Scope: "lab", Instance: "node7",
		LocalID: 7, ConfigGen: 1}); err != nil {
		t.Fatal(err)
	}
	return node, conf
}

func canonicalPeerLine(peer driver.UTCPeerPolicy, prefix string) string {
	endpoint, _ := netip.ParseAddrPort(peer.Export)
	family := 4
	var address [16]byte
	if endpoint.Addr().Is4() {
		v4 := endpoint.Addr().As4()
		copy(address[:4], v4[:])
	} else {
		family = 6
		address = endpoint.Addr().As16()
	}
	return fmt.Sprintf("%s %d %d %d %d %d %d %x %d %d %s", prefix, peer.PeerID, peer.PairID,
		peer.PairGen, peer.Mode, peer.ExpiresAt, family, address, endpoint.Port(), peer.ScopeID, peer.Manifest)
}

func writeExecStatus(t *testing.T, conf string, configGen uint64, policy, peerRow string) {
	t.Helper()
	boot := strings.Repeat("a", 32)
	body := fmt.Sprintf("status-v1 lab node7 7 %s %d 1900000000 OK 4242\nmetrics 1 0 0 0\n", boot, configGen)
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

func TestExecNodePrepareRefusesLabAndStaleProcess(t *testing.T) {
	node, conf := execNodeFixture(t)
	security := "token-lab"
	node.UTCDSocket = controlServer(t, func() map[string]string {
		return map[string]string{"status": "running", "security": security, "role": "client"}
	})
	p := execNodePlan()
	if _, err := node.Prepare(context.Background(), p, 0); err == nil {
		t.Fatal("lab dataplane must not reach PREPARED")
	}
	security = "static-mtls"
	if _, err := node.Prepare(context.Background(), p, 0); err == nil {
		t.Fatal("missing current driver status must be refused")
	}
	writeExecStatus(t, conf, 1, "", "")
	receipt, err := node.Prepare(context.Background(), p, 0)
	if err != nil || receipt.State != "PREPARED" || receipt.PID != 4242 {
		t.Fatalf("prepare = %+v, %v", receipt, err)
	}
	node.ProcessCommand = func(pid uint64) (string, error) { return "/usr/bin/other -c /tmp", nil }
	if _, err := node.Prepare(context.Background(), p, 0); err == nil {
		t.Fatal("PID belonging to another program must be refused")
	}
	node.ProcessCommand = func(pid uint64) (string, error) { return "", fmt.Errorf("no such process") }
	if _, err := node.Prepare(context.Background(), p, 0); err == nil {
		t.Fatal("dead PID must be refused")
	}
	if _, err := node.Prepare(context.Background(), p, 1); err == nil {
		t.Fatal("side that is not this installed node must be refused")
	}
}

func TestExecNodeApplyVerifyBlock(t *testing.T) {
	node, conf := execNodeFixture(t)
	var mu sync.Mutex
	sent, received := uint64(0), uint64(0)
	node.UTCDSocket = controlServer(t, func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		return map[string]string{"status": "running", "security": "static-mtls", "role": "client",
			"udp_to_tcp": fmt.Sprint(sent), "tcp_to_udp": fmt.Sprint(received),
			"session0_epoch": "5", "session0_from_udp": fmt.Sprint(sent),
			"session0_to_udp": fmt.Sprint(received), "session0_from_tcp": "0",
			"session0_to_tcp": "0"}
	})
	p := execNodePlan()
	peer := peerPolicy(p, 0)
	policy := canonicalPeerLine(peer, "policy")
	digest := sha512.Sum512([]byte(policy + "\n"))
	peerRow := fmt.Sprintf("peer %d %d %d APPLIED POLICY_LOADED %s %x", peer.PeerID, peer.PairID,
		peer.PairGen, peer.Manifest, digest)
	writeExecStatus(t, conf, 2, policy, peerRow)
	receipt, err := node.Apply(context.Background(), p, 0, Receipt{State: "PREPARED", OperationID: p.OperationID})
	if err != nil || receipt.State != "APPLIED" {
		t.Fatalf("apply = %+v, %v", receipt, err)
	}
	var other [2]Receipt
	other[1] = Receipt{OperationID: p.OperationID, Generation: p.Generation, NodeID: 8, State: "APPLIED", PID: 99, BootID: strings.Repeat("b", 32)}
	if _, err := node.Verify(context.Background(), p, 0, other); err == nil {
		t.Fatal("counters that never advance must not verify")
	}
	mu.Lock()
	sent, received = 11, 12
	mu.Unlock()
	verified, err := node.Verify(context.Background(), p, 0, other)
	if err != nil || verified.State != "VERIFIED" || verified.Sent != 11 || verified.Received != 12 ||
		verified.PeerBootID != other[1].BootID {
		t.Fatalf("verify = %+v, %v", verified, err)
	}
	other[1].State = "PREPARED"
	if _, err := node.Verify(context.Background(), p, 0, other); err == nil {
		t.Fatal("peer without APPLIED evidence must not verify")
	}
	other[1].State = "APPLIED"
	// Compensation always uses a larger generation and a fixed past deadline.
	blockPlan := p
	blockPlan.Generation = p.Generation + 1
	blockPlan.ValidUntil = 1
	blockPlan.Manifest = blockPlan.Digest()
	blockedPolicy := canonicalPeerLine(peerPolicy(blockPlan, 0), "policy")
	writeExecStatus(t, conf, 3, blockedPolicy, "")
	blocked, err := node.Block(context.Background(), blockPlan, 0)
	if err != nil || blocked.State != "BLOCKED" {
		t.Fatalf("block = %+v, %v", blocked, err)
	}
	writeExecStatus(t, conf, 3, blockedPolicy, peerRow)
	if _, err := node.Block(context.Background(), blockPlan, 0); err == nil {
		t.Fatal("a pair still reported APPLIED must not be reported BLOCKED")
	}
}

// Counters that only move on a session outside the Apply baseline (a reconnect
// or another incarnation) must not verify: attribution requires the operation's
// own live session to advance.
func TestExecNodeVerifyRejectsUnattributedSessionCounters(t *testing.T) {
	node, conf := execNodeFixture(t)
	var mu sync.Mutex
	total := uint64(0)
	advanced := false
	node.UTCDSocket = controlServer(t, func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		fields := map[string]string{"status": "running", "security": "static-mtls", "role": "client",
			"udp_to_tcp": fmt.Sprint(total), "tcp_to_udp": fmt.Sprint(total),
			"session0_epoch": "5", "session0_from_udp": "0", "session0_to_udp": "0",
			"session0_from_tcp": "0", "session0_to_tcp": "0"}
		if advanced {
			fields["session1_epoch"] = "9"
			fields["session1_from_udp"] = fmt.Sprint(total)
			fields["session1_to_udp"] = fmt.Sprint(total)
			fields["session1_from_tcp"] = "0"
			fields["session1_to_tcp"] = "0"
		}
		return fields
	})
	p := execNodePlan()
	peer := peerPolicy(p, 0)
	policy := canonicalPeerLine(peer, "policy")
	digest := sha512.Sum512([]byte(policy + "\n"))
	peerRow := fmt.Sprintf("peer %d %d %d APPLIED POLICY_LOADED %s %x", peer.PeerID, peer.PairID,
		peer.PairGen, peer.Manifest, digest)
	writeExecStatus(t, conf, 2, policy, peerRow)
	if _, err := node.Apply(context.Background(), p, 0,
		Receipt{State: "PREPARED", OperationID: p.OperationID}); err != nil {
		t.Fatal(err)
	}
	var other [2]Receipt
	other[1] = Receipt{OperationID: p.OperationID, Generation: p.Generation, NodeID: 8,
		State: "APPLIED", PID: 99, BootID: strings.Repeat("b", 32)}
	mu.Lock()
	total, advanced = 42, true
	mu.Unlock()
	if _, err := node.Verify(context.Background(), p, 0, other); err == nil {
		t.Fatal("counters moving on a session outside the operation must not verify")
	}
}

func TestUTCDSessionsParsingAndRewind(t *testing.T) {
	fields := map[string]string{"sessions": "1", "udp_to_tcp": "7",
		"session0_epoch": "5", "session0_from_udp": "10", "session0_to_udp": "20",
		"session0_from_tcp": "1", "session0_to_tcp": "2"}
	sessions, err := UTCDSessions(fields)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("parse = %v, %v", sessions, err)
	}
	if got := sessions[5]; got.FromUDP != 10 || got.ToUDP != 20 || got.FromTCP != 1 || got.ToTCP != 2 {
		t.Fatalf("parsed = %+v", got)
	}
	if _, err := UTCDSessions(map[string]string{"session0_epoch": "5",
		"session0_from_udp": "1"}); err == nil {
		t.Fatal("incomplete session rows must be refused")
	}
	// A rewound leg on the same epoch is evidence corruption, not progress.
	_, _, _, err = sessionAdvance(map[uint64]UTCSession{5: {Epoch: 5, FromUDP: 10}},
		map[string]string{"session0_epoch": "5", "session0_from_udp": "9",
			"session0_to_udp": "20", "session0_from_tcp": "0", "session0_to_tcp": "0"})
	if err == nil {
		t.Fatal("rewound session counters must not attribute")
	}
}
