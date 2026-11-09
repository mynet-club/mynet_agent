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

// Runtime owns one pair/side. All control exchanges and projection publishes
// run serially from Run; a second Run on the same state directory is an
// operator error, not a supported concurrency mode.
type Runtime struct {
	cfg      Config
	session  *hub.Session
	identity hub.Identity

	// Now, Jitter and Logf are testability/observability seams. New installs
	// production defaults.
	Now    func() time.Time
	Jitter func(time.Duration) time.Duration
	Logf   func(format string, args ...any)

	started            bool
	prior              hub.Prior
	grantValidUntil    uint64
	publishedExpiresAt uint64
	configGen          uint64
	verified           *Verified
	file               projectionState
	// hadSummary records that a summary reached disk at the current or an
	// earlier generation: the GNB may already hold that config_gen as its
	// high-water mark even when utc.conf is gone, so a rebuild must advance.
	hadSummary bool
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
	return &Runtime{
		cfg: cfg,
		session: &hub.Session{
			Client: client, Identity: cfg.Identity,
			PairID: cfg.PairID, Side: cfg.Side, Mode: cfg.Mode,
		},
		identity:  cfg.Identity,
		configGen: cfg.Identity.ConfigGen,
		Now:       time.Now,
		Jitter:    defaultJitter,
	}, nil
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

// Run loads durable state once, then renews before expiry until ctx is
// cancelled. Corrupt or ambiguous state stops the runtime; transient control
// or GNB failures are retried with bounded backoff.
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
	for {
		err := r.step(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var wait time.Duration
		if err != nil {
			var state *stateError
			if errors.As(err, &state) {
				return err
			}
			r.logf("utc hub cycle failed; retrying: %v", err)
			wait = backoff
			backoff = growBackoff(backoff, r.cfg.RetryMax)
		} else {
			backoff = r.cfg.RetryMin
			wait = r.nextDelay()
		}
		if wait < r.cfg.RetryMin {
			wait = r.cfg.RetryMin
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

// load strictly reconstructs the durable control and dataplane state. A
// missing summary with any surviving projection/status is refused: assigning
// from generation 1 over history would be a silent rollback.
func (r *Runtime) load() error {
	r.identity = r.cfg.Identity
	r.configGen = r.cfg.Identity.ConfigGen
	r.started = false
	r.hadSummary = false
	r.prior = hub.Prior{}
	r.grantValidUntil = 0
	r.publishedExpiresAt = 0
	r.verified = nil
	r.file = projectionState{}

	info, err := os.Stat(r.cfg.ConfDir)
	if err != nil {
		return fmt.Errorf("utc hub conf_dir: %w", err)
	}
	if !info.IsDir() {
		return errors.New("utc hub conf_dir is not a directory")
	}
	for _, path := range []string{r.cfg.SummaryPath, r.cfg.VerifiedPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return permanent(err)
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

	statusRaw, statusErr := readStatusFile(r.cfg.StatusPath)
	statusExists := statusErr == nil
	if statusExists {
		snapshot, err := parseStatus(statusRaw)
		if err != nil {
			return permanent(fmt.Errorf("utc.status: %w", err))
		}
		if snapshot.Header.Scope != r.cfg.Scope ||
			snapshot.Header.Instance != r.identity.Instance ||
			snapshot.Header.LocalID != r.identity.LocalID {
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

	file, err := inspectProjection(r.confPath(), r.identity, r.cfg.PairID)
	if err != nil {
		return permanent(fmt.Errorf("utc.conf: %w", err))
	}
	r.file = file
	if file.ConfigGen > r.configGen {
		r.configGen = file.ConfigGen
	}

	summary, err := hub.LoadSummary(r.cfg.SummaryPath)
	summaryExists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return permanent(fmt.Errorf("utc-hub summary: %w", err))
	}
	if !summaryExists {
		if file.Exists || statusExists {
			return permanent(errors.New("utc-hub summary is missing while projection/status exists"))
		}
		r.started = false
	} else {
		if err := r.validateSummary(summary); err != nil {
			return permanent(err)
		}
		if summary.ConfigGen > r.configGen {
			r.configGen = summary.ConfigGen
		}
		if file.Exists && file.PairGen > summary.PairGen {
			return permanent(errors.New("utc.conf is ahead of the persisted summary"))
		}
		r.started = true
		r.hadSummary = true
		r.prior = summary.Prior()
		r.grantValidUntil = summary.ValidUntil
		if file.Exists {
			r.publishedExpiresAt = file.ExpiresAt
		} else {
			r.publishedExpiresAt = summary.ValidUntil
		}
	}

	verified, err := loadVerified(r.cfg.VerifiedPath)
	switch {
	case err == nil:
		r.verified = &verified
	case os.IsNotExist(err):
		r.verified = nil
	default:
		return permanent(fmt.Errorf("utc-hub verified watermark: %w", err))
	}
	if r.verified != nil {
		if r.verified.Scope != r.cfg.Scope || r.verified.PeerID != r.identity.PeerID ||
			r.verified.PairID != r.cfg.PairID || r.verified.Side != r.cfg.Side ||
			r.verified.Mode != r.cfg.Mode {
			return permanent(errors.New("utc-hub verified watermark belongs to another pair"))
		}
		if !summaryExists {
			return permanent(errors.New("utc-hub summary is missing while a verified watermark exists"))
		}
		// A verified watermark below the on-disk config_gen is a stale
		// watermark: this process previously died between the utc.conf write
		// and the watermark write of a same-generation renewal. That state is
		// legitimate and self-heals through the rebuild path; only a digest
		// mismatch at the watermark's own generation is tampering.
		if file.Exists && r.verified.Prior() == r.prior &&
			r.verified.ConfigGen == r.configGen &&
			r.verified.ProjectionDigest != file.Digest {
			return permanent(errProjectionMismatch)
		}
	}
	r.identity.ConfigGen = r.configGen
	r.session.Identity = r.identity
	return nil
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
		if snapshot, err := parseStatus(raw); err == nil &&
			snapshot.Header.Scope == r.cfg.Scope &&
			snapshot.Header.Instance == r.identity.Instance &&
			snapshot.Header.LocalID == r.identity.LocalID {
			if !haveIdentity {
				r.identity.BootID = snapshot.Header.BootID
				r.identity.PID = snapshot.Header.PID
			}
			if snapshot.Header.ConfigGen > r.configGen {
				r.configGen = snapshot.Header.ConfigGen
			}
		} else if err == nil {
			r.logf("utc hub: ignored utc.status for another scope/instance/local")
		}
	} else if !os.IsNotExist(err) {
		// Not fatal: the identity file already bound this cycle, and a
		// later poll will surface a persistent status problem.
		r.logf("utc hub: reading utc.status failed: %v", err)
	}
	if r.identity.BootID == "" || r.identity.PID == 0 {
		return errors.New("utc hub identity has no usable boot id/pid")
	}
	r.identity.ConfigGen = r.configGen
	r.session.Identity = r.identity
	return nil
}

func (r *Runtime) validateSummary(summary hub.Summary) error {
	if summary.Scope != r.cfg.Scope || summary.Instance != r.identity.Instance ||
		summary.PairID != r.cfg.PairID || summary.Side != r.cfg.Side ||
		summary.Mode != r.cfg.Mode {
		return errors.New("utc-hub summary does not match the configured pair")
	}
	if !summary.SignatureVerified || summary.ValidUntil == 0 {
		return errors.New("utc-hub summary is not a verified grant")
	}
	return nil
}

// step performs exactly one control exchange plus its durable publish and
// APPLIED verification. It is single-flight by construction.
func (r *Runtime) step(ctx context.Context) error {
	if err := r.refreshIdentity(); err != nil {
		return err
	}
	now := r.Now()
	var outcome hub.Outcome
	var err error
	if !r.started {
		outcome, err = r.session.Assign(ctx)
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
			statusRotate, _, rotateErr = hub.RotationRequired(statusRaw, r.identity.PeerID, r.cfg.PairID)
			if rotateErr != nil {
				return rotateErr
			}
		}
		timeRotate := r.publishedExpiresAt != 0 && uint64(now.Unix()) >= r.publishedExpiresAt
		if statusExists && (statusRotate || !timeRotate) {
			outcome, _, err = r.session.Recover(ctx, r.prior, statusRaw)
		} else {
			outcome, err = r.session.Renew(ctx, r.prior, timeRotate)
		}
	}
	if err != nil {
		return err
	}
	if !outcome.Response.Verified {
		return errors.New("utc-hub accepted reply was not signature verified")
	}
	r.started = true
	r.prior = outcome.Prior()
	r.grantValidUntil = outcome.Summary.ValidUntil
	if err := r.validateAccepted(outcome, now); err != nil {
		return err
	}
	if err := r.publishOutcome(ctx, outcome, now); err != nil {
		return err
	}
	return nil
}

func (r *Runtime) validateAccepted(outcome hub.Outcome, now time.Time) error {
	grant := outcome.Response.Grant
	if grant.NotBefore > uint64(now.Unix()) {
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

func (r *Runtime) publishOutcome(ctx context.Context, outcome hub.Outcome, now time.Time) error {
	verifiedPrior := r.verified != nil && r.verified.Prior() == r.prior &&
		r.verified.ConfigGen == r.configGen
	// A same-generation renewal keeps pair_gen and both epochs, so Outcome
	// reports Changed=false. The state-v2 consumer still accepts it when only
	// the expiry moves forward; republish so the dataplane deadline tracks the
	// renewed grant instead of waiting for the old deadline to force a rotate.
	renewalExtended := !outcome.Changed && r.file.Exists &&
		outcome.Summary.ValidUntil > r.publishedExpiresAt
	writeNeeded := outcome.Changed || renewalExtended || !verifiedPrior || !r.file.Exists
	if writeNeeded {
		matches, err := r.fileMatches(outcome.Text)
		if err != nil {
			return err
		}
		if !matches {
			nextGen := r.configGen
			// The first projection may use the configured generation. Once a
			// projection reached disk at this generation — the file still
			// exists or the summary proves a prior publish — the dataplane
			// owns its high water, so every rewrite must strictly advance it.
			if r.file.Exists || r.hadSummary {
				nextGen++
			}
			if nextGen == 0 {
				nextGen = 1
			}
			r.setConfigGen(nextGen)
			projection, text, err := hub.BuildProjection(r.identity, outcome.Response)
			if err != nil {
				return err
			}
			outcome.Projection = projection
			outcome.Text = text
			outcome.Summary.ConfigGen = nextGen
		} else {
			outcome.Summary.ConfigGen = r.configGen
		}
		if err := r.publish(outcome); err != nil {
			return err
		}
		// The publish is durable; refresh the state so a status failure keeps
		// the exact text/digest for the next idempotent verification.
		file, err := inspectProjection(r.confPath(), r.identity, r.cfg.PairID)
		if err != nil {
			return err
		}
		r.file = file
		r.publishedExpiresAt = outcome.Projection.Peers[0].ExpiresAt
	} else if err := r.writeSummary(outcome); err != nil {
		return err
	}

	if err := r.waitApplied(ctx, outcome.Prior().PairGen); err != nil {
		return err
	}
	file, err := inspectProjection(r.confPath(), r.identity, r.cfg.PairID)
	if err != nil {
		return err
	}
	if !file.Exists {
		return errors.New("utc.conf disappeared after APPLIED")
	}
	verified := Verified{
		Scope: r.cfg.Scope, Instance: r.identity.Instance,
		ConfigGen: r.configGen, PeerID: r.identity.PeerID, PairID: r.cfg.PairID,
		Side: r.cfg.Side, Mode: r.cfg.Mode,
		PairGen: outcome.Summary.PairGen, LeaseEpoch: outcome.Summary.LeaseEpoch,
		PlacementEpoch: outcome.Summary.PlacementEpoch,
		EdgeID:         outcome.Summary.EdgeID, ShardID: outcome.Summary.ShardID,
		ReactorID: outcome.Summary.ReactorID, ValidUntil: file.ExpiresAt,
		ObservedAt: uint64(now.Unix()), ProjectionDigest: file.Digest,
	}
	raw, err := json.Marshal(verified)
	if err != nil {
		return err
	}
	if err := hub.WriteFileAtomic(r.cfg.VerifiedPath, raw); err != nil {
		return fmt.Errorf("utc-hub verified watermark: %w", err)
	}
	r.verified = &verified
	r.file = file
	if r.publishedExpiresAt == 0 {
		r.publishedExpiresAt = file.ExpiresAt
	}
	return nil
}

func (r *Runtime) publish(outcome hub.Outcome) error {
	if len(outcome.Text) == 0 {
		return errors.New("utc-hub outcome has no projection")
	}
	if err := r.writeSummary(outcome); err != nil {
		return err
	}
	r.hadSummary = true
	return hub.WriteFileAtomic(r.confPath(), outcome.Text)
}

func (r *Runtime) writeSummary(outcome hub.Outcome) error {
	raw, err := json.Marshal(outcome.Summary)
	if err != nil {
		return err
	}
	if err := hub.WriteFileAtomic(r.cfg.SummaryPath, raw); err != nil {
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
	r.identity.ConfigGen = gen
	r.session.Identity = r.identity
}

// waitApplied polls utc.status until it bears this process identity and the
// managed pair is APPLIED. A missing or non-matching status never advances the
// verified watermark.
func (r *Runtime) waitApplied(ctx context.Context, pairGen uint64) error {
	deadline := time.Now().Add(r.cfg.ApplyTimeout)
	last := errors.New("utc.status is not available")
	for {
		raw, err := readStatusFile(r.cfg.StatusPath)
		if err == nil {
			snapshot, parseErr := parseStatus(raw)
			if parseErr == nil {
				if appliedErr := snapshot.applied(r.identity, r.cfg.PairID, pairGen,
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
func (r *Runtime) nextDelay() time.Duration {
	if r.grantValidUntil == 0 {
		return r.cfg.RetryMin
	}
	target := int64(r.grantValidUntil) - int64(r.cfg.RenewLead/time.Second) -
		int64(r.Jitter(r.cfg.JitterMax)/time.Second)
	if r.publishedExpiresAt != 0 && int64(r.publishedExpiresAt) < target {
		target = int64(r.publishedExpiresAt)
	}
	// A successful renewal inside the window may not move valid_until (the
	// frozen renew path keeps the epoch). Wait for the published deadline
	// instead of retrying the same renewal every second.
	if target <= r.Now().Unix() {
		if r.publishedExpiresAt <= uint64(r.Now().Unix()) {
			return r.cfg.RetryMin
		}
		target = int64(r.publishedExpiresAt)
	}
	delay := time.Unix(target, 0).Sub(r.Now())
	if delay < r.cfg.RetryMin {
		delay = r.cfg.RetryMin
	}
	return delay
}
