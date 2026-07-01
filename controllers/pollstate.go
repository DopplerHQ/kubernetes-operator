package controllers

import (
	"math/rand"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

const (
	// stickyFailureThreshold is the number of consecutive poll/download failures
	// after which a resource enters v3-only "sticky" mode.
	stickyFailureThreshold = 3
	// stickyBaseDuration is the base window a resource stays in v3-only mode before
	// the poll path is re-probed.
	stickyBaseDuration = time.Hour
	// stickyJitter is the maximum +/- jitter applied to stickyBaseDuration to avoid
	// synchronized re-probes across many resources.
	stickyJitter = 10 * time.Minute
)

// pollState is the in-memory, per-resource state that drives poll-first reconciliation.
// It is never persisted (per spec): a controller restart naturally falls back to the v3
// adoption path on the next reconcile.
type pollState struct {
	etag         string
	identityHash string // hash of: auth ref, project, config, processors hash, format, nameTransformer
	generation   int64
	failures     int       // consecutive poll/download failures
	stickyUntil  time.Time // v3-only mode until this instant; zero value = not sticky
}

// pollStates is a concurrency-safe map of DopplerSecret UID -> pollState. Keying by UID
// means a resource recreate (which mints a fresh UID) naturally gets an absent entry and
// therefore takes the v4 adoption path again.
// Poll-first reconcile assumes a single writer (leader election enabled); multiple concurrent writers/replicas would only cause redundant polls/downloads, not data corruption, since each write is a full create/update of the managed secret, not a partial merge.
type pollStates struct {
	sync.Mutex
	m map[types.UID]*pollState

	// nowFn and jitterFn are injectable for deterministic testing of sticky expiry.
	nowFn    func() time.Time
	jitterFn func() time.Duration
}

// newPollStates builds a pollStates with the real clock and randomized sticky jitter.
func newPollStates() *pollStates {
	return &pollStates{
		m:        map[types.UID]*pollState{},
		nowFn:    time.Now,
		jitterFn: defaultStickyJitter,
	}
}

// defaultStickyJitter returns a random jitter in [-stickyJitter, +stickyJitter].
func defaultStickyJitter() time.Duration {
	return time.Duration(rand.Int63n(int64(2*stickyJitter+1))) - stickyJitter
}

// get returns the state for a UID, if present.
func (ps *pollStates) get(uid types.UID) (*pollState, bool) {
	ps.Lock()
	defer ps.Unlock()
	st, ok := ps.m[uid]
	return st, ok
}

// shouldPoll reports whether the poll endpoint should be consulted for this resource.
// It returns the stored etag (copied out under the lock) and ok=true only when: state
// exists, identity+generation match, an etag is present, and the resource is not
// currently sticky. Returning the etag by value (rather than a *pollState pointer) keeps
// callers from dereferencing shared mutable state outside the mutex.
func (ps *pollStates) shouldPoll(uid types.UID, identityHash string, generation int64) (etag string, ok bool) {
	ps.Lock()
	defer ps.Unlock()
	st, exists := ps.m[uid]
	if !exists {
		return "", false
	}
	if st.identityHash != identityHash || st.generation != generation {
		return "", false
	}
	if st.etag == "" {
		return "", false
	}
	if ps.nowFn().Before(st.stickyUntil) {
		return "", false
	}
	return st.etag, true
}

// shouldAttemptAdoption reports whether a v4 adoption download should be attempted for a
// resource that is not currently poll-eligible. Adoption is eligible when either no state
// exists yet, or a state exists that has never produced a usable etag and is not currently
// sticky. This lets a transient first-adoption failure be retried on subsequent reconciles
// instead of permanently locking the resource onto v3. The sticky window is honored
// identically to shouldPoll so that 404/501 (unsupported) resources back off the same way
// as any other repeated failure, and are re-probed once the sticky window expires (a
// server can gain v4 support after the operator was rolled out, e.g. a feature-flag
// flip) rather than being permanently excluded. The check runs entirely under the lock.
func (ps *pollStates) shouldAttemptAdoption(uid types.UID, identityHash string, generation int64) bool {
	ps.Lock()
	defer ps.Unlock()
	st, exists := ps.m[uid]
	if !exists {
		return true
	}
	if st.identityHash != identityHash || st.generation != generation {
		// Identity/generation changed: the stored state is stale; adopt afresh.
		return true
	}
	// Only resources that have never produced a usable etag take the adoption path;
	// once an etag exists, ongoing change detection flows through shouldPoll instead.
	if st.etag != "" {
		return false
	}
	// Back off while sticky (e.g. after 3 consecutive adoption failures, or a 404/501).
	if ps.nowFn().Before(st.stickyUntil) {
		return false
	}
	return true
}

// recordSuccess (re)creates fresh state after a successful v4 adoption or changed-download.
// It stores the new etag, clears failures, and marks the resource non-sticky.
func (ps *pollStates) recordSuccess(uid types.UID, etag string, identityHash string, generation int64) {
	ps.Lock()
	defer ps.Unlock()
	ps.m[uid] = &pollState{
		etag:         etag,
		identityHash: identityHash,
		generation:   generation,
		failures:     0,
	}
}

// recordPollCurrent clears the consecutive-failure count (and any sticky window) after a
// PollCurrent response, preserving the existing etag/identity/generation. Without this, a
// "current" response between two failures would let the failure count keep climbing across
// otherwise-healthy poll cycles, so "3 consecutive failures" would not actually require
// consecutiveness. Creates no new entry: PollCurrent is only ever observed for a resource
// that already has state (shouldPoll requires it), so a missing entry here is a no-op.
func (ps *pollStates) recordPollCurrent(uid types.UID) {
	ps.Lock()
	defer ps.Unlock()
	st, ok := ps.m[uid]
	if !ok {
		return
	}
	st.failures = 0
	st.stickyUntil = time.Time{}
}

// resetForIdentity resets an existing entry to the given identity/generation with a clean
// slate (empty etag, zero failures, non-sticky) when the stored identity/generation is
// stale. Callers must hold the lock. Returns the (possibly newly reset) state.
func (ps *pollStates) resetForIdentity(uid types.UID, identityHash string, generation int64) *pollState {
	st, ok := ps.m[uid]
	if !ok || st.identityHash != identityHash || st.generation != generation {
		st = &pollState{
			identityHash: identityHash,
			generation:   generation,
		}
		ps.m[uid] = st
	}
	return st
}

// recordFailure increments the consecutive failure count for a resource, creating a state
// entry if one does not yet exist. If the stored identity/generation is stale (differs from
// the current one), the entry is first reset to the current identity/generation with a
// clean slate before counting, so failure counting always accrues against the resource's
// current identity rather than restarting silently against stale state forever (a stale
// entry is otherwise never adopted-over, since adoption reads etag=="" as "never adopted"
// only for freshly-reset state). When the failure count reaches the threshold, the resource
// enters sticky (v3-only) mode. It returns whether the resource is now sticky.
func (ps *pollStates) recordFailure(uid types.UID, identityHash string, generation int64) bool {
	ps.Lock()
	defer ps.Unlock()
	st := ps.resetForIdentity(uid, identityHash, generation)
	st.failures++
	if st.failures >= stickyFailureThreshold {
		st.stickyUntil = ps.stickyExpiry()
		return true
	}
	return false
}

// recordUnsupported flags a resource's server as lacking v4 support (404/501) and puts it
// into sticky mode immediately, creating a state entry if one does not yet exist (and
// resetting a stale identity/generation first, mirroring recordFailure). Sticky here is
// symmetric with every other failure mode: after the sticky window expires,
// shouldAttemptAdoption re-probes v4 support, so a server that gains v4 support later (or a
// transient 404/501) self-heals instead of being permanently excluded.
func (ps *pollStates) recordUnsupported(uid types.UID, identityHash string, generation int64) {
	ps.Lock()
	defer ps.Unlock()
	st := ps.resetForIdentity(uid, identityHash, generation)
	st.stickyUntil = ps.stickyExpiry()
}

// reset removes any state for a UID. A nil receiver is a no-op so callers never need to
// nil-check before calling.
func (ps *pollStates) reset(uid types.UID) {
	if ps == nil {
		return
	}
	ps.Lock()
	defer ps.Unlock()
	delete(ps.m, uid)
}

// stickyExpiry computes now + stickyBaseDuration +/- jitter. Callers must hold the lock.
func (ps *pollStates) stickyExpiry() time.Time {
	return ps.nowFn().Add(stickyBaseDuration + ps.jitterFn())
}
