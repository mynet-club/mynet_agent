package utc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/driver"
)

// SSHNode drives a node on another host over SSH for development and test
// topologies: the single-host RunPlan executor cannot reach a remote node.
//
// The trust shape stays the operator-installed one. The SSH channel only
// carries projection JSON and fixed invocations of the remote's own installed
// CLI (`mynet utc publish-peer|verify`) plus read-only status reads. A plan
// can never supply an executable, a trust root or an arbitrary command; every
// remote path comes from this locally configured struct.
type SSHNode struct {
	NodeID        uint64
	Instance      string
	SSHHost       string // operator-pinned ssh target
	SSHKey        string // operator-pinned key
	RemoteConfDir string
	RemoteCLI     string // operator-installed mynet binary on the remote
	RemoteGNB     string // operator-installed gnb binary on the remote
	RemoteUTCD    string // operator-installed utc binary for status reads
	RemoteSocket  string
	Now           func() time.Time
	// Budget bounds each operation's evidence wait; the default absorbs ssh
	// latency while staying far below a stuck remote.
	Budget time.Duration
	// Remote runs a remote command line with optional stdin; tests replace it.
	Remote func(command string, stdin string) ([]byte, error)

	mu       sync.Mutex
	baseline map[string][2]uint64
	session  map[string]map[uint64]UTCSession
}

func (n *SSHNode) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *SSHNode) budget() time.Duration {
	if n.Budget > 0 {
		return n.Budget
	}
	return 15 * time.Second
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func (n *SSHNode) remote(command string, stdin string) ([]byte, error) {
	if n.Remote != nil {
		return n.Remote(command, stdin)
	}
	args := []string{"-o", "BatchMode=yes",
		// Multiplex every call over one connection: test polling must not look
		// like a brute-force attempt to the remote's fail2ban.
		"-o", "ControlMaster=auto", "-o", "ControlPersist=600s",
		"-o", "ControlPath=/tmp/mynet-ssh-%r@%h:%p"}
	if n.SSHKey != "" {
		args = append(args, "-i", n.SSHKey)
	}
	args = append(args, n.SSHHost, command)
	cmd := exec.Command("ssh", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("ssh %s: %w: %s", n.SSHHost, err, string(out))
	}
	return out, nil
}

// observe reads the remote GNB boot and PID from the service's own status
// header: status-v1 <scope> <instance> <local> <boot> <gen> <ts> <reason> <pid>.
func (n *SSHNode) observe() (string, uint64, error) {
	out, err := n.remote(fmt.Sprintf("sudo -n cat %s/utc.status", shellQuote(n.RemoteConfDir)), "")
	if err != nil {
		return "", 0, err
	}
	lines := strings.Split(string(out), "\n")
	header := strings.Fields(lines[0])
	if len(header) < 9 || header[0] != "status-v1" {
		return "", 0, fmt.Errorf("remote utc.status header malformed: %q", lines[0])
	}
	if header[1] != "lab" || header[2] != n.Instance ||
		header[3] != strconv.FormatUint(n.NodeID, 10) {
		return "", 0, fmt.Errorf("remote utc.status identity mismatch: %q", lines[0])
	}
	pid, err := strconv.ParseUint(header[8], 10, 64)
	if err != nil || pid == 0 || header[4] == "" {
		return "", 0, fmt.Errorf("remote utc.status boot/pid unusable: %q", lines[0])
	}
	return header[4], pid, nil
}

func (n *SSHNode) receipt(p Plan, side int, state, boot string, pid uint64) Receipt {
	return Receipt{
		OperationID: p.OperationID, Scope: p.Scope, PairID: p.PairID,
		Generation: p.Generation, Manifest: p.Manifest, NodeID: n.NodeID,
		BootID: boot, PID: pid, ObservedAt: uint64(n.now().Unix()),
		ValidUntil: p.ValidUntil, State: state,
	}
}

func (n *SSHNode) check(p Plan, side int) error {
	if side != 0 && side != 1 {
		return errors.New("UTC side must be 0 or 1")
	}
	if p.Sides[side].NodeID != n.NodeID {
		return fmt.Errorf("UTC node %d does not serve side %d", n.NodeID, side)
	}
	return nil
}

func (n *SSHNode) projection(p Plan, side int) driver.UTCProjection {
	return driver.UTCProjection{Scope: p.Scope, Instance: n.Instance, LocalID: n.NodeID,
		ConfigGen: 0, Peers: []driver.UTCPeerPolicy{peerPolicy(p, side)}}
}

// publish stages exactly one peer projection and feeds it over stdin to the
// remote's merge publisher (publish-peer), which keeps every peer it does not
// own. It returns the projection as actually published (with the writer's
// real config_gen): evidence must be checked against that, not the
// config_gen=0 input the merge publisher requires.
func (n *SSHNode) publish(ctx context.Context, p Plan, side int, peer driver.UTCPeerPolicy) (string, driver.UTCProjection, error) {
	projection := n.projection(p, side)
	projection.Peers = []driver.UTCPeerPolicy{peer}
	raw, err := json.Marshal(projection)
	if err != nil {
		return "", driver.UTCProjection{}, err
	}
	staged := fmt.Sprintf("/tmp/mynet-utc-%d-%d-%d.json", p.PairID, p.Generation, n.NodeID)
	command := fmt.Sprintf("cat > %s && chmod 600 %s && sudo -n %s utc publish-peer -c %s --binary %s --file %s",
		shellQuote(staged), shellQuote(staged), shellQuote(n.RemoteCLI),
		shellQuote(n.RemoteConfDir), shellQuote(n.RemoteGNB), shellQuote(staged))
	out, err := n.remote(command, string(raw))
	if err != nil {
		return "", driver.UTCProjection{}, fmt.Errorf("remote publish-peer: %w", err)
	}
	var result struct {
		State      string               `json:"state"`
		ConfigGen  uint64               `json:"config_gen"`
		Projection driver.UTCProjection `json:"projection"`
	}
	if err := json.Unmarshal(out, &result); err != nil || result.ConfigGen == 0 {
		return "", driver.UTCProjection{}, fmt.Errorf("remote publish-peer output unusable: %s", string(out))
	}
	return staged, result.Projection, nil
}

// verifyPublished re-stages the projection as actually published, then asks
// the remote's local APPLIED verifier to confirm it under the current boot.
func (n *SSHNode) verifyPublished(staged string, projection driver.UTCProjection, boot string, pid uint64) error {
	raw, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	if _, err := n.remote(fmt.Sprintf("cat > %s && chmod 600 %s",
		shellQuote(staged), shellQuote(staged)), string(raw)); err != nil {
		return err
	}
	_, err = n.remote(fmt.Sprintf("sudo -n %s utc verify -c %s --binary %s --file %s --boot %s --pid %d",
		shellQuote(n.RemoteCLI), shellQuote(n.RemoteConfDir), shellQuote(n.RemoteGNB),
		shellQuote(staged), shellQuote(boot), pid), "")
	return err
}

func (n *SSHNode) utcdStatus() (map[string]string, error) {
	out, err := n.remote(fmt.Sprintf("sudo -n %s --socket %s status --json",
		shellQuote(n.RemoteUTCD), shellQuote(n.RemoteSocket)), "")
	if err != nil {
		return nil, err
	}
	// The remote CLI emits real JSON numbers for numeric fields; normalize
	// every value to its literal text for the shared parsers.
	decoder := json.NewDecoder(bytes.NewReader(out))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("remote utcd status: %w", err)
	}
	info := map[string]string{}
	for key, value := range raw {
		switch typed := value.(type) {
		case string:
			info[key] = typed
		case json.Number:
			info[key] = typed.String()
		default:
			return nil, fmt.Errorf("remote utcd status field %q has unsupported type", key)
		}
	}
	if info["status"] != "running" {
		return nil, fmt.Errorf("remote utcd not running: %v", info)
	}
	return info, nil
}

func (n *SSHNode) Prepare(ctx context.Context, p Plan, side int) (Receipt, error) {
	var empty Receipt
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := n.check(p, side); err != nil {
		return empty, err
	}
	if _, err := n.remote(fmt.Sprintf("sudo -n test -x %s && sudo -n test -x %s",
		shellQuote(n.RemoteCLI), shellQuote(n.RemoteGNB)), ""); err != nil {
		return empty, fmt.Errorf("remote install incomplete: %w", err)
	}
	boot, pid, err := n.observe()
	if err != nil {
		return empty, err
	}
	return n.receipt(p, side, "PREPARED", boot, pid), nil
}

func (n *SSHNode) Apply(ctx context.Context, p Plan, side int, prepared Receipt) (Receipt, error) {
	var empty Receipt
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := n.check(p, side); err != nil {
		return empty, err
	}
	if prepared.State != "PREPARED" || prepared.OperationID != p.OperationID {
		return empty, errors.New("UTC apply requires matching PREPARED evidence")
	}
	waitCtx, cancel := context.WithTimeout(ctx, n.budget())
	defer cancel()
	staged, published, err := n.publish(waitCtx, p, side, peerPolicy(p, side))
	if err != nil {
		return empty, err
	}
	defer func() {
		_, _ = n.remote("rm -f "+shellQuote(staged), "")
	}()
	var boot string
	var pid uint64
	for {
		boot, pid, err = n.observe()
		if err == nil {
			if verifyErr := n.verifyPublished(staged, published, boot, pid); verifyErr == nil {
				break
			} else if waitCtx.Err() != nil {
				return empty, fmt.Errorf("UTC APPLIED evidence: %w", verifyErr)
			}
		} else if waitCtx.Err() != nil {
			return empty, err
		}
		select {
		case <-waitCtx.Done():
			return empty, fmt.Errorf("UTC APPLIED evidence: %w", waitCtx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	fields, err := n.utcdStatus()
	if err != nil {
		return empty, err
	}
	sent, err := UTCDCounter(fields, "udp_to_tcp")
	if err != nil {
		return empty, err
	}
	received, err := UTCDCounter(fields, "tcp_to_udp")
	if err != nil {
		return empty, err
	}
	sessions, err := UTCDSessions(fields)
	if err != nil {
		return empty, err
	}
	n.mu.Lock()
	if n.baseline == nil {
		n.baseline = map[string][2]uint64{}
		n.session = map[string]map[uint64]UTCSession{}
	}
	n.baseline[p.OperationID] = [2]uint64{sent, received}
	n.session[p.OperationID] = sessions
	n.mu.Unlock()
	if waitCtx.Err() != nil {
		return empty, fmt.Errorf("UTC apply exceeded budget: %w", waitCtx.Err())
	}
	receipt := n.receipt(p, side, "APPLIED", boot, pid)
	receipt.Sent, receipt.Received = sent, received
	if len(sessions) == 1 {
		for epoch := range sessions {
			receipt.SessionEpoch = epoch
		}
	}
	return receipt, nil
}

func (n *SSHNode) Verify(ctx context.Context, p Plan, side int, applied [2]Receipt) (Receipt, error) {
	var empty Receipt
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := n.check(p, side); err != nil {
		return empty, err
	}
	other := applied[1-side]
	if other.OperationID != p.OperationID || other.Generation != p.Generation ||
		other.NodeID != p.Sides[1-side].NodeID || other.State != "APPLIED" || other.PID == 0 {
		return empty, errors.New("UTC verify requires current APPLIED evidence from both sides")
	}
	waitCtx, cancel := context.WithTimeout(ctx, n.budget())
	defer cancel()
	n.mu.Lock()
	base := n.baseline[p.OperationID]
	sessionBase := n.session[p.OperationID]
	n.mu.Unlock()
	if sessionBase == nil {
		return empty, errors.New("UTC verify has no session baseline")
	}
	var fields map[string]string
	var sent, received uint64
	var err error
	for {
		fields, err = n.utcdStatus()
		if err != nil {
			return empty, err
		}
		if sent, err = UTCDCounter(fields, "udp_to_tcp"); err != nil {
			return empty, err
		}
		if received, err = UTCDCounter(fields, "tcp_to_udp"); err != nil {
			return empty, err
		}
		if sent > base[0] && received > base[1] {
			break
		}
		if waitCtx.Err() != nil {
			return empty, fmt.Errorf("UTC verify needs bidirectional counters above %d/%d, got %d/%d",
				base[0], base[1], sent, received)
		}
		select {
		case <-waitCtx.Done():
			return empty, fmt.Errorf("UTC verify needs bidirectional counters above %d/%d, got %d/%d",
				base[0], base[1], sent, received)
		case <-time.After(200 * time.Millisecond):
		}
	}
	attributed, sentBytes, receivedBytes, err := sessionAdvance(sessionBase, fields)
	if err != nil {
		return empty, err
	}
	// Recheck after the wait: the operation's policy must still be current.
	boot, pid, err := n.observe()
	if err != nil {
		return empty, err
	}
	staged, published, err := n.publish(waitCtx, p, side, peerPolicy(p, side))
	if err != nil {
		return empty, fmt.Errorf("UTC verify recheck: %w", err)
	}
	// publish-peer is idempotent for identical content; the published
	// projection must verify as APPLIED under the current boot and pid.
	verifyErr := n.verifyPublished(staged, published, boot, pid)
	_, _ = n.remote("rm -f "+shellQuote(staged), "")
	if verifyErr != nil {
		return empty, fmt.Errorf("UTC verify recheck: %w", verifyErr)
	}
	if waitCtx.Err() != nil {
		return empty, fmt.Errorf("UTC verify exceeded budget: %w", waitCtx.Err())
	}
	receipt := n.receipt(p, side, "VERIFIED", boot, pid)
	receipt.PeerBootID = other.BootID
	receipt.Sent, receipt.Received = sent, received
	receipt.SessionEpoch = attributed.Epoch
	receipt.SessionSent = sentBytes
	receipt.SessionReceived = receivedBytes
	return receipt, nil
}

// Block publishes a new generation whose permit deadline is already in the
// past, then requires the remote to stop reporting the pair as APPLIED.
func (n *SSHNode) Block(ctx context.Context, p Plan, side int) (Receipt, error) {
	var empty Receipt
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := n.check(p, side); err != nil {
		return empty, err
	}
	peer := peerPolicy(p, side)
	peer.ExpiresAt = 1
	staged, _, err := n.publish(ctx, p, side, peer)
	if err != nil {
		return empty, err
	}
	_, _ = n.remote("rm -f "+shellQuote(staged), "")
	boot, pid, err := n.observe()
	if err != nil {
		return empty, err
	}
	out, err := n.remote(fmt.Sprintf("sudo -n cat %s/utc.status", shellQuote(n.RemoteConfDir)), "")
	if err != nil {
		return empty, err
	}
	for _, row := range strings.Split(string(out), "\n") {
		fields := strings.Fields(row)
		if len(fields) >= 5 && fields[0] == "peer" && fields[1] == strconv.FormatUint(peer.PeerID, 10) {
			if fields[4] == "APPLIED" {
				return empty, errors.New("UTC block did not take effect on the remote")
			}
			return n.receipt(p, side, "BLOCKED", boot, pid), nil
		}
	}
	return empty, errors.New("UTC block evidence missing on the remote")
}
