package hub

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	exchangeAttempts = 3
	exchangeBackoff  = 100 * time.Millisecond
)

// Identity is the local installation content written into the utc-v2 header.
// It must match the running GNB so the consumer can bind the projection to
// its own boot/PID; the hub response supplies only the per-pair fields.
type Identity struct {
	Scope        string
	Instance     string
	LocalID      uint64
	PeerID       uint64
	BootID       string
	PID          uint64
	Capabilities uint32
	ConfigGen    uint64
}

// Session binds one pair/side to a control client and turns accepted control
// replies into utc.conf content. It performs no background work: the caller
// decides when to assign, renew, recover or fail over.
type Session struct {
	Client   *Client
	Identity Identity
	PairID   uint64
	Side     uint8
	Mode     uint8
	Nonce    func() (uint64, error)
}

// Outcome is one accepted control reply plus the projection it produced.
// Changed reports whether the local projection differs from Prior; a same
// generation renewal that only extends the grant deadline is not publishable
// to the dataplane and leaves Changed false.
type Outcome struct {
	Response   Response
	Summary    Summary
	Projection Projection
	Text       []byte
	Changed    bool
}

// Summary is the durable, human-auditable digest of one control result.
type Summary struct {
	Scope             string `json:"scope"`
	Instance          string `json:"instance"`
	ConfigGen         uint64 `json:"config_gen"`
	TransportID       string `json:"transport_id"`
	AccountID         string `json:"account_id"`
	PairID            uint64 `json:"pair_id"`
	Side              uint8  `json:"side"`
	Mode              uint8  `json:"mode"`
	PairGen           uint64 `json:"pair_gen"`
	LeaseEpoch        uint64 `json:"lease_epoch"`
	PlacementEpoch    uint64 `json:"placement_epoch"`
	EdgeID            uint32 `json:"edge_id"`
	ShardID           uint32 `json:"shard_id"`
	ReactorID         uint32 `json:"reactor_id"`
	GrantID           string `json:"grant_id"`
	IssuerID          string `json:"issuer_id"`
	KeyID             string `json:"key_id"`
	ByteLimit         uint64 `json:"byte_limit"`
	IssuedAt          uint64 `json:"issued_at"`
	ValidUntil        uint64 `json:"valid_until"`
	BindingID         string `json:"binding_id"`
	BindingAddr       string `json:"binding_address"`
	BindingPort       uint16 `json:"binding_port"`
	BindingFamily     uint8  `json:"binding_family"`
	GrantDigest       string `json:"grant_sha256"`
	BindingDigest     string `json:"target_binding_sha256"`
	SignatureVerified bool   `json:"signature_verified"`
	RecoveryReason    string `json:"recovery_reason,omitempty"`
}

func (s *Session) validate() error {
	if s == nil || s.Client == nil {
		return errors.New("utc-hub session requires a client")
	}
	if s.PairID == 0 || s.Side > 1 || (s.Mode != controlModeAS && s.Mode != controlModeAD) {
		return errors.New("utc-hub session pair identity is invalid")
	}
	if err := s.Identity.Validate(); err != nil {
		return err
	}
	if s.Nonce == nil {
		s.Nonce = randomNonce
	}
	return nil
}

func randomNonce() (uint64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	nonce := binary.BigEndian.Uint64(raw[:])
	if nonce == 0 {
		nonce = 1
	}
	return nonce, nil
}

func (s *Session) nextNonce() (uint64, error) { return s.Nonce() }

// Assign requests a fresh placement. Prior must be zero.
func (s *Session) Assign(ctx context.Context) (Outcome, error) {
	return s.exchange(ctx, Prior{}, Request{PairID: s.PairID, Side: s.Side, Mode: s.Mode})
}

// Renew refreshes the current placement. rotate requests the next
// pair_gen/lease_epoch generation and is required whenever the driver reports
// the pair blocked, expired or needing a new generation.
func (s *Session) Renew(ctx context.Context, prior Prior, rotate bool) (Outcome, error) {
	if prior.PairGen == 0 || prior.LeaseEpoch == 0 || prior.Placement.Epoch == 0 {
		return Outcome{}, errors.New("renew requires a prior generation and placement")
	}
	return s.exchange(ctx, prior, Request{
		Renew: true, PairID: s.PairID, Side: s.Side, Mode: s.Mode,
		PairGen: prior.PairGen, LeaseEpoch: prior.LeaseEpoch, Rotate: rotate,
		Prior: prior.Placement,
	})
}

// Recover inspects utc.status and renews with rotate=1 when this pair is
// RECOVERED_BLOCKED, EXPIRED or REQUIRES_NEW_GENERATION. A healthy or
// unrelated status renews the current generation.
func (s *Session) Recover(ctx context.Context, prior Prior, status []byte) (Outcome, bool, error) {
	rotate, reason, err := RotationRequired(status, s.Identity.PeerID, s.PairID)
	if err != nil {
		return Outcome{}, false, err
	}
	outcome, err := s.Renew(ctx, prior, rotate)
	if err != nil {
		return outcome, rotate, err
	}
	outcome.Summary.RecoveryReason = reason
	return outcome, rotate, nil
}

// Failover reports the old placement's fence certificate and asks the hub for
// a replacement. The wire contract has no lease-only replacement: the request
// is a renewal with rotate=1 so pair_gen advances together with the placement
// epoch, which is what the utc-v2 consumer requires before unblocking an
// export peer.
func (s *Session) Failover(ctx context.Context, prior Prior, fence Fence) (Outcome, error) {
	if prior.PairGen == 0 || prior.LeaseEpoch == 0 || prior.Placement.Epoch == 0 {
		return Outcome{}, errors.New("failover requires the old placement state")
	}
	return s.exchange(ctx, prior, Request{
		Renew: true, PairID: s.PairID, Side: s.Side, Mode: s.Mode,
		PairGen: prior.PairGen, LeaseEpoch: prior.LeaseEpoch,
		Rotate: true, Failover: true, Fence: &fence, Prior: prior.Placement,
	})
}

// exchange sends one logical control request. Transport failures are retried
// with the same nonce (and the same fence evidence) so a lost reply hits the
// hub idempotency cache instead of failing on an already-consumed fence.
func (s *Session) exchange(ctx context.Context, prior Prior, request Request) (Outcome, error) {
	if err := s.validate(); err != nil {
		return Outcome{}, err
	}
	if request.Nonce == 0 {
		nonce, err := s.nextNonce()
		if err != nil {
			return Outcome{}, err
		}
		request.Nonce = nonce
	}
	var lastErr error
	for attempt := 0; attempt < exchangeAttempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * exchangeBackoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return Outcome{}, ctx.Err()
			case <-timer.C:
			}
		}
		response, err := s.Client.exchangeRequest(ctx, request)
		if err == nil {
			return s.outcome(prior, response)
		}
		lastErr = err
		var refused *ReasonError
		var transport *transportError
		if errors.As(err, &refused) || !errors.As(err, &transport) || ctx.Err() != nil {
			return Outcome{}, err
		}
	}
	return Outcome{}, lastErr
}

func (s *Session) outcome(prior Prior, response Response) (Outcome, error) {
	projection, text, err := BuildProjection(s.Identity, response)
	if err != nil {
		return Outcome{}, err
	}
	changed := prior.PairGen == 0 ||
		prior.PairGen != response.Grant.PairGen ||
		prior.LeaseEpoch != response.Grant.LeaseEpoch ||
		prior.Placement != response.Placement
	summary := response.Summary(s.Client.config.TransportID, s.Client.config.AccountID)
	summary.Instance = s.Identity.Instance
	summary.ConfigGen = s.Identity.ConfigGen
	summary.Mode = s.Mode
	return Outcome{
		Response:   response,
		Projection: projection,
		Text:       text,
		Changed:    changed,
		Summary:    summary,
	}, nil
}

// Prior returns the epoch/placement state to persist for the next renewal.
func (outcome Outcome) Prior() Prior {
	return Prior{
		PairGen:    outcome.Response.Grant.PairGen,
		LeaseEpoch: outcome.Response.Grant.LeaseEpoch,
		Placement:  outcome.Response.Placement,
	}
}

// BuildProjection converts one accepted control reply into the canonical
// utc-v2 projection. The export endpoint comes from the signed TargetBinding,
// and the binding digest (equal to the grant's target_binding_digest) is the
// per-peer manifest. Both generation values are taken from the grant.
func BuildProjection(identity Identity, response Response) (Projection, []byte, error) {
	peer, err := buildASPeer(identity, identity.PeerID, response)
	if err != nil {
		return Projection{}, nil, err
	}
	projection := projectionHeader(identity)
	projection.Peers = []Peer{peer}
	text, err := projection.Canonical()
	if err != nil {
		return Projection{}, nil, err
	}
	return projection, text, nil
}

func projectionHeader(identity Identity) Projection {
	return Projection{
		Scope:        identity.Scope,
		Instance:     identity.Instance,
		LocalID:      identity.LocalID,
		ConfigGen:    identity.ConfigGen,
		BootID:       identity.BootID,
		PID:          identity.PID,
		Capabilities: identity.Capabilities,
	}
}

// buildASPeer maps one accepted reply onto a peer row. Every value comes from
// the signed grant/binding; nothing is re-signed or invented.
func buildASPeer(identity Identity, peerID uint64, response Response) (Peer, error) {
	grant, binding := response.Grant, response.Binding
	if grant.Scope != identity.Scope {
		return Peer{}, errors.New("projection scope does not match the grant")
	}
	if binding.Kind != bindingKindAS {
		return Peer{}, errors.New("projection requires an A-S export binding")
	}
	address, family, err := exportAddress(binding)
	if err != nil {
		return Peer{}, err
	}
	return Peer{
		PeerID:         peerID,
		PairID:         grant.PairID,
		PairGen:        grant.PairGen,
		Mode:           1,
		LeaseEpoch:     grant.LeaseEpoch,
		PlacementEpoch: grant.PlacementEpoch,
		ExpiresAt:      grant.ValidUntil,
		Family:         family,
		Address:        address,
		Port:           binding.Port,
		ScopeID:        binding.AddressScopeID,
		Manifest:       hex.EncodeToString(grant.TargetBindingDigest[:]),
	}, nil
}

func exportAddress(binding Binding) ([16]byte, uint8, error) {
	var raw [16]byte
	address, err := netip.ParseAddr(binding.Address)
	if err != nil || address.Zone() != "" || address.Is4In6() ||
		address.IsUnspecified() || address.IsMulticast() {
		return raw, 0, errors.New("TargetBinding address is not a usable unicast endpoint")
	}
	if address.Is4() {
		value := address.As4()
		raw[10], raw[11] = 0xff, 0xff
		copy(raw[12:], value[:])
		return raw, 4, nil
	}
	return address.As16(), 6, nil
}

// Summary records the decoded grant/binding identity plus the placement.
func (response Response) Summary(transportID, accountID string) Summary {
	grant, binding := response.Grant, response.Binding
	return Summary{
		Scope: grant.Scope, TransportID: transportID, AccountID: accountID,
		PairID: grant.PairID, Side: grant.Side,
		PairGen: grant.PairGen, LeaseEpoch: grant.LeaseEpoch,
		PlacementEpoch: grant.PlacementEpoch,
		EdgeID:         response.Placement.EdgeID, ShardID: response.Placement.ShardID,
		ReactorID: response.Placement.ReactorID,
		GrantID:   grant.GrantID, IssuerID: grant.IssuerID, KeyID: grant.KeyID,
		ByteLimit: grant.ByteLimit, IssuedAt: grant.IssuedAt,
		ValidUntil: grant.ValidUntil,
		BindingID:  binding.BindingID, BindingAddr: binding.Address,
		BindingPort: binding.Port, BindingFamily: binding.Family,
		GrantDigest:       hexDigest(grantDigest(grant)),
		BindingDigest:     hexDigest(bindingDigest(binding)),
		SignatureVerified: response.Verified,
	}
}

// Write persists the summary (when summaryPath is non-empty) and then the
// projection. The order keeps the recorded high water ahead of the file the
// dataplane consumes; both writes are fsync+rename.
func (outcome Outcome) Write(confPath, summaryPath string) error {
	if len(outcome.Text) == 0 {
		return errors.New("outcome has no projection text")
	}
	if summaryPath != "" {
		raw, err := json.Marshal(outcome.Summary)
		if err != nil {
			return err
		}
		if err := WriteFileAtomic(summaryPath, raw); err != nil {
			return err
		}
	}
	if !outcome.Changed {
		// A same-generation renewal only extends the signed deadline. The
		// consumer rejects same epoch content changes, so keep the file as is.
		return nil
	}
	return WriteFileAtomic(confPath, outcome.Text)
}

// LoadSummary strictly reads back a persisted control result. A missing,
// oversized, non-private or malformed file fails closed: the caller must not
// renew from ambiguous history.
func LoadSummary(path string) (Summary, error) {
	var summary Summary
	info, err := os.Lstat(path)
	if err != nil {
		return summary, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 65536 {
		return summary, errors.New("utc-hub summary must be a bounded private file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return summary, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&summary); err != nil {
		return summary, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Summary{}, errors.New("utc-hub summary has trailing data")
	}
	if summary.Scope == "" || summary.ConfigGen == 0 || summary.PairID == 0 ||
		summary.PairGen == 0 || summary.LeaseEpoch == 0 ||
		summary.PlacementEpoch == 0 || summary.EdgeID == 0 {
		return Summary{}, errors.New("utc-hub summary identity is incomplete")
	}
	return summary, nil
}

// Prior rebuilds the renewal state from persisted history.
func (summary Summary) Prior() Prior {
	return Prior{
		PairGen:    summary.PairGen,
		LeaseEpoch: summary.LeaseEpoch,
		Placement: Placement{
			Epoch: summary.PlacementEpoch, EdgeID: summary.EdgeID,
			ShardID: summary.ShardID, ReactorID: summary.ReactorID,
		},
	}
}

// WriteFileAtomic replaces path with private content only after file and
// directory fsync succeed. A failed rename or fsync is an error even when the
// new content is already visible.
func WriteFileAtomic(path string, data []byte) error {
	if path == "" {
		return errors.New("output path is required")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".utc-hub-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr = directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// RotationRequired decides whether utc.status requires a new generation.
// RECOVERED_BLOCKED is a service-level restart recovery; EXPIRED and
// REQUIRES_NEW_GENERATION are per-peer block reasons. A missing or APPLIED
// peer row is not a rotation trigger.
func RotationRequired(status []byte, peerID, pairID uint64) (bool, string, error) {
	text := strings.TrimSuffix(string(status), "\n")
	if text == "" {
		return false, "", errors.New("utc.status is empty")
	}
	lines := strings.Split(text, "\n")
	header := strings.Fields(lines[0])
	if len(header) != 9 || header[0] != "status-v1" {
		return false, "", errors.New("utc.status header is invalid")
	}
	if header[7] == "RECOVERED_BLOCKED" {
		return true, header[7], nil
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] != "peer" {
			continue
		}
		id, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return false, "", errors.New("utc.status peer id is invalid")
		}
		pair, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			return false, "", errors.New("utc.status pair id is invalid")
		}
		if id != peerID || pair != pairID {
			continue
		}
		state, reason := fields[4], fields[5]
		rotate := state == "BLOCKED" &&
			(reason == "EXPIRED" || reason == "REQUIRES_NEW_GENERATION" ||
				reason == "RECOVERED_BLOCKED")
		return rotate, reason, nil
	}
	return false, "", nil
}

// Validate checks that an identity can produce a projection header. It is
// exported so callers can fail before dialing.
func (identity Identity) Validate() error {
	projection := Projection{
		Scope: identity.Scope, Instance: identity.Instance, LocalID: identity.LocalID,
		ConfigGen: identity.ConfigGen, BootID: identity.BootID, PID: identity.PID,
		Capabilities: identity.Capabilities,
	}
	_, err := projection.Canonical()
	if err != nil {
		return err
	}
	if identity.PeerID == 0 {
		return errors.New("projection peer id is required")
	}
	return nil
}
