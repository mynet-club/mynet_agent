package hub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"
)

// Control wire constants from mynet_hub_protocol.h.
const (
	reasonOK                 = 0
	reasonFenceRequired      = 9
	controlModeAS            = 1
	controlModeAD            = 2
	controlReasonTLV         = 1
	controlGrantTLV          = 2
	controlPlacementEpochTLV = 3
	controlEdgeTLV           = 4
	controlShardTLV          = 5
	controlReactorTLV        = 6
	controlBindingTLV        = 7
)

var reasonNames = map[uint16]string{
	0: "OK", 1: "BUSY", 2: "ALREADY_ACTIVE", 3: "NO_PERMIT", 4: "NOT_BEFORE",
	5: "EXPIRED", 6: "REVOKED", 7: "VERSION_REWIND", 8: "ROLLBACK_DETECTED",
	9: "FENCE_REQUIRED", 10: "OWNER_MISMATCH", 11: "EPOCH_CONFLICT",
	12: "BINDING_MISMATCH", 13: "QUOTA_EXHAUSTED", 14: "ADMISSION_TIMEOUT",
	15: "UNCERTAIN", 16: "STATE_CORRUPT", 17: "UNSUPPORTED",
	18: "INVALID_FRAME", 19: "RETRYABLE_UNAVAILABLE",
}

// ReasonError reports a non-OK control response. The hub rejected the request;
// nothing was granted.
type ReasonError struct {
	Reason uint16
}

func (e *ReasonError) Error() string {
	name, ok := reasonNames[e.Reason]
	if !ok {
		name = fmt.Sprintf("UNKNOWN(%d)", e.Reason)
	}
	return fmt.Sprintf("utc-hub control refused: %s", name)
}

// Config identifies one control client. Scope, TransportID and AccountID are
// sent on every request; the hub enforces the account allowlist. IssuerPublicKey
// is the trust anchor: every grant and TargetBinding must carry a valid
// Ed25519 signature from that key.
type Config struct {
	Address         string
	Scope           string
	TransportID     string
	AccountID       string
	StreamID        uint32
	Timeout         time.Duration
	IssuerPublicKey ed25519.PublicKey
}

// Placement is the hub-assigned owner generation returned on success.
type Placement struct {
	Epoch     uint64
	EdgeID    uint32
	ShardID   uint32
	ReactorID uint32
}

// Fence is the evidence the old placement owner reports when the hub moves the
// pair to another Edge. The old placement coordinates come from Prior; these
// fields describe the fence itself.
type Fence struct {
	ID         uint64
	GateEpoch  uint64
	JournalLSN uint64
	BootNonce  [16]byte
	Receipts   [32]byte
	Meter      [32]byte
}

// Prior is the current epoch/placement state, normally read back from the last
// published projection or its persisted summary.
type Prior struct {
	PairGen    uint64
	LeaseEpoch uint64
	Placement  Placement
}

// Request is one ASSIGN or RENEW control message.
type Request struct {
	Renew      bool
	PairID     uint64
	Side       uint8
	Mode       uint8
	Failover   bool
	PairGen    uint64
	LeaseEpoch uint64
	Rotate     bool
	Fence      *Fence
	Prior      Placement

	Scope       string
	TransportID string
	AccountID   string
	Nonce       uint64
}

// Response is one accepted control reply. Grant/Binding carry the decoded,
// frozen records; raw keeps their exact signed bytes.
type Response struct {
	Reason    uint16
	Placement Placement
	Grant     Grant
	Binding   Binding
	Verified  bool
}

// Client exchanges single control frames over TCP. utc/2 has no TLS layer in
// the current hub; transport security and authentication belong to the
// deployment (tunnel, mTLS proxy) and are not invented here.
type Client struct {
	config Config
}

func NewClient(config Config) (*Client, error) {
	if config.Address == "" {
		return nil, errors.New("utc-hub address is required")
	}
	if !token(config.Scope) || !token(config.TransportID) || !token(config.AccountID) {
		return nil, errors.New("utc-hub scope/transport/account must be canonical tokens")
	}
	if config.StreamID == 0 {
		config.StreamID = 1
	}
	if config.Timeout <= 0 {
		config.Timeout = 5 * time.Second
	}
	if len(config.IssuerPublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("utc-hub issuer public key is required")
	}
	return &Client{config: config}, nil
}

// transportError marks a failure before any verified control reply was
// decoded. Retrying the identical request is safe because the hub idempotency
// key is stable; reply validation failures are never retried.
type transportError struct{ err error }

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

func (c *Client) exchangeRequest(ctx context.Context, request Request) (Response, error) {
	var response Response
	payload, err := c.encodeRequest(request)
	if err != nil {
		return response, err
	}
	frame, err := encodeControlFrame(c.config.StreamID, payload)
	if err != nil {
		return response, err
	}
	dialer := &net.Dialer{Timeout: c.config.Timeout}
	connection, err := dialer.DialContext(ctx, "tcp", c.config.Address)
	if err != nil {
		return response, &transportError{fmt.Errorf("utc-hub dial: %w", err)}
	}
	defer connection.Close()
	deadline := time.Now().Add(c.config.Timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			connection.Close()
		case <-done:
		}
	}()
	if _, err := connection.Write(frame); err != nil {
		return response, &transportError{fmt.Errorf("utc-hub write: %w", err)}
	}
	reply, err := readControlFrame(connection)
	if err != nil {
		return response, &transportError{fmt.Errorf("utc-hub read: %w", err)}
	}
	if reply.StreamID != c.config.StreamID {
		return response, fmt.Errorf("utc-hub reply stream %d does not match request", reply.StreamID)
	}
	return c.decodeResponse(reply.Payload, request)
}

func (c *Client) encodeRequest(request Request) ([]byte, error) {
	scope := request.Scope
	if scope == "" {
		scope = c.config.Scope
	}
	transport := request.TransportID
	if transport == "" {
		transport = c.config.TransportID
	}
	account := request.AccountID
	if account == "" {
		account = c.config.AccountID
	}
	if scope != c.config.Scope || transport != c.config.TransportID || account != c.config.AccountID {
		return nil, errors.New("control request may not override the configured identity")
	}
	if request.PairID == 0 || request.Side > 1 ||
		(request.Mode != controlModeAS && request.Mode != controlModeAD) {
		return nil, errors.New("control request identity is invalid")
	}
	if request.Renew && (request.PairGen == 0 || request.LeaseEpoch == 0) {
		return nil, errors.New("renew requires pair_gen and lease_epoch")
	}
	if request.Fence != nil && (!request.Failover || !request.Renew) {
		return nil, errors.New("fence evidence requires a failover renewal")
	}
	if request.Failover {
		if request.Fence == nil {
			return nil, errors.New("failover requires fence evidence")
		}
		if request.Prior.Epoch == 0 || request.Prior.EdgeID == 0 {
			return nil, errors.New("failover requires the old placement identity")
		}
		if request.Fence.ID == 0 || request.Fence.GateEpoch == 0 || request.Fence.JournalLSN == 0 {
			return nil, errors.New("fence id/gate/journal LSN must be non-zero")
		}
		if !digestNonZero(request.Fence.BootNonce[:]) ||
			!digestNonZero(request.Fence.Receipts[:]) ||
			!digestNonZero(request.Fence.Meter[:]) {
			return nil, errors.New("fence evidence digests must be non-zero")
		}
	}
	nonce := request.Nonce
	if nonce == 0 {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, fmt.Errorf("control nonce: %w", err)
		}
		nonce = binary.BigEndian.Uint64(raw[:])
		if nonce == 0 {
			nonce = 1
		}
	}
	writer := newTLVWriter(256)
	op := uint8(1)
	if request.Renew {
		op = 2
	}
	steps := []error{
		writer.addU8(1, op),
		writer.add(2, []byte(scope)),
		writer.addU64(3, request.PairID),
		writer.addU8(4, request.Side),
		writer.addU8(5, request.Mode),
	}
	if request.Failover {
		steps = append(steps, writer.addU8(6, 1))
	}
	steps = append(steps,
		writer.add(7, []byte(transport)),
		writer.add(8, []byte(account)),
		writer.addU64(9, nonce),
	)
	if request.Renew {
		steps = append(steps,
			writer.addU64(10, request.PairGen),
			writer.addU64(11, request.LeaseEpoch),
		)
		if request.Rotate {
			steps = append(steps, writer.addU8(12, 1))
		}
	}
	if request.Failover {
		fence := request.Fence
		steps = append(steps,
			writer.addU64(13, request.Prior.Epoch),
			writer.addU32(14, request.Prior.EdgeID),
			writer.addU32(15, request.Prior.ShardID),
			writer.addU64(16, fence.ID),
			writer.addU64(17, fence.GateEpoch),
			writer.addU64(18, fence.JournalLSN),
			writer.add(19, fence.BootNonce[:]),
			writer.add(20, fence.Receipts[:]),
			writer.add(21, fence.Meter[:]),
		)
	}
	for _, step := range steps {
		if step != nil {
			return nil, step
		}
	}
	return writer.buffer, nil
}

func (c *Client) decodeResponse(payload []byte, request Request) (Response, error) {
	var response Response
	entries, err := parseTLVs(payload)
	if err != nil {
		return response, err
	}
	if len(entries) == 0 || entries[0].Type != controlReasonTLV || len(entries[0].Value) != 2 {
		return response, errors.New("utc-hub reply has no reason")
	}
	response.Reason = binary.BigEndian.Uint16(entries[0].Value)
	if response.Reason != reasonOK {
		if len(entries) != 1 {
			return response, errors.New("utc-hub refusal carries payload fields")
		}
		return response, &ReasonError{Reason: response.Reason}
	}
	if len(entries) != 7 {
		return response, fmt.Errorf("utc-hub success reply has %d fields, want 7", len(entries))
	}
	want := []uint16{
		controlGrantTLV, controlPlacementEpochTLV, controlEdgeTLV,
		controlShardTLV, controlReactorTLV, controlBindingTLV,
	}
	if entries[1].Type != want[0] || len(entries[1].Value) != grantBytes ||
		entries[2].Type != want[1] || len(entries[2].Value) != 8 ||
		entries[3].Type != want[2] || len(entries[3].Value) != 4 ||
		entries[4].Type != want[3] || len(entries[4].Value) != 4 ||
		entries[5].Type != want[4] || len(entries[5].Value) != 4 ||
		entries[6].Type != want[5] || len(entries[6].Value) != bindingBytes {
		return response, errors.New("utc-hub success reply layout is invalid")
	}
	response.Placement = Placement{
		Epoch:     binary.BigEndian.Uint64(entries[2].Value),
		EdgeID:    binary.BigEndian.Uint32(entries[3].Value),
		ShardID:   binary.BigEndian.Uint32(entries[4].Value),
		ReactorID: binary.BigEndian.Uint32(entries[5].Value),
	}
	grant, err := decodeGrant(entries[1].Value)
	if err != nil {
		return response, err
	}
	binding, err := decodeBinding(entries[6].Value)
	if err != nil {
		return response, err
	}
	response.Grant = grant
	response.Binding = binding
	if err := checkBindingAgainstGrant(grant, binding); err != nil {
		return response, err
	}
	wantKind := uint8(bindingKindAS)
	if request.Mode == controlModeAD {
		wantKind = bindingKindAD
	}
	if binding.Kind != wantKind {
		return response, errors.New("utc-hub TargetBinding kind does not match the requested mode")
	}
	if err := checkGrantAgainstRequest(grant, response.Placement, request, c.config); err != nil {
		return response, err
	}
	if !verifyGrantSignature(grant, c.config.IssuerPublicKey) ||
		!verifyBindingSignature(binding, c.config.IssuerPublicKey) {
		return response, errors.New("utc-hub grant/binding signature is invalid")
	}
	response.Verified = true
	return response, nil
}

func checkGrantAgainstRequest(grant Grant, placement Placement, request Request, config Config) error {
	if grant.Scope != config.Scope || grant.ClientTransportID != config.TransportID ||
		grant.PairID != request.PairID || grant.Side != request.Side ||
		grant.PlacementEpoch != placement.Epoch {
		return errors.New("utc-hub grant does not match the request identity")
	}
	edge, ok := parseGrantID(grant.EdgeID)
	if !ok || edge != placement.EdgeID {
		return errors.New("utc-hub grant edge does not match the placement")
	}
	shard, ok := parseGrantID(grant.ShardID)
	if !ok || shard != placement.ShardID {
		return errors.New("utc-hub grant shard does not match the placement")
	}
	reactor, ok := parseGrantID(grant.ReactorID)
	if !ok || reactor != placement.ReactorID {
		return errors.New("utc-hub grant reactor does not match the placement")
	}
	if grant.QuotaAccountID != config.AccountID {
		return errors.New("utc-hub grant account does not match the request")
	}
	switch {
	case !request.Renew:
		if grant.PairGen != 1 || grant.LeaseEpoch != 1 {
			return errors.New("utc-hub assignment did not start at generation 1")
		}
	case request.Rotate:
		if request.PairGen == ^uint64(0) || request.LeaseEpoch == ^uint64(0) ||
			grant.PairGen != request.PairGen+1 || grant.LeaseEpoch != request.LeaseEpoch+1 {
			return errors.New("utc-hub rotation did not advance both epochs")
		}
	default:
		if grant.PairGen != request.PairGen || grant.LeaseEpoch != request.LeaseEpoch {
			return errors.New("utc-hub renewal changed epochs without rotate")
		}
		if request.Prior.Epoch != 0 && placement != request.Prior {
			return errors.New("utc-hub renewal changed the placement without rotate")
		}
	}
	if request.Failover && grant.PlacementEpoch <= request.Prior.Epoch {
		return errors.New("utc-hub failover did not advance the placement epoch")
	}
	return nil
}

// grantDigest is SHA-256 over the complete signed 826-byte grant record.
func grantDigest(grant Grant) [32]byte { return sha256.Sum256(grant.raw) }

func bindingDigest(binding Binding) [32]byte { return sha256.Sum256(binding.raw) }

func hexDigest(digest [32]byte) string { return hex.EncodeToString(digest[:]) }
