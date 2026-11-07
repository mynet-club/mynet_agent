package utc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Minimal client for utcd's local control socket. It only reads status; it never
// installs trust, changes configuration or touches VPN material. Payloads are
// bounded key=value lines as emitted by src/dataplane/instance.c.
const (
	utcdControlMagic   = 0x4d4e4354
	utcdControlVersion = 1
	utcdControlStatus  = 0x1001
	utcdControlReply   = 0x1100
	utcdControlError   = 0x1101
	utcdPayloadLimit   = 4096
)

// UTCDStatus returns the current status fields of a running utcd.
// An unreachable socket is an error, never an implicit "lab" or "ready".
func UTCDStatus(ctx context.Context, socketPath string) (map[string]string, error) {
	if socketPath == "" || len(socketPath) > 103 || socketPath[0] != '/' {
		return nil, errors.New("UTC control socket must be an absolute path")
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("UTC control connect: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	}
	header := make([]byte, 16)
	binary.BigEndian.PutUint32(header[0:4], utcdControlMagic)
	binary.BigEndian.PutUint16(header[4:6], utcdControlVersion)
	binary.BigEndian.PutUint16(header[6:8], utcdControlStatus)
	binary.BigEndian.PutUint32(header[8:12], 1)
	if _, err := conn.Write(header); err != nil {
		return nil, fmt.Errorf("UTC control write: %w", err)
	}
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, fmt.Errorf("UTC control read: %w", err)
	}
	if binary.BigEndian.Uint32(header[0:4]) != utcdControlMagic ||
		binary.BigEndian.Uint16(header[4:6]) != utcdControlVersion ||
		binary.BigEndian.Uint32(header[8:12]) != 1 {
		return nil, errors.New("UTC control response header mismatch")
	}
	kind := binary.BigEndian.Uint16(header[6:8])
	length := binary.BigEndian.Uint32(header[12:16])
	if length > utcdPayloadLimit {
		return nil, errors.New("UTC control payload too large")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, fmt.Errorf("UTC control payload: %w", err)
	}
	if kind == utcdControlError {
		return nil, fmt.Errorf("UTC control error: %s", strings.TrimSpace(string(payload)))
	}
	if kind != utcdControlReply {
		return nil, errors.New("UTC control unexpected response type")
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(payload), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, errors.New("UTC control malformed status line")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, errors.New("UTC control duplicate status field")
		}
		fields[key] = value
	}
	if fields["status"] != "running" {
		return nil, errors.New("UTC dataplane is not running")
	}
	return fields, nil
}

// UTCDSecurity reports the configured security mode. Formal coordination only
// accepts static-mtls; both lab modes are refused explicitly.
func UTCDSecurity(fields map[string]string) (string, error) {
	mode := fields["security"]
	switch mode {
	case "static-mtls":
		return mode, nil
	case "token-lab", "mtls-transport-lab":
		return mode, fmt.Errorf("UTC dataplane security %s is a lab mode, not formal PREPARED", mode)
	default:
		return mode, fmt.Errorf("UTC dataplane security %q is unsupported", mode)
	}
}

// UTCDCounter reads a non-negative diagnostic counter.
func UTCDCounter(fields map[string]string, name string) (uint64, error) {
	raw, ok := fields[name]
	if !ok {
		return 0, fmt.Errorf("UTC dataplane counter %s missing", name)
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("UTC dataplane counter %s invalid", name)
	}
	return value, nil
}

// UTCSession is one live session's byte legs keyed by its epoch. from_udp and
// to_udp cross the GNB UDP socket, from_tcp and to_tcp cross the session's TCP
// stream. Counters are per session incarnation, so an operation can claim its
// own traffic instead of instance-cumulative totals.
type UTCSession struct {
	Epoch   uint64
	FromUDP uint64
	ToUDP   uint64
	FromTCP uint64
	ToTCP   uint64
}

// UTCDSessions strictly parses the per-session counter rows. A session row
// that is present but incomplete is an error, never a silent zero.
func UTCDSessions(fields map[string]string) (map[uint64]UTCSession, error) {
	sessions := map[uint64]UTCSession{}
	seen := map[uint64]uint8{}
	for key, raw := range fields {
		if !strings.HasPrefix(key, "session") || key == "sessions" {
			continue
		}
		parts := strings.SplitN(key, "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("UTC session field %q malformed", key)
		}
		slot, err := strconv.ParseUint(strings.TrimPrefix(parts[0], "session"), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("UTC session field %q malformed", key)
		}
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("UTC session field %q invalid", key)
		}
		entry := sessions[slot]
		var bit uint8
		switch parts[1] {
		case "epoch":
			entry.Epoch = value
			bit = 1
		case "from_udp":
			entry.FromUDP = value
			bit = 2
		case "to_udp":
			entry.ToUDP = value
			bit = 4
		case "from_tcp":
			entry.FromTCP = value
			bit = 8
		case "to_tcp":
			entry.ToTCP = value
			bit = 16
		default:
			return nil, fmt.Errorf("UTC session field %q unknown", key)
		}
		if seen[slot]&bit != 0 {
			return nil, fmt.Errorf("UTC session field %q duplicated", key)
		}
		seen[slot] |= bit
		sessions[slot] = entry
	}
	byEpoch := map[uint64]UTCSession{}
	for slot, entry := range sessions {
		if seen[slot] != 31 || entry.Epoch == 0 {
			return nil, fmt.Errorf("UTC session slot %d row is incomplete", slot)
		}
		if _, clash := byEpoch[entry.Epoch]; clash {
			return nil, fmt.Errorf("UTC session epoch %d duplicated", entry.Epoch)
		}
		byEpoch[entry.Epoch] = entry
	}
	return byEpoch, nil
}
