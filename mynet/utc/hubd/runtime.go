package hubd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/utc/hub"
)

// Runtime owns the node's configured pairs. All control exchanges and
// projection publishes run serially from Run; a second Run on the same state
// directory is an operator error, not a supported concurrency mode. One pair
// failing never rewrites or blocks another pair's accepted row.
type Runtime struct {
	cfg      Config
	identity hub.Identity
	pairs    []*pairState

	configGen  uint64
	file       projectionState
	hadSummary bool

	// Now, Jitter and Logf are testability/observability seams. New installs
	// production defaults.
	Now    func() time.Time
	Jitter func(time.Duration) time.Duration
	Logf   func(format string, args ...any)
}

// pairState is the durable and in-memory state of one pair/side.
type pairState struct {
	cfg      PairConfig
	identity hub.Identity
	session  *hub.Session

	started            bool
	prior              hub.Prior
	grantValidUntil    uint64
	publishedExpiresAt uint64
	verified           *Verified
	lastPeer           *hub.Peer

	nextDue time.Time
	backoff time.Duration
}

// New validates the local wiring and dials nothing. Run performs the first
// load and exchange.
func New(cfg Config) (*Runtime, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	client, err := hub.NewClient(hub.Config{
		Address: cfg.HubAddress, Scope: cfg.Scope,
		TransportID: cfg.TransportID, AccountID: cfg.AccountID,
		StreamID: cfg.StreamID, Timeout: cfg.ClientTimeout,
		IssuerPublicKey: cfg.IssuerPublicKey,
	})
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{
		cfg:       cfg,
		identity:  cfg.Identity,
		configGen: cfg.Identity.ConfigGen,
		Now:       time.Now,
		Jitter:    defaultJitter,
	}
	for _, pairConfig := range cfg.Pairs {
		identity := cfg.Identity
		identity.PeerID = pairConfig.PeerID
		runtime.pairs = append(runtime.pairs, &pairState{
			cfg:      pairConfig,
			identity: identity,
			session: &hub.Session{
				Client: client, Identity: identity,
				PairID: pairConfig.PairID, Side: pairConfig.Side, Mode: pairConfig.Mode,
				LocalEndpoint: pairConfig.LocalEndpoint,
			},
			backoff: cfg.RetryMin,
		})
	}
	return runtime, nil
}

func defaultJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0
	}
	return time.Duration(binary.BigEndian.Uint64(raw[:]) % uint64(max+1))
}

// identityPoll bounds how long Run may sleep before noticing a GNB restart.
const identityPoll = 2 * time.Second

// Run loads durable state once, then renews every pair before expiry until ctx
// is cancelled. Corrupt or ambiguous state stops the runtime; transient control
// or GNB failures are retried per pair with bounded backoff.
func (r *Runtime) Run(ctx context.Context) error {
	backoff := r.cfg.RetryMin
	for {
		err := r.load()
		if err == nil {
			break
		}
		var state *stateError
		if errors.As(err, &state) {
			return err
		}
		r.logf("utc hub state load failed; retrying: %v", err)
		if err := waitContext(ctx, backoff); err != nil {
			return err
		}
		backoff = growBackoff(backoff, r.cfg.RetryMax)
	}
	backoff = r.cfg.RetryMin
	previousBoot, previousPID := "", uint64(0)
	for {
		if err := r.refreshIdentity(); err != nil {
			r.logf("utc hub identity refresh failed; retrying: %v", err)
			if err := waitContext(ctx, backoff); err != nil {
				return err
			}
			backoff = growBackoff(backoff, r.cfg.RetryMax)
			continue
		}
		backoff = r.cfg.RetryMin
		r.identityMoved(previousBoot, previousPID)
		previousBoot, previousPID = r.identity.BootID, r.identity.PID
		now := r.Now()
		for _, pair := range r.pairs {
			if pair.nextDue.After(now) {
				continue
			}
			err := r.stepPair(ctx, pair)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var state *stateError
			if errors.As(err, &state) {
				return err
			}
			if err != nil {
				r.logf("utc hub pair %d cycle failed; retrying: %v", pair.cfg.PairID, err)
				pair.nextDue = r.Now().Add(pair.backoff)
				pair.backoff = growBackoff(pair.backoff, r.cfg.RetryMax)
			} else {
				pair.backoff = r.cfg.RetryMin
				pair.nextDue = r.Now().Add(r.nextDelay(pair))
			}
		}
		wait := r.nextWait()
		if wait < r.cfg.RetryMin {
			wait = r.cfg.RetryMin
		}
		if wait > identityPoll {
			wait = identityPoll
		}
		if err := waitContext(ctx, wait); err != nil {
			return err
		}
	}
}

func growBackoff(current, maximum time.Duration) time.Duration {
	next := current * 2
	if next > maximum || next <= 0 {
		return maximum
	}
	return next
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *Runtime) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

func (r *Runtime) confPath() string { return filepath.Join(r.cfg.ConfDir, "utc.conf") }

// nextWait reports the delay until the earliest pair is due.
func (r *Runtime) nextWait() time.Duration {
	now := r.Now()
	var wait time.Duration
	for i, pair := range r.pairs {
		delay := pair.nextDue.Sub(now)
		if delay < 0 {
			delay = 0
		}
		if i == 0 || delay < wait {
			wait = delay
		}
	}
	return wait
}

// identityMoved forces an immediate cycle when the GNB process changed, so a
// restart-only recovery does not wait for the next renewal window.
func (r *Runtime) identityMoved(previousBoot string, previousPID uint64) bool {
	if previousBoot == "" {
		return false
	}
	if r.identity.BootID == previousBoot && r.identity.PID == previousPID {
		return false
	}
	for _, pair := range r.pairs {
		pair.nextDue = time.Time{}
	}
	return true
}

// load strictly reconstructs the durable control and dataplane state for every
// pair. A pair whose summary is missing while its projection or status row
// survives is refused: assigning from generation 1 over history would be a
// silent rollback. A pair that was never managed (no summary, no row) is fresh.
func (r *Runtime) load() error {
	r.identity = r.cfg.Identity
	r.configGen = r.cfg.Identity.ConfigGen
	r.hadSummary = false
	r.file = projectionState{}
	for _, pair := range r.pairs {
		pair.identity = r.cfg.Identity
		pair.identity.PeerID = pair.cfg.PeerID
		pair.started = false
		pair.prior = hub.Prior{}
		pair.grantValidUntil = 0
		pair.publishedExpiresAt = 0
		pair.verified = nil
		pair.lastPeer = nil
		pair.nextDue = time.Time{}
		pair.backoff = r.cfg.RetryMin
		pair.session.Identity = pair.identity
	}

	info, err := os.Stat(r.cfg.ConfDir)
	if err != nil {
		return fmt.Errorf("utc hub conf_dir: %w", err)
	}
	if !info.IsDir() {
		return errors.New("utc hub conf_dir is not a directory")
	}
	for _, pair := range r.pairs {
		for _, path := range []string{pair.cfg.SummaryPath, pair.cfg.VerifiedPath} {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return permanent(err)
			}
		}
	}

	// Prefer the GNB-written identity: hub.json may leave boot/PID empty
	// because they change on every GNB start. A malformed or foreign file is
	// never papered over with a stale status header.
	identityFromFile := false
	bootID, pid, err := readIdentityFile(r.identityPath(), r.cfg.Identity.LocalID)
	switch {
	case err == nil:
		r.identity.BootID, r.identity.PID = bootID, pid
		identityFromFile = true
	case !os.IsNotExist(err):
		return permanent(fmt.Errorf("utc.identity: %w", err))
	}

	var snapshot statusSnapshot
	statusExists := false
	statusRaw, statusErr := readStatusFile(r.cfg.StatusPath)
	if statusErr == nil {
		snapshot, err = parseStatus(statusRaw)
		if err != nil {
			return permanent(fmt.Errorf("utc.status: %w", err))
		}
		statusExists = true
		discloseOnly := snapshot.Header.Scope == "none" && snapshot.Header.Instance == "none"
		if !discloseOnly &&
			(snapshot.Header.Scope != r.cfg.Scope ||
				snapshot.Header.Instance != r.identity.Instance ||
				snapshot.Header.LocalID != r.identity.LocalID) {
			return permanent(errors.New("utc.status belongs to another scope/instance"))
		}
		if !identityFromFile {
			r.identity.BootID = snapshot.Header.BootID
			r.identity.PID = snapshot.Header.PID
		}
		if snapshot.Header.ConfigGen > r.configGen {
			r.configGen = snapshot.Header.ConfigGen
		}
	} else if !os.IsNotExist(statusErr) {
		return permanent(fmt.Errorf("utc.status: %w", statusErr))
	}
	if r.identity.BootID == "" || r.identity.PID == 0 {
		return permanent(errors.New("utc hub identity has no boot id/pid and no utc.identity file"))
	}

	file, err := inspectProjection(r.confPath(), r.identity)
	if err != nil {
		return permanent(fmt.Errorf("utc.conf: %w", err))
	}
	r.file = file
	if file.ConfigGen > r.configGen {
		r.configGen = file.ConfigGen
	}

	for _, pair := range r.pairs {
		summary, summaryErr := hub.LoadSummary(pair.cfg.SummaryPath)
		summaryExists := summaryErr == nil
		if summaryErr != nil && !os.IsNotExist(summaryErr) {
			return permanent(fmt.Errorf("utc-hub summary: %w", summaryErr))
		}
		verified, verifiedErr := loadVerified(pair.cfg.VerifiedPath)
		verifiedExists := verifiedErr == nil
		if verifiedErr != nil && !os.IsNotExist(verifiedErr) {
			return permanent(fmt.Errorf("utc-hub verified watermark: %w", verifiedErr))
		}
		if verifiedExists && !summaryExists {
			return permanent(errors.New("utc-hub summary is missing while a verified watermark exists"))
		}
		peer, hasRow := file.Peers[pair.cfg.PairID]
		if !summaryExists {
			if hasRow || statusHasPair(snapshot, statusExists, pair) {
				return permanent(fmt.Errorf(
					"utc-hub summary is missing while pair %d projection/status exists",
					pair.cfg.PairID))
			}
			continue
		}
		if err := r.validateSummary(pair, summary); err != nil {
			return permanent(err)
		}
		if summary.ConfigGen > r.configGen {
			r.configGen = summary.ConfigGen
		}
		if hasRow {
			if peer.PeerID != pair.cfg.PeerID {
				return permanent(fmt.Errorf(
					"utc.conf peer %d does not match configured pair %d",
					peer.PeerID, pair.cfg.PairID))
			}
			if peer.PairGen > summary.PairGen {
				return permanent(errors.New("utc.conf is ahead of the persisted summary"))
			}
			published := peer
			pair.lastPeer = &published
			pair.publishedExpiresAt = peer.ExpiresAt
		} else if file.Exists {
			return permanent(fmt.Errorf(
				"utc.conf is missing configured pair %d", pair.cfg.PairID))
		}
		pair.started = true
		r.hadSummary = true
		pair.prior = summary.Prior()
		pair.grantValidUntil = summary.ValidUntil
		if !hasRow {
			pair.publishedExpiresAt = summary.ValidUntil
		}
		if verifiedExists {
			if verified.Scope != r.cfg.Scope || verified.PeerID != pair.cfg.PeerID ||
				verified.PairID != pair.cfg.PairID || verified.Side != pair.cfg.Side ||
				verified.Mode != pair.cfg.Mode {
				return permanent(errors.New("utc-hub verified watermark belongs to another pair"))
			}
			// A verified watermark below the on-disk config_gen is a stale
			// watermark: this process previously died between the utc.conf write
			// and the watermark write of a same-generation renewal. That state is
			// legitimate and self-heals through the rebuild path; only a digest
			// mismatch at the watermark's own generation is tampering. Another
			// pair republishing advances config_gen, so the generations differ
			// and this pair's watermark is left untouched.
			if hasRow && verified.Prior() == pair.prior &&
				verified.ConfigGen == file.ConfigGen &&
				verified.ProjectionDigest != file.Digest {
				return permanent(errProjectionMismatch)
			}
			stored := verified
			pair.verified = &stored
		}
	}
	r.syncIdentity()
	return nil
}

// statusHasPair reports whether utc.status carries a row for this pair.
func statusHasPair(snapshot statusSnapshot, exists bool, pair *pairState) bool {
	if !exists {
		return false
	}
	peer, ok := snapshot.Peers[pair.cfg.PeerID]
	return ok && peer.PairID == pair.cfg.PairID
}

// syncIdentity copies the shared boot/PID/config_gen into every pair and
// session.
func (r *Runtime) syncIdentity() {
	r.identity.ConfigGen = r.configGen
	for _, pair := range r.pairs {
		pair.identity = r.identity
		pair.identity.PeerID = pair.cfg.PeerID
		pair.session.Identity = pair.identity
	}
}

// refreshIdentity re-binds boot/PID/config_gen to the current GNB process.
// utc.identity is authoritative when the GNB publishes it; utc.status remains
// the fallback for GNB builds that predate the file. A malformed identity file
// fails the cycle: publishing against a header the GNB never wrote would be a
// silent no-op the dataplane never applies.
func (r *Runtime) refreshIdentity() error {
	bootID, pid, identityErr := readIdentityFile(r.identityPath(), r.cfg.Identity.LocalID)
	haveIdentity := identityErr == nil
	if haveIdentity {
		r.identity.BootID, r.identity.PID = bootID, pid
	} else if !os.IsNotExist(identityErr) {
		return fmt.Errorf("utc.identity: %w", identityErr)
	}
	if raw, err := readStatusFile(r.cfg.StatusPath); err == nil {
		if snapshot, err := parseStatus(raw); err == nil {
			discloseOnly := snapshot.Header.Scope == "none" && snapshot.Header.Instance == "none"
			matches := snapshot.Header.Scope == r.cfg.Scope &&
				snapshot.Header.Instance == r.identity.Instance &&
				snapshot.Header.LocalID == r.identity.LocalID
			if !discloseOnly && !matches {
				r.logf("utc hub: ignored utc.status for another scope/instance/local")
			} else {
				if !haveIdentity {
					r.identity.BootID = snapshot.Header.BootID
					r.identity.PID = snapshot.Header.PID
				}
				if snapshot.Header.ConfigGen > r.configGen {
					r.configGen = snapshot.Header.ConfigGen
				}
			}
		}
	} else if !os.IsNotExist(err) {
		// Not fatal: the identity file already bound this cycle, and a
		// later poll will surface a persistent status problem.
		r.logf("utc hub: reading utc.status failed: %v", err)
	}
	if r.identity.BootID == "" || r.identity.PID == 0 {
		return errors.New("utc hub identity has no usable boot id/pid")
	}
	r.syncIdentity()
	return nil
}

func (r *Runtime) validateSummary(pair *pairState, summary hub.Summary) error {
	if summary.Scope != r.cfg.Scope || summary.Instance != pair.identity.Instance ||
		summary.PairID != pair.cfg.PairID || summary.Side != pair.cfg.Side ||
		summary.Mode != pair.cfg.Mode {
		return fmt.Errorf("utc-hub summary does not match configured pair %d", pair.cfg.PairID)
	}
	if !summary.SignatureVerified || summary.ValidUntil == 0 {
		return errors.New("utc-hub summary is not a verified grant")
	}
	return nil
}

// step performs one control cycle for every configured pair. It is the test and
// direct entry point; Run applies per-pair scheduling around stepPair.
func (r *Runtime) step(ctx context.Context) error {
	if err := r.refreshIdentity(); err != nil {
		return err
	}
	var errs []error
	for _, pair := range r.pairs {
		if err := r.stepPair(ctx, pair); err != nil {
			var state *stateError
			if errors.As(err, &state) {
				return err
			}
			errs = append(errs, fmt.Errorf("pair %d: %w", pair.cfg.PairID, err))
		}
	}
	return errors.Join(errs...)
}

// stepPair performs exactly one control exchange plus its durable publish and
// APPLIED verification for one pair. It is single-flight by construction.
func (r *Runtime) stepPair(ctx context.Context, pair *pairState) error {
	now := r.Now()
	var outcome hub.Outcome
	var err error
	if !pair.started {
		outcome, err = pair.session.Assign(ctx)
	} else {
		statusRaw, statusErr := readStatusFile(r.cfg.StatusPath)
		statusExists := statusErr == nil
		if statusErr != nil && !os.IsNotExist(statusErr) {
			return statusErr
		}
		statusRotate := false
		if statusExists {
			if _, parseErr := parseStatus(statusRaw); parseErr != nil {
				return parseErr
			}
			var rotateErr error
			statusRotate, _, rotateErr = hub.RotationRequired(
				statusRaw, pair.identity.PeerID, pair.cfg.PairID)
			if rotateErr != nil {
				return rotateErr
			}
		}
		timeRotate := pair.publishedExpiresAt != 0 &&
			uint64(now.Unix()) >= pair.publishedExpiresAt
		if statusExists && (statusRotate || !timeRotate) {
			outcome, _, err = pair.session.Recover(ctx, pair.prior, statusRaw)
		} else {
			outcome, err = pair.session.Renew(ctx, pair.prior, timeRotate)
		}
	}
	if err != nil {
		return err
	}
	if !outcome.Response.Verified {
		return errors.New("utc-hub accepted reply was not signature verified")
	}
	pair.started = true
	pair.prior = outcome.Prior()
	pair.grantValidUntil = outcome.Summary.ValidUntil
	// Validate against the time after the Hub exchange: not_before is minted by
	// the Hub, and a client clock a second behind must not reject forever.
	if err := r.validateAccepted(outcome, r.Now()); err != nil {
		return err
	}
	if err := r.publishOutcome(ctx, pair, outcome, now); err != nil {
		return err
	}
	return nil
}

// grantClockSkew tolerates a small Hub-ahead-of-client clock difference; the
// Hub mints not_before from its own clock.
const grantClockSkew = 2

func (r *Runtime) validateAccepted(outcome hub.Outcome, now time.Time) error {
	grant := outcome.Response.Grant
	if grant.NotBefore > uint64(now.Unix())+grantClockSkew {
		return errors.New("utc-hub grant is not valid yet")
	}
	if grant.ValidUntil <= uint64(now.Unix()) {
		return errors.New("utc-hub grant is already expired")
	}
	window := uint64(r.cfg.LeaseTTL / time.Second)
	if grant.ValidUntil <= grant.IssuedAt || grant.ValidUntil-grant.IssuedAt > window {
		return fmt.Errorf("utc-hub grant window does not match the configured %s TTL", r.cfg.LeaseTTL)
	}
	if outcome.Summary.EdgeID == 0 {
		return errors.New("utc-hub accepted reply has no placement edge")
	}
	return nil
}

func (r *Runtime) publishOutcome(ctx context.Context, pair *pairState, outcome hub.Outcome, now time.Time) error {
	verifiedPrior := pair.verified != nil && pair.verified.Prior() == pair.prior &&
		pair.verified.ConfigGen == r.configGen
	// A same-generation renewal keeps pair_gen and both epochs, so Outcome
	// reports Changed=false. The state-v2 consumer still accepts it when only
	// the expiry moves forward; republish so the dataplane deadline tracks the
	// renewed grant instead of waiting for the old deadline to force a rotate.
	renewalExtended := !outcome.Changed && r.file.Exists &&
		outcome.Summary.ValidUntil > pair.publishedExpiresAt
	// A GNB restart changes boot_id/PID; the old projection is bound to the
	// previous process and must be republished at a higher generation or the
	// dataplane refuses it as REQUIRES_NEW_GENERATION.
	identityChanged := r.file.Exists &&
		(r.file.BootID != r.identity.BootID || r.file.PID != r.identity.PID)
	writeNeeded := outcome.Changed || renewalExtended || !verifiedPrior ||
		!r.file.Exists || identityChanged
	if writeNeeded {
		peer, err := r.buildPeer(pair, outcome)
		if err != nil {
			return err
		}
		gen := r.configGen
		text, err := r.canonicalProjection(gen, pair.cfg.PairID, peer)
		if err != nil {
			return err
		}
		matches, err := r.fileMatches(text)
		if err != nil {
			return err
		}
		if !matches {
			nextGen := gen
			// The first projection may use the configured generation. Once a
			// projection reached disk at this generation — the file still
			// exists or a summary proves a prior publish — the dataplane owns
			// its high water, so every rewrite must strictly advance it.
			if r.file.Exists || r.hadSummary {
				nextGen++
			}
			if nextGen == 0 {
				nextGen = 1
			}
			r.setConfigGen(nextGen)
			text, err = r.canonicalProjection(nextGen, pair.cfg.PairID, peer)
			if err != nil {
				return err
			}
		}
		outcome.Summary.ConfigGen = r.configGen
		if err := r.publish(pair, outcome, text); err != nil {
			return err
		}
		// The publish is durable; refresh the state so a status failure keeps
		// the exact text/digest for the next idempotent verification.
		file, err := inspectProjection(r.confPath(), r.identity)
		if err != nil {
			return err
		}
		r.file = file
		published := peer
		pair.lastPeer = &published
		pair.publishedExpiresAt = peer.ExpiresAt
	} else if err := r.writeSummary(pair, outcome); err != nil {
		return err
	}

	if err := r.waitApplied(ctx, pair, outcome.Prior().PairGen); err != nil {
		return err
	}
	file, err := inspectProjection(r.confPath(), r.identity)
	if err != nil {
		return err
	}
	if !file.Exists {
		return errors.New("utc.conf disappeared after APPLIED")
	}
	peer, ok := file.Peers[pair.cfg.PairID]
	if !ok {
		return fmt.Errorf("utc.conf does not describe managed pair %d", pair.cfg.PairID)
	}
	verified := Verified{
		Scope: r.cfg.Scope, Instance: pair.identity.Instance,
		ConfigGen: r.configGen, PeerID: pair.identity.PeerID, PairID: pair.cfg.PairID,
		Side: pair.cfg.Side, Mode: pair.cfg.Mode,
		PairGen: outcome.Summary.PairGen, LeaseEpoch: outcome.Summary.LeaseEpoch,
		PlacementEpoch: outcome.Summary.PlacementEpoch,
		EdgeID:         outcome.Summary.EdgeID, ShardID: outcome.Summary.ShardID,
		ReactorID: outcome.Summary.ReactorID, ValidUntil: peer.ExpiresAt,
		ObservedAt: uint64(now.Unix()), ProjectionDigest: file.Digest,
	}
	raw, err := json.Marshal(verified)
	if err != nil {
		return err
	}
	if err := hub.WriteFileAtomic(pair.cfg.VerifiedPath, raw); err != nil {
		return fmt.Errorf("utc-hub verified watermark: %w", err)
	}
	pair.verified = &verified
	published := peer
	pair.lastPeer = &published
	if pair.publishedExpiresAt == 0 {
		pair.publishedExpiresAt = peer.ExpiresAt
	}
	return nil
}

// buildPeer maps this pair's accepted reply onto its projection row.
func (r *Runtime) buildPeer(pair *pairState, outcome hub.Outcome) (hub.Peer, error) {
	projection, _, err := hub.BuildProjectionMulti(pair.identity,
		[]hub.PeerResponse{{
			PeerID: pair.cfg.PeerID, Response: outcome.Response,
			LocalEndpoint: pair.cfg.LocalEndpoint,
		}})
	if err != nil {
		return hub.Peer{}, err
	}
	return projection.Peers[0], nil
}

// canonicalProjection rebuilds the whole shared file with one pair replaced and
// every other accepted row kept byte-identical.
func (r *Runtime) canonicalProjection(configGen uint64, replacedPair uint64, peer hub.Peer) ([]byte, error) {
	peers := make([]hub.Peer, 0, len(r.pairs))
	for _, other := range r.pairs {
		if other.cfg.PairID == replacedPair {
			peers = append(peers, peer)
			continue
		}
		if other.lastPeer != nil {
			peers = append(peers, *other.lastPeer)
		}
	}
	projection := hub.Projection{
		Scope: r.identity.Scope, Instance: r.identity.Instance,
		LocalID: r.identity.LocalID, ConfigGen: configGen,
		BootID: r.identity.BootID, PID: r.identity.PID,
		Capabilities: r.identity.Capabilities, Peers: peers,
	}
	return projection.Canonical()
}

func (r *Runtime) publish(pair *pairState, outcome hub.Outcome, text []byte) error {
	if len(text) == 0 {
		return errors.New("utc-hub outcome has no projection")
	}
	if err := r.writeSummary(pair, outcome); err != nil {
		return err
	}
	r.hadSummary = true
	return hub.WriteFileAtomic(r.confPath(), text)
}

func (r *Runtime) writeSummary(pair *pairState, outcome hub.Outcome) error {
	raw, err := json.Marshal(outcome.Summary)
	if err != nil {
		return err
	}
	if err := hub.WriteFileAtomic(pair.cfg.SummaryPath, raw); err != nil {
		return fmt.Errorf("utc-hub summary: %w", err)
	}
	return nil
}

func (r *Runtime) fileMatches(text []byte) (bool, error) {
	raw, err := os.ReadFile(r.confPath())
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return bytes.Equal(raw, text), nil
}

func (r *Runtime) setConfigGen(gen uint64) {
	r.configGen = gen
	r.syncIdentity()
}

// waitApplied polls utc.status until it bears this process identity and the
// managed pair is APPLIED. A missing or non-matching status never advances the
// verified watermark.
func (r *Runtime) waitApplied(ctx context.Context, pair *pairState, pairGen uint64) error {
	deadline := time.Now().Add(r.cfg.ApplyTimeout)
	last := errors.New("utc.status is not available")
	for {
		raw, err := readStatusFile(r.cfg.StatusPath)
		if err == nil {
			snapshot, parseErr := parseStatus(raw)
			if parseErr == nil {
				if appliedErr := snapshot.applied(pair.identity, pair.cfg.PairID, pairGen,
					r.Now(), r.cfg.StatusMaxAge); appliedErr == nil {
					return nil
				} else {
					last = appliedErr
				}
			} else {
				last = parseErr
			}
		} else if !os.IsNotExist(err) {
			last = err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("utc.status did not report APPLIED: %w", last)
		}
		timer := time.NewTimer(r.cfg.StatusPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// nextDelay targets the earlier of the grant renewal window and the expiry
// published to GNB. Renewals normally republish the later deadline; the
// published expiry stays as a safety net so a renewal that cannot be published
// falls back to rotating at the deadline already known to the dataplane.
func (r *Runtime) nextDelay(pair *pairState) time.Duration {
	if pair.grantValidUntil == 0 {
		return r.cfg.RetryMin
	}
	target := int64(pair.grantValidUntil) - int64(r.cfg.RenewLead/time.Second) -
		int64(r.Jitter(r.cfg.JitterMax)/time.Second)
	if pair.publishedExpiresAt != 0 && int64(pair.publishedExpiresAt) < target {
		target = int64(pair.publishedExpiresAt)
	}
	// A successful renewal inside the window may not move valid_until (the
	// frozen renew path keeps the epoch). Wait for the published deadline
	// instead of retrying the same renewal every second.
	if target <= r.Now().Unix() {
		if pair.publishedExpiresAt <= uint64(r.Now().Unix()) {
			return r.cfg.RetryMin
		}
		target = int64(pair.publishedExpiresAt)
	}
	delay := time.Unix(target, 0).Sub(r.Now())
	if delay < r.cfg.RetryMin {
		delay = r.cfg.RetryMin
	}
	return delay
}
