package utc

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type TrustedStaticIssuer struct {
	IssuerID         string
	PublicKey        ed25519.PublicKey
	Scopes           []string
	MinimumRevision  uint64
	MaxLeaseTTL      uint64
	MaxRevocationTTL uint64
}
type StaticBindingRule struct {
	BindingID         string `json:"binding_id"`
	Direction         string `json:"direction"`
	SubjectID         string `json:"subject_id"`
	SourceTransportID string `json:"source_transport_id"`
	ProviderID        string `json:"provider_id"`
	TargetPeerID      uint64 `json:"target_peer_id"`
	EndpointScope     string `json:"endpoint_scope"`
	QuotaAccountID    string `json:"quota_account_id"`
	QuotaOwner        string `json:"quota_owner"`
}
type StaticPairRule struct {
	Scope    string              `json:"scope"`
	PairID   uint64              `json:"pair_id"`
	Sides    [2]Side             `json:"sides"`
	Bindings []StaticBindingRule `json:"bindings"`
}
type StaticAuthorityConfig struct {
	LeaseDirectory      string
	RevocationDirectory string
	StateDirectory      string
	Issuers             map[string]TrustedStaticIssuer
	Pairs               []StaticPairRule
	Now                 func() time.Time
}
type StaticAuthority struct {
	leases, revocations, state string
	issuers                    map[string]TrustedStaticIssuer
	pairs                      map[string]StaticPairRule
	now                        func() time.Time
	clockMu                    sync.Mutex
	observedClock              uint64
}

func pairKey(scope string, pair uint64) string { return fmt.Sprintf("%s/%d", scope, pair) }
func staticFile(id string, suffix string) string {
	hash := sha256.Sum256([]byte(id))
	return hex.EncodeToString(hash[:]) + suffix
}

// StaticLeaseFile names signed public metadata without interpreting IDs as paths.
func StaticLeaseFile(scope, id string) string { return staticFile(scope+"/"+id, ".lease.json") }
func StaticRevocationFile(scope, key string) string {
	return staticFile(scope+"/"+key, ".revocations.json")
}
func NewStaticAuthority(config StaticAuthorityConfig) (*StaticAuthority, error) {
	for _, directory := range []string{config.LeaseDirectory, config.RevocationDirectory, config.StateDirectory} {
		if !filepath.IsAbs(directory) {
			return nil, errors.New("static UTC authority requires absolute directories")
		}
	}
	if filepath.Clean(config.StateDirectory) == filepath.Clean(config.LeaseDirectory) || filepath.Clean(config.StateDirectory) == filepath.Clean(config.RevocationDirectory) {
		return nil, errors.New("UTC authority watermarks need an independent state directory")
	}
	if len(config.Issuers) == 0 || len(config.Issuers) > 64 || len(config.Pairs) == 0 || len(config.Pairs) > 128 {
		return nil, errors.New("invalid static UTC trust capacity")
	}
	a := &StaticAuthority{leases: config.LeaseDirectory, revocations: config.RevocationDirectory, state: config.StateDirectory, issuers: map[string]TrustedStaticIssuer{}, pairs: map[string]StaticPairRule{}, now: config.Now}
	if a.now == nil {
		a.now = time.Now
	}
	for key, issuer := range config.Issuers {
		if !token(key) || !token(issuer.IssuerID) || len(issuer.PublicKey) != ed25519.PublicKeySize || issuer.MinimumRevision == 0 || issuer.MaxLeaseTTL == 0 || issuer.MaxLeaseTTL > 86400 || issuer.MaxRevocationTTL == 0 || issuer.MaxRevocationTTL > 3600 || len(issuer.Scopes) == 0 || len(issuer.Scopes) > 128 {
			return nil, errors.New("invalid static UTC issuer policy")
		}
		seen := map[string]bool{}
		for _, scope := range issuer.Scopes {
			if !token(scope) || seen[scope] {
				return nil, errors.New("invalid issuer scope")
			}
			seen[scope] = true
		}
		issuer.PublicKey = append(ed25519.PublicKey(nil), issuer.PublicKey...)
		issuer.Scopes = append([]string(nil), issuer.Scopes...)
		a.issuers[key] = issuer
	}
	for _, rule := range config.Pairs {
		sample := Plan{OperationID: "schema", Scope: rule.Scope, PairID: rule.PairID, Generation: 1, ValidUntil: 1, LeaseRefs: []string{"schema"}, Sides: rule.Sides}
		sample.Manifest = sample.Digest()
		if sample.validate() != nil || len(rule.Bindings) < 2 || len(rule.Bindings) > 8 {
			return nil, errors.New("invalid trusted UTC pair binding")
		}
		key := pairKey(rule.Scope, rule.PairID)
		if _, ok := a.pairs[key]; ok {
			return nil, errors.New("duplicate trusted pair")
		}
		directions := map[string]bool{}
		claims := map[string]bool{}
		for _, binding := range rule.Bindings {
			if !validDirection(binding.Direction) || !canonicalHex(binding.EndpointScope, 32) || (binding.TargetPeerID != rule.Sides[0].NodeID && binding.TargetPeerID != rule.Sides[1].NodeID) {
				return nil, errors.New("invalid trusted binding scope")
			}
			for _, id := range []string{binding.BindingID, binding.SubjectID, binding.SourceTransportID, binding.ProviderID, binding.QuotaAccountID, binding.QuotaOwner} {
				if !token(id) {
					return nil, errors.New("invalid trusted binding identity")
				}
			}
			claim := binding.BindingID + "/" + binding.Direction
			if claims[claim] {
				return nil, errors.New("duplicate trusted binding claim")
			}
			claims[claim] = true
			directions[binding.Direction] = true
		}
		if !directions["a-to-b"] || !directions["b-to-a"] {
			return nil, errors.New("static UTC authorization requires both directions")
		}
		rule.Bindings = append([]StaticBindingRule(nil), rule.Bindings...)
		a.pairs[key] = rule
	}
	if err := os.MkdirAll(a.state, 0700); err != nil {
		return nil, err
	}
	if err := checkStaticStateDirectory(a.state); err != nil {
		return nil, err
	}
	return a, nil
}
func (a *StaticAuthority) clock() (uint64, error) {
	a.clockMu.Lock()
	defer a.clockMu.Unlock()
	now := a.now().Unix()
	if now <= 0 {
		return 0, errors.New("trusted UTC clock unavailable")
	}
	if uint64(now) < a.observedClock {
		return 0, errors.New("UTC authority clock rewind")
	}
	a.observedClock = uint64(now)
	return uint64(now), nil
}
func readCanonicalPrivate(path string, out any, limit int64, requireOwner bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > limit || requireOwner && !staticOwned(info) {
		return errors.New("UTC authorization metadata is not a bounded private file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, actual) {
		return errors.New("UTC metadata changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > limit {
		return errors.New("UTC authorization metadata too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("UTC authorization metadata has trailing data")
	}
	canonical, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSuffix(raw, []byte("\n")), canonical) {
		return errors.New("UTC authorization metadata is not canonical JSON")
	}
	return nil
}

type staticFloor struct {
	Version         uint32   `json:"version"`
	KeyID           string   `json:"key_id"`
	Scope           string   `json:"scope"`
	Revision        uint64   `json:"revision"`
	Digest          string   `json:"digest"`
	ClockFloor      uint64   `json:"clock_floor"`
	Checksum        string   `json:"checksum,omitempty"`
	RevokedLeaseIDs []string `json:"revoked_lease_ids"`
}

func floorChecksum(floor staticFloor) string {
	floor.Checksum = ""
	raw, _ := json.Marshal(floor)
	sum := sha256.Sum256(append([]byte("mynet.utc.revocation-floor.v1\x00"), raw...))
	return hex.EncodeToString(sum[:])
}
func (a *StaticAuthority) floorPath(scope, key string) string {
	return filepath.Join(a.state, staticFile(scope+"/"+key, ".floor.json"))
}
func (a *StaticAuthority) readFloor(scope, key string, issuer TrustedStaticIssuer) (staticFloor, error) {
	var floor staticFloor
	if err := readCanonicalPrivate(a.floorPath(scope, key), &floor, 131072, true); err != nil {
		return floor, fmt.Errorf("UTC revocation floor unavailable; explicit initialization/recovery required: %w", err)
	}
	if floor.Version != 1 || floor.KeyID != key || floor.Scope != scope || floor.Revision < issuer.MinimumRevision || floor.ClockFloor == 0 || !canonicalHex(floor.Digest, 32) || floor.Checksum != floorChecksum(floor) {
		return floor, errors.New("UTC revocation floor integrity failure")
	}
	if len(floor.RevokedLeaseIDs) > 1024 {
		return floor, errors.New("UTC remembered revocation capacity exceeded")
	}
	for i, id := range floor.RevokedLeaseIDs {
		if !token(id) || i > 0 && floor.RevokedLeaseIDs[i-1] >= id {
			return floor, errors.New("invalid remembered revocations")
		}
	}
	return floor, nil
}
func (a *StaticAuthority) saveFloor(floor staticFloor) error {
	floor.Checksum = floorChecksum(floor)
	raw, err := json.Marshal(floor)
	if err != nil {
		return err
	}
	return writePrivate(a.floorPath(floor.Scope, floor.KeyID), raw)
}
func (a *StaticAuthority) snapshot(scope, key string, issuer TrustedStaticIssuer, now uint64) (StaticRevocations, string, error) {
	var snapshot StaticRevocations
	if err := readCanonicalPrivate(filepath.Join(a.revocations, StaticRevocationFile(scope, key)), &snapshot, 131072, false); err != nil {
		return snapshot, "", err
	}
	payload, err := StaticRevocationSigningBytes(snapshot)
	if err != nil || snapshot.KeyID != key || snapshot.Scope != scope || snapshot.IssuerID != issuer.IssuerID || !verifyStaticSignature(issuer.PublicKey, payload, snapshot.Signature) {
		return snapshot, "", errors.New("invalid signed UTC revocation snapshot")
	}
	if snapshot.IssuedAt > now || now >= snapshot.ExpiresAt || snapshot.ExpiresAt-snapshot.IssuedAt > issuer.MaxRevocationTTL || snapshot.Revision < issuer.MinimumRevision {
		return snapshot, "", errors.New("UTC revocation snapshot stale or outside issuer policy")
	}
	digest := sha256.Sum256(payload)
	return snapshot, hex.EncodeToString(digest[:]), nil
}

// InstallRevocationFloor is an explicit trusted provisioning operation. Validate
// never calls it: missing history cannot silently reset an issuer's watermark.
func (a *StaticAuthority) InstallRevocationFloor(ctx context.Context, scope, key string, expectedRevision uint64) error {
	issuer, ok := a.issuers[key]
	if !ok {
		return errors.New("unknown UTC signing key")
	}
	allowed := false
	for _, candidate := range issuer.Scopes {
		allowed = allowed || candidate == scope
	}
	if !token(scope) || !allowed {
		return errors.New("UTC issuer scope denied")
	}
	if err := checkStaticStateDirectory(a.state); err != nil {
		return err
	}
	unlock, err := (DiskStore{Directory: a.state}).Lock(ctx, scope+"/"+key, 0)
	if err != nil {
		return err
	}
	defer unlock()
	now, err := a.clock()
	if err != nil {
		return err
	}
	snapshot, digest, err := a.snapshot(scope, key, issuer, now)
	if err != nil {
		return err
	}
	if snapshot.Revision != expectedRevision {
		return errors.New("UTC revocation provisioning revision mismatch")
	}
	remembered := append([]string(nil), snapshot.RevokedLeaseIDs...)
	if _, err := os.Lstat(a.floorPath(scope, key)); err == nil {
		old, err := a.readFloor(scope, key, issuer)
		if err != nil {
			return err
		}
		if now < old.ClockFloor || snapshot.Revision < old.Revision || snapshot.Revision == old.Revision && digest != old.Digest {
			return errors.New("UTC revocation provisioning rewind/conflict")
		}
		remembered, err = mergeRevoked(old.RevokedLeaseIDs, remembered)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.saveFloor(staticFloor{Version: 1, KeyID: key, Scope: scope, Revision: snapshot.Revision, Digest: digest, ClockFloor: now, RevokedLeaseIDs: remembered})
}

type checkedStaticRevocations struct {
	snapshot   StaticRevocations
	denied     []string
	observedAt uint64
}

func mergeRevoked(old, current []string) ([]string, error) {
	result := append(append([]string(nil), old...), current...)
	sort.Strings(result)
	merged := result[:0]
	for _, id := range result {
		if len(merged) == 0 || merged[len(merged)-1] != id {
			merged = append(merged, id)
		}
	}
	if len(merged) > 1024 {
		return nil, errors.New("UTC remembered revocation capacity exceeded; rotate issuer key with trusted recovery")
	}
	return merged, nil
}
func (a *StaticAuthority) currentRevocations(ctx context.Context, scope, key string, issuer TrustedStaticIssuer) (checkedStaticRevocations, error) {
	empty := checkedStaticRevocations{}
	if err := checkStaticStateDirectory(a.state); err != nil {
		return empty, err
	}
	unlock, err := (DiskStore{Directory: a.state}).Lock(ctx, scope+"/"+key, 0)
	if err != nil {
		return empty, err
	}
	defer unlock()
	// A timestamp captured before waiting for the lock can look like a rewind.
	now, err := a.clock()
	if err != nil {
		return empty, err
	}
	floor, err := a.readFloor(scope, key, issuer)
	if err != nil {
		return empty, err
	}
	if now < floor.ClockFloor {
		return empty, errors.New("UTC authority clock rewind")
	}
	floor.ClockFloor = now
	if err := a.saveFloor(floor); err != nil {
		return empty, err
	}
	snapshot, digest, err := a.snapshot(scope, key, issuer, now)
	if err != nil {
		return empty, err
	}
	if snapshot.Revision < floor.Revision || snapshot.Revision == floor.Revision && digest != floor.Digest {
		return empty, errors.New("UTC revocation revision rewind/conflict")
	}
	remembered, err := mergeRevoked(floor.RevokedLeaseIDs, snapshot.RevokedLeaseIDs)
	if err != nil {
		return empty, err
	}
	if snapshot.Revision > floor.Revision || len(remembered) != len(floor.RevokedLeaseIDs) {
		floor.Revision = snapshot.Revision
		floor.Digest = digest
		floor.RevokedLeaseIDs = remembered
		if err := a.saveFloor(floor); err != nil {
			return empty, err
		}
	}
	return checkedStaticRevocations{snapshot: snapshot, denied: remembered, observedAt: now}, ctx.Err()
}

func bindingMatches(rule StaticBindingRule, lease StaticLease) bool {
	return rule.BindingID == lease.BindingID && rule.Direction == lease.Direction && rule.SubjectID == lease.SubjectID && rule.SourceTransportID == lease.SourceTransportID && rule.ProviderID == lease.ProviderID && rule.TargetPeerID == lease.TargetPeerID && rule.EndpointScope == lease.EndpointScope && rule.QuotaAccountID == lease.QuotaAccountID && rule.QuotaOwner == lease.QuotaOwner
}

// Validate verifies actual Ed25519 leases, trusted binding identities and signed
// revocations. It does not claim that the data service has enforced TLS/quota.
func (a *StaticAuthority) Validate(ctx context.Context, plan Plan) (result error) {
	var observed uint64
	validThrough := plan.ValidUntil
	checkedKeys := map[string]bool{}
	defer func() {
		if result == nil || observed == 0 {
			return
		}
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		for key := range checkedKeys {
			if err := a.rememberRejectedClock(recovery, plan.Scope, key, observed); err != nil {
				result = errors.Join(result, err)
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := plan.validate(); err != nil {
		return err
	}
	rule, ok := a.pairs[pairKey(plan.Scope, plan.PairID)]
	if !ok || rule.Sides != plan.Sides || len(rule.Bindings) != len(plan.LeaseRefs) {
		return errors.New("UTC plan outside trusted binding scope")
	}
	covered := map[string]bool{}
	for _, ref := range plan.LeaseRefs {
		var lease StaticLease
		if err := readCanonicalPrivate(filepath.Join(a.leases, StaticLeaseFile(plan.Scope, ref)), &lease, 16384, false); err != nil {
			return err
		}
		issuer, ok := a.issuers[lease.KeyID]
		if !ok {
			return errors.New("untrusted UTC signing key")
		}
		payload, err := StaticLeaseSigningBytes(lease)
		if err != nil || issuer.IssuerID != lease.IssuerID || !verifyStaticSignature(issuer.PublicKey, payload, lease.Signature) {
			return errors.New("invalid signed UTC lease")
		}
		allowed := false
		for _, scope := range issuer.Scopes {
			allowed = allowed || scope == plan.Scope
		}
		if !allowed {
			return errors.New("UTC issuer scope denied")
		}
		checked, err := a.currentRevocations(ctx, plan.Scope, lease.KeyID, issuer)
		if err != nil {
			return err
		}
		revoked, observedAt := checked.snapshot, checked.observedAt
		now, err := a.clock()
		if err != nil {
			return err
		}
		checkedKeys[lease.KeyID] = true
		if now > observed {
			observed = now
		}
		if revoked.ExpiresAt < validThrough {
			validThrough = revoked.ExpiresAt
		}
		if now < observedAt || now >= revoked.ExpiresAt {
			return errors.New("UTC authority clock/snapshot changed during validation")
		}
		if lease.Scope != plan.Scope || lease.LeaseID != ref || lease.PairID != plan.PairID || lease.PairGen != plan.Generation || lease.Manifest != plan.Manifest || lease.IssuedAt > now || lease.NotBefore > now || now >= lease.ExpiresAt || now >= plan.ValidUntil || plan.ValidUntil > lease.ExpiresAt || lease.ExpiresAt-lease.IssuedAt > issuer.MaxLeaseTTL || lease.RevocationEpoch > revoked.Revision {
			return errors.New("UTC lease time/pair/manifest mismatch")
		}
		index := sort.SearchStrings(checked.denied, lease.LeaseID)
		if index < len(checked.denied) && checked.denied[index] == lease.LeaseID {
			return errors.New("UTC lease revoked")
		}
		claim := lease.BindingID + "/" + lease.Direction
		matched := false
		for _, binding := range rule.Bindings {
			matched = matched || bindingMatches(binding, lease)
		}
		if !matched || covered[claim] {
			return errors.New("UTC lease binding mismatch or duplicate direction")
		}
		covered[claim] = true
	}
	if len(covered) != len(rule.Bindings) {
		return errors.New("UTC lease directions incomplete")
	}
	finalNow, err := a.clock()
	if err != nil {
		return err
	}
	if finalNow > observed {
		observed = finalNow
	}
	if finalNow >= validThrough {
		return errors.New("UTC authorization expired during validation")
	}
	return ctx.Err()
}

// Preserve a time at which an authorization was observed to be invalid. The
// error path must not forget that expiry across a process restart/clock rewind.
func (a *StaticAuthority) rememberRejectedClock(ctx context.Context, scope, key string, observed uint64) error {
	if err := checkStaticStateDirectory(a.state); err != nil {
		return err
	}
	unlock, err := (DiskStore{Directory: a.state}).Lock(ctx, scope+"/"+key, 0)
	if err != nil {
		return err
	}
	defer unlock()
	floor, err := a.readFloor(scope, key, a.issuers[key])
	if err != nil {
		return err
	}
	if floor.ClockFloor < observed {
		floor.ClockFloor = observed
	}
	return a.saveFloor(floor)
}
