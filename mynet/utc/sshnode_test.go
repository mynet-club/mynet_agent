//go:build !windows

package utc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRemote is a stateful stand-in for the operator-installed remote CLI: the
// publish stage feeds it a projection, the status reads reflect its state.
type fakeRemote struct {
	mu      sync.Mutex
	status  string
	fields  map[string]string
	staged  string
	applied bool
}

func (f *fakeRemote) run(command string, stdin string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.Contains(command, "test -x"):
		return nil, nil
	case strings.Contains(command, "utc publish-peer"):
		var projection map[string]any
		if err := json.Unmarshal([]byte(stdin), &projection); err != nil {
			return nil, err
		}
		f.applied = true
		state := "APPLIED"
		peer := projection["peers"].([]any)[0].(map[string]any)
		if expires, ok := peer["expires_at"].(float64); ok && expires == 1 {
			state, f.applied = "BLOCKED", false
		}
		num := func(key string) uint64 { return uint64(peer[key].(float64)) }
		f.status = fmt.Sprintf("status-v1 lab node8 8 %s 7 1900000000 OK 4242\n"+
			"peer %d %d %d %s POLICY_LOADED deadbeef cafebabe\n",
			strings.Repeat("b", 32), num("peer_id"), num("pair_id"), num("pair_gen"), state)
		projection["config_gen"] = 7
		result, err := json.Marshal(map[string]any{"state": "PUBLISHED",
			"config_gen": 7, "projection": projection})
		return result, err
	case strings.HasPrefix(command, "cat >"):
		// Evidence re-staging of the published projection. run() already
		// holds the mutex for the whole dispatch.
		f.staged = stdin
		return nil, nil
	case strings.Contains(command, "utc verify -c"):
		if f.applied && f.staged != "" {
			return []byte(`{"state":"APPLIED"}`), nil
		}
		return nil, errors.New("remote not applied")
	case strings.Contains(command, "status --json"):
		raw, err := json.Marshal(f.fields)
		return raw, err
	case strings.Contains(command, "utc.status"):
		return []byte(f.status), nil
	case strings.HasPrefix(command, "rm -f "):
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected remote command: %s", command)
}

func TestSSHNodeDriveCycle(t *testing.T) {
	remote := &fakeRemote{
		status: "status-v1 lab node8 8 " + strings.Repeat("b", 32) + " 7 1900000000 OK 4242\n",
		fields: map[string]string{"status": "running", "connection_epoch": "3",
			"udp_to_tcp": "10", "tcp_to_udp": "10",
			"session0_epoch": "3", "session0_from_udp": "10", "session0_to_udp": "10",
			"session0_from_tcp": "0", "session0_to_tcp": "0"},
	}
	node := &SSHNode{
		NodeID: 8, Instance: "node8", SSHHost: "admin@18.141.199.14",
		RemoteConfDir: "/tmp/x/2", RemoteCLI: "/tmp/r6/bin/mynet",
		RemoteGNB: "/tmp/r6/bin/gnb", RemoteUTCD: "/tmp/r6/bin/utc",
		RemoteSocket: "/tmp/x/relay.sock",
		Now:          func() time.Time { return time.Unix(1900000000, 0) },
		Remote:       remote.run,
	}
	p := execNodePlan()
	prepared, err := node.Prepare(context.Background(), p, 1)
	if err != nil || prepared.State != "PREPARED" {
		t.Fatalf("prepare = %+v, %v", prepared, err)
	}
	applied, err := node.Apply(context.Background(), p, 1, prepared)
	if err != nil || applied.State != "APPLIED" || applied.SessionEpoch != 3 {
		t.Fatalf("apply = %+v, %v", applied, err)
	}
	var receipts [2]Receipt
	receipts[0] = Receipt{OperationID: p.OperationID, Generation: p.Generation, NodeID: 7,
		State: "APPLIED", PID: 77, BootID: strings.Repeat("a", 32)}
	receipts[1] = applied
	// No counter movement yet: verification must wait and fail.
	node.Budget = 300 * time.Millisecond
	if _, err := node.Verify(context.Background(), p, 1, receipts); err == nil {
		t.Fatal("flat counters must not verify")
	}
	node.Budget = 0
	remote.mu.Lock()
	remote.fields["udp_to_tcp"] = "21"
	remote.fields["tcp_to_udp"] = "22"
	remote.fields["session0_from_udp"] = "21"
	remote.fields["session0_to_udp"] = "22"
	remote.mu.Unlock()
	verified, err := node.Verify(context.Background(), p, 1, receipts)
	if err != nil || verified.State != "VERIFIED" {
		t.Fatalf("verify = %+v, %v", verified, err)
	}
	if verified.SessionSent != 11 || verified.SessionReceived != 12 ||
		verified.SessionEpoch != 3 {
		t.Fatalf("session attribution = %+v", verified)
	}
	// The block generation keeps the shape the ledger accepts: the remote
	// reports the pair as BLOCKED and the evidence must say so.
	blocked := p
	blocked.Generation = p.Generation + 1
	blocked.ValidUntil = 1
	blocked.Manifest = blocked.Digest()
	result, err := node.Block(context.Background(), blocked, 1)
	if err != nil || result.State != "BLOCKED" {
		t.Fatalf("block = %+v, %v", result, err)
	}
}
