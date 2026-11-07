package utc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/driver"
)

// ExecNode is the real executor adapter for one installed node. Every path and
// identity comes from the local installation, never from the plan or from a
// remote request: the plan only selects pair/generation/manifest content.
//
// Prepare is read-only and refuses both lab dataplane modes. Apply publishes
// through the existing driver and then requires the current GNB process to
// report APPLIED for that exact projection. Verify additionally needs the
// operation-bound bidirectional counters to advance. Block publishes a new
// generation whose permit deadline is already in the past and requires the
// driver to stop reporting the pair as APPLIED.
type ExecNode struct {
	NodeID     uint64
	Instance   string
	ConfDir    string
	GNBBinary  string
	UTCDSocket string
	Now        func() time.Time
	// ProcessCommand returns the current command line of a live PID. The default
	// uses the operating system; tests may substitute a deterministic inspector
	// to model PID reuse. It never changes authorization decisions in production.
	ProcessCommand func(pid uint64) (string, error)
	// DriverExec optionally overrides the driver's command execution seam, the
	// same seam the driver itself exposes for capability tests.
	DriverExec func(string, ...string) ([]byte, error)

	mu       sync.Mutex
	baseline map[string][2]uint64
	// sessionBaseline snapshots the per-session legs at Apply so Verify can
	// attribute the counter advance to this operation's own session.
	sessionBaseline map[string]map[uint64]UTCSession
}

func (n *ExecNode) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *ExecNode) driver() *driver.GNBDriver {
	d := driver.NewGNBDriver(int64(n.NodeID))
	d.ConfDir = n.ConfDir
	d.Binary = n.GNBBinary
	if n.DriverExec != nil {
		d.Exec = n.DriverExec
	}
	return d
}

func (n *ExecNode) side(p Plan, side int) error {
	if side < 0 || side > 1 {
		return errors.New("UTC side out of range")
	}
	if p.Sides[side].NodeID != n.NodeID || p.Sides[side].Instance != n.Instance {
		return errors.New("UTC plan side does not match the installed node")
	}
	if n.ConfDir == "" || n.GNBBinary == "" || !filepath.IsAbs(n.ConfDir) || !filepath.IsAbs(n.GNBBinary) {
		return errors.New("UTC executor requires installed absolute driver paths")
	}
	return nil
}

// observeProcess reads the current driver status header and independently
// confirms the reported PID is a live process of this installation. A stale
// status file or a reused PID cannot be adopted as current evidence.
func (n *ExecNode) observeProcess(d *driver.GNBDriver) (boot string, pid uint64, err error) {
	raw, err := os.ReadFile(filepath.Join(n.ConfDir, "utc.status"))
	if err != nil {
		return "", 0, fmt.Errorf("UTC current driver status: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) < 2 {
		return "", 0, errors.New("UTC driver status incomplete")
	}
	header := strings.Fields(lines[0])
	if len(header) != 9 || header[0] != "status-v1" {
		return "", 0, errors.New("UTC driver status header invalid")
	}
	pid, err = strconv.ParseUint(header[8], 10, 64)
	if err != nil || pid == 0 {
		return "", 0, errors.New("UTC driver status PID invalid")
	}
	generated, err := strconv.ParseUint(header[6], 10, 64)
	if err != nil {
		return "", 0, errors.New("UTC driver status timestamp invalid")
	}
	now := n.now()
	if generated > uint64(now.Unix()) || uint64(now.Unix())-generated > 5 {
		return "", 0, errors.New("UTC driver status is stale or future dated")
	}
	inspect := n.ProcessCommand
	if inspect == nil {
		inspect = processCommandLine
	}
	command, err := inspect(pid)
	if err != nil {
		return "", 0, fmt.Errorf("UTC current process observation: %w", err)
	}
	if !strings.Contains(command, n.ConfDir) || !strings.Contains(command, filepath.Base(n.GNBBinary)) {
		return "", 0, errors.New("UTC status PID does not belong to this installation")
	}
	return header[4], pid, nil
}

func processCommandLine(pid uint64) (string, error) {
	output, err := exec.Command("ps", "-p", strconv.FormatUint(pid, 10), "-o", "args=").Output()
	if err != nil {
		return "", err
	}
	command := strings.TrimSpace(string(output))
	if command == "" {
		return "", errors.New("process not found")
	}
	return command, nil
}

func (n *ExecNode) receipt(p Plan, side int, state, boot string, pid uint64) Receipt {
	return Receipt{
		OperationID: p.OperationID, Scope: p.Scope, PairID: p.PairID, Generation: p.Generation,
		Manifest: p.Manifest, NodeID: n.NodeID, BootID: boot, PID: pid,
		ObservedAt: uint64(n.now().Unix()), ValidUntil: p.ValidUntil, State: state,
	}
}

// Prepare only inspects installed capability, durable history, the current
// process and the dataplane's configured security mode. It writes nothing.
func (n *ExecNode) Prepare(ctx context.Context, p Plan, side int) (Receipt, error) {
	var empty Receipt
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := n.side(p, side); err != nil {
		return empty, err
	}
	fields, err := UTCDStatus(ctx, n.UTCDSocket)
	if err != nil {
		return empty, err
	}
	if _, err := UTCDSecurity(fields); err != nil {
		return empty, err
	}
	d := n.driver()
	// Managed startup history and driver capability are required; an existing
	// projection is not, because the first pair has not been published yet.
	if _, err := d.UTCLaunchArgs(); err != nil {
		return empty, err
	}
	boot, pid, err := n.observeProcess(d)
	if err != nil {
		return empty, err
	}
	return n.receipt(p, side, "PREPARED", boot, pid), nil
}

func peerPolicy(p Plan, side int) driver.UTCPeerPolicy {
	return driver.UTCPeerPolicy{
		PeerID: p.Sides[1-side].NodeID, PairID: p.PairID, PairGen: p.Generation,
		Mode: 1, ExpiresAt: p.ValidUntil, Export: p.Sides[side].Export,
		ScopeID: p.Sides[side].ScopeID, Manifest: p.Manifest,
	}
}

func (n *ExecNode) Apply(ctx context.Context, p Plan, side int, prepared Receipt) (Receipt, error) {
	var empty Receipt
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := n.side(p, side); err != nil {
		return empty, err
	}
	if prepared.State != "PREPARED" || prepared.OperationID != p.OperationID {
		return empty, errors.New("UTC apply requires matching PREPARED evidence")
	}
	// The whole Apply, including the publish lock and durable write, shares one
	// bounded budget with the APPLIED wait.
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	d := n.driver()
	published, err := d.PublishUTCPeer(waitCtx, p.Scope, n.Instance, n.NodeID, peerPolicy(p, side))
	if err != nil {
		return empty, err
	}
	// The live GNB applies the projection on its own poll cycle; wait a
	// bounded window for the exact APPLIED evidence before failing.
	var boot string
	var pid uint64
	for {
		boot, pid, err = n.observeProcess(d)
		if err == nil {
			if verifyErr := d.VerifyUTCProjection(published, boot, pid, n.now()); verifyErr == nil {
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
		case <-time.After(100 * time.Millisecond):
		}
	}
	fields, err := UTCDStatus(waitCtx, n.UTCDSocket)
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
	}
	if n.sessionBaseline == nil {
		n.sessionBaseline = map[string]map[uint64]UTCSession{}
	}
	n.baseline[p.OperationID] = [2]uint64{sent, received}
	n.sessionBaseline[p.OperationID] = sessions
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

// Verify re-reads the current process and requires both directions of the
// dataplane to have advanced since Apply. A loaded configuration with no
// operation-bound traffic is not VERIFIED.
func (n *ExecNode) Verify(ctx context.Context, p Plan, side int, applied [2]Receipt) (Receipt, error) {
	var empty Receipt
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := n.side(p, side); err != nil {
		return empty, err
	}
	other := applied[1-side]
	if other.OperationID != p.OperationID || other.Generation != p.Generation ||
		other.NodeID != p.Sides[1-side].NodeID || other.State != "APPLIED" || other.PID == 0 {
		return empty, errors.New("UTC verify requires current APPLIED evidence from both sides")
	}
	d := n.driver()
	// One bounded budget covers the evidence I/O of Verify: projection reads,
	// control queries and the rechecks all start with the remaining time, and a
	// success that lands after the deadline is refused. The synchronous file
	// write inside a publish is not interruptible mid-write; a write that
	// completes late is never certified, and its durable state is reconciled by
	// the idempotent retry path rather than rolled back here.
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	projection, err := d.ReadUTCProjection(waitCtx)
	if err != nil {
		return empty, err
	}
	if projection == nil {
		return empty, errors.New("UTC verify has no durable projection evidence")
	}
	target := peerPolicy(p, side)
	current, found := projection.Peer(target.PeerID)
	if !found || current != target {
		return empty, errors.New("UTC verify projection does not carry this operation's policy")
	}
	boot, pid, err := n.observeProcess(d)
	if err != nil {
		return empty, err
	}
	if err := d.VerifyUTCProjection(*projection, boot, pid, n.now()); err != nil {
		return empty, fmt.Errorf("UTC verify APPLIED recheck: %w", err)
	}
	var fields map[string]string
	var sent, received uint64
	n.mu.Lock()
	baseline, known := n.baseline[p.OperationID]
	n.mu.Unlock()
	if !known {
		return empty, errors.New("UTC verify has no operation baseline")
	}
	// Apply snapshots the counters and Verify runs moments later; real traffic
	// needs a bounded window to advance both directions. A silent pair still
	// fails, and cancellation is honoured.
	for {
		fields, err = UTCDStatus(waitCtx, n.UTCDSocket)
		if err != nil {
			return empty, err
		}
		if sent, err = UTCDCounter(fields, "udp_to_tcp"); err != nil {
			return empty, err
		}
		if received, err = UTCDCounter(fields, "tcp_to_udp"); err != nil {
			return empty, err
		}
		if sent > baseline[0] && received > baseline[1] {
			break
		}
		if waitCtx.Err() != nil {
			return empty, fmt.Errorf("UTC verify needs bidirectional counters above %d/%d, got %d/%d",
				baseline[0], baseline[1], sent, received)
		}
		select {
		case <-waitCtx.Done():
			return empty, fmt.Errorf("UTC verify needs bidirectional counters above %d/%d, got %d/%d",
				baseline[0], baseline[1], sent, received)
		case <-time.After(100 * time.Millisecond):
		}
	}
	// Attribution: instance counters are cumulative totals; VERIFIED must come
	// from this operation's own live session advancing in both directions.
	n.mu.Lock()
	sessionBase := n.sessionBaseline[p.OperationID]
	n.mu.Unlock()
	if sessionBase == nil {
		return empty, errors.New("UTC verify has no session baseline")
	}
	attributed, sentBytes, receivedBytes, err := sessionAdvance(sessionBase, fields)
	if err != nil {
		return empty, err
	}
	// Recheck after the wait: the operation's policy must still be current.
	final, err := d.ReadUTCProjection(waitCtx)
	if err != nil || final == nil {
		return empty, errors.New("UTC verify recheck lost the projection")
	}
	boot, pid, err = n.observeProcess(d)
	if err != nil {
		return empty, err
	}
	if err := d.VerifyUTCProjection(*final, boot, pid, n.now()); err != nil {
		return empty, fmt.Errorf("UTC verify recheck: %w", err)
	}
	if current, found := final.Peer(target.PeerID); !found || current != target {
		return empty, errors.New("UTC verify recheck: policy changed under this operation")
	}
	if waitCtx.Err() != nil {
		return empty, fmt.Errorf("UTC verify exceeded budget: %w", waitCtx.Err())
	}
	receipt := n.receipt(p, side, "VERIFIED", boot, pid)
	// Bind the peer's current boot into the evidence: VERIFIED is about this
	// operation on both ends, not about one side's counters alone.
	receipt.PeerBootID = other.BootID
	receipt.Sent, receipt.Received = sent, received
	receipt.SessionEpoch = attributed.Epoch
	receipt.SessionSent = sentBytes
	receipt.SessionReceived = receivedBytes
	return receipt, nil
}

// sessionAdvance attributes counter movement to the operation's own session:
// the same live epoch must advance both directions above its Apply baseline.
// An advance that belongs to another session fails attribution.
func sessionAdvance(baseline map[uint64]UTCSession, fields map[string]string) (UTCSession, uint64, uint64, error) {
	current, err := UTCDSessions(fields)
	if err != nil {
		return UTCSession{}, 0, 0, err
	}
	for epoch, now := range current {
		was, ok := baseline[epoch]
		if !ok {
			continue
		}
		if now.FromUDP < was.FromUDP || now.ToUDP < was.ToUDP ||
			now.FromTCP < was.FromTCP || now.ToTCP < was.ToTCP {
			return UTCSession{}, 0, 0, fmt.Errorf("UTC session %d counters rewound", epoch)
		}
		sent := (now.FromUDP - was.FromUDP) + (now.FromTCP - was.FromTCP)
		received := (now.ToUDP - was.ToUDP) + (now.ToTCP - was.ToTCP)
		if sent > 0 && received > 0 {
			return now, sent, received, nil
		}
	}
	return UTCSession{}, 0, 0, errors.New("UTC verify has no session-attributed counter advance")
}

// Block publishes a new generation whose deadline is already in the past, then
// requires the current driver to stop reporting the pair as APPLIED.
func (n *ExecNode) Block(ctx context.Context, p Plan, side int) (Receipt, error) {
	var empty Receipt
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := n.side(p, side); err != nil {
		return empty, err
	}
	d := n.driver()
	peer := peerPolicy(p, side)
	peer.ExpiresAt = 1
	_, err := d.PublishUTCPeer(ctx, p.Scope, n.Instance, n.NodeID, peer)
	if err != nil {
		return empty, err
	}
	boot, pid, err := n.observeProcess(d)
	if err != nil {
		return empty, err
	}
	raw, err := os.ReadFile(filepath.Join(n.ConfDir, "utc.status"))
	if err != nil {
		return empty, err
	}
	blocked, err := pairBlocked(string(raw), peer)
	if err != nil {
		return empty, err
	}
	if !blocked {
		return empty, errors.New("UTC block requires the driver to stop reporting APPLIED")
	}
	return n.receipt(p, side, "BLOCKED", boot, pid), nil
}

func pairBlocked(status string, peer driver.UTCPeerPolicy) (bool, error) {
	lines := strings.Split(strings.TrimSuffix(status, "\n"), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 11 || fields[0] != "policy" {
			continue
		}
		id, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || id != peer.PeerID {
			continue
		}
		generation, err := strconv.ParseUint(fields[3], 10, 64)
		if err != nil {
			return false, errors.New("UTC status generation invalid")
		}
		if generation != peer.PairGen {
			return false, errors.New("UTC block generation not observed")
		}
		for _, row := range lines {
			if strings.HasPrefix(row, "peer ") && strings.Contains(row, " APPLIED ") &&
				strings.HasPrefix(row, fmt.Sprintf("peer %d ", id)) {
				return false, nil
			}
		}
		return true, nil
	}
	return false, errors.New("UTC status has no policy row for the blocked pair")
}
