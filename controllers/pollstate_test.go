package controllers

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
)

// fixedClock returns a controllable now function for deterministic sticky-expiry assertions.
func fixedClock(t *time.Time) func() time.Time {
	return func() time.Time { return *t }
}

const (
	testUID  = types.UID("uid-1")
	testHash = "identity-hash-1"
	testEtag = "etag-1"
)

// newTestPollStates builds a pollStates with a deterministic clock and zero jitter so
// sticky expiry is exactly now+stickyBaseDuration.
func newTestPollStates(now *time.Time) *pollStates {
	ps := newPollStates()
	ps.nowFn = fixedClock(now)
	ps.jitterFn = func() time.Duration { return 0 }
	return ps
}

func TestPollStateFirstReconcileNoState(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)

	if _, ok := ps.get(testUID); ok {
		t.Fatalf("expected no state for fresh UID")
	}
	if _, ok := ps.shouldPoll(testUID, testHash, 1); ok {
		t.Fatalf("expected shouldPoll=false when no state exists")
	}
}

func TestPollStateRecordSuccessEnablesPoll(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)

	ps.recordSuccess(testUID, testEtag, testHash, 1)

	st, ok := ps.get(testUID)
	if !ok {
		t.Fatalf("expected state to exist after recordSuccess")
	}
	if st.etag != testEtag || st.identityHash != testHash || st.generation != 1 {
		t.Fatalf("unexpected state: %+v", st)
	}
	if st.failures != 0 {
		t.Fatalf("expected failures=0, got %d", st.failures)
	}

	if _, ok := ps.shouldPoll(testUID, testHash, 1); !ok {
		t.Fatalf("expected shouldPoll=true after a clean success")
	}
}

func TestPollStateChangedUpdatesEtagResetsFailures(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)

	ps.recordSuccess(testUID, testEtag, testHash, 1)
	ps.recordFailure(testUID, testHash, 1)
	// "changed" branch re-records success with a fresh etag.
	ps.recordSuccess(testUID, "etag-2", testHash, 1)

	st, _ := ps.get(testUID)
	if st.etag != "etag-2" {
		t.Fatalf("expected etag updated to etag-2, got %q", st.etag)
	}
	if st.failures != 0 {
		t.Fatalf("expected failures reset to 0, got %d", st.failures)
	}
}

func TestPollStateFailuresAccumulateAndGoStickyAtThree(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)
	ps.recordSuccess(testUID, testEtag, testHash, 1)

	if sticky := ps.recordFailure(testUID, testHash, 1); sticky {
		t.Fatalf("did not expect sticky after 1 failure")
	}
	if sticky := ps.recordFailure(testUID, testHash, 1); sticky {
		t.Fatalf("did not expect sticky after 2 failures")
	}
	sticky := ps.recordFailure(testUID, testHash, 1)
	if !sticky {
		t.Fatalf("expected sticky after 3 failures")
	}

	st, _ := ps.get(testUID)
	if st.failures != 3 {
		t.Fatalf("expected failures=3, got %d", st.failures)
	}
	wantExpiry := now.Add(stickyBaseDuration)
	if !st.stickyUntil.Equal(wantExpiry) {
		t.Fatalf("expected stickyUntil=%v, got %v", wantExpiry, st.stickyUntil)
	}
	// While sticky, polling must be suppressed.
	if _, ok := ps.shouldPoll(testUID, testHash, 1); ok {
		t.Fatalf("expected shouldPoll=false while sticky")
	}
}

func TestPollStateStickyExpiryResumesPolling(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)
	ps.recordSuccess(testUID, testEtag, testHash, 1)
	ps.recordFailure(testUID, testHash, 1)
	ps.recordFailure(testUID, testHash, 1)
	ps.recordFailure(testUID, testHash, 1)

	// Just before expiry: still sticky.
	now = now.Add(stickyBaseDuration - time.Second)
	if _, ok := ps.shouldPoll(testUID, testHash, 1); ok {
		t.Fatalf("expected still sticky just before expiry")
	}

	// After expiry: polling resumes (etag is still present, identity+gen still match).
	now = now.Add(2 * time.Second)
	if _, ok := ps.shouldPoll(testUID, testHash, 1); !ok {
		t.Fatalf("expected polling to resume after sticky expiry")
	}
}

func TestPollStateIdentityChangeBypassesPoll(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)
	ps.recordSuccess(testUID, testEtag, testHash, 1)

	if _, ok := ps.shouldPoll(testUID, "different-hash", 1); ok {
		t.Fatalf("expected shouldPoll=false when identity hash changes")
	}
}

func TestPollStateGenerationChangeBypassesPoll(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)
	ps.recordSuccess(testUID, testEtag, testHash, 1)

	if _, ok := ps.shouldPoll(testUID, testHash, 2); ok {
		t.Fatalf("expected shouldPoll=false when generation changes")
	}
}

func TestPollStateEmptyEtagBypassesPoll(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)
	ps.recordSuccess(testUID, "", testHash, 1)

	if _, ok := ps.shouldPoll(testUID, testHash, 1); ok {
		t.Fatalf("expected shouldPoll=false when etag is empty")
	}
}

func TestPollStateUIDRecreateGetsFreshState(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)
	ps.recordSuccess(testUID, testEtag, testHash, 1)

	// A recreate is a brand-new UID; the map naturally has no entry for it.
	newUID := types.UID("uid-2")
	if _, ok := ps.get(newUID); ok {
		t.Fatalf("expected no state for recreated (new) UID")
	}
	if _, ok := ps.shouldPoll(newUID, testHash, 1); ok {
		t.Fatalf("expected shouldPoll=false for recreated UID")
	}
}

func TestPollStateReset(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)
	ps.recordSuccess(testUID, testEtag, testHash, 1)
	ps.reset(testUID)

	if _, ok := ps.get(testUID); ok {
		t.Fatalf("expected state removed after reset")
	}
}

func TestPollStateResetNilReceiverIsNoOp(t *testing.T) {
	var ps *pollStates
	// Must not panic on a nil receiver so Reconcile callers need not nil-check.
	ps.reset(testUID)
}

func TestPollStateUnsupportedGoesStickyImmediately(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)

	ps.recordUnsupported(testUID, testHash, 1)

	st, ok := ps.get(testUID)
	if !ok {
		t.Fatalf("expected state to exist after recordUnsupported")
	}
	wantExpiry := now.Add(stickyBaseDuration)
	if !st.stickyUntil.Equal(wantExpiry) {
		t.Fatalf("expected immediate sticky until %v, got %v", wantExpiry, st.stickyUntil)
	}
	// Even with matching identity/gen, a sticky state must not poll.
	if _, ok := ps.shouldPoll(testUID, testHash, 1); ok {
		t.Fatalf("expected shouldPoll=false when sticky after 404/501")
	}
}

// TestPollStateUnsupportedSelfHealsAfterStickyExpiry is Finding 3: a 404/501 (server has no
// v4 support) must NOT be a permanent condition, since the operator can be released before
// the server-side v4 flag is turned on. Sticky here behaves exactly like any other failure
// mode: once the sticky window expires, adoption is re-probed. A repeat 404/501 re-enters
// sticky; a success resumes the v4 path.
func TestPollStateUnsupportedSelfHealsAfterStickyExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)

	// 404/501 -> sticky.
	ps.recordUnsupported(testUID, testHash, 1)
	if ps.shouldAttemptAdoption(testUID, testHash, 1) {
		t.Fatalf("expected adoption NOT eligible immediately after 404/501")
	}

	// Just before expiry: still sticky.
	now = now.Add(stickyBaseDuration - time.Second)
	if ps.shouldAttemptAdoption(testUID, testHash, 1) {
		t.Fatalf("expected adoption NOT eligible just before sticky expiry")
	}

	// Expiry: adoption re-probe runs.
	now = now.Add(2 * time.Second)
	if !ps.shouldAttemptAdoption(testUID, testHash, 1) {
		t.Fatalf("expected adoption eligible again after sticky expiry (self-heal re-probe)")
	}

	// Another 404/501 re-enters sticky, exactly as before.
	ps.recordUnsupported(testUID, testHash, 1)
	if ps.shouldAttemptAdoption(testUID, testHash, 1) {
		t.Fatalf("expected adoption NOT eligible again immediately after a second 404/501")
	}

	// Expire again, then a clean success on the re-probe resumes the v4 path.
	now = now.Add(stickyBaseDuration + time.Second)
	if !ps.shouldAttemptAdoption(testUID, testHash, 1) {
		t.Fatalf("expected adoption eligible after second sticky expiry")
	}
	ps.recordSuccess(testUID, testEtag, testHash, 1)
	if _, ok := ps.shouldPoll(testUID, testHash, 1); !ok {
		t.Fatalf("expected v4 poll path to resume after a success on the re-probe")
	}
}

func TestPollStateJitterWithinBounds(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newPollStates() // real jitterFn
	ps.nowFn = fixedClock(&now)

	ps.recordSuccess(testUID, testEtag, testHash, 1)
	ps.recordFailure(testUID, testHash, 1)
	ps.recordFailure(testUID, testHash, 1)
	ps.recordFailure(testUID, testHash, 1)

	st, _ := ps.get(testUID)
	minExpiry := now.Add(stickyBaseDuration - stickyJitter)
	maxExpiry := now.Add(stickyBaseDuration + stickyJitter)
	if st.stickyUntil.Before(minExpiry) || st.stickyUntil.After(maxExpiry) {
		t.Fatalf("stickyUntil %v out of jitter bounds [%v, %v]", st.stickyUntil, minExpiry, maxExpiry)
	}
}

// --- Finding 2: adoption-eligibility ladder (unify first-adoption retry with sticky) ---

func TestPollStateAdoptionEligibleWhenNoState(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)

	if !ps.shouldAttemptAdoption(testUID, testHash, 1) {
		t.Fatalf("expected adoption eligible when no state exists")
	}
}

func TestPollStateAdoptionRetriedAfterSingleFailure(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)

	// First adoption attempt fails: creates state with failures=1, etag="".
	if sticky := ps.recordFailure(testUID, testHash, 1); sticky {
		t.Fatalf("did not expect sticky after 1 adoption failure")
	}
	st, _ := ps.get(testUID)
	if st.failures != 1 || st.etag != "" {
		t.Fatalf("expected failures=1 etag=\"\" after first adoption failure, got %+v", st)
	}
	// The very next reconcile cycle must retry adoption, not give up.
	if !ps.shouldAttemptAdoption(testUID, testHash, 1) {
		t.Fatalf("expected adoption to be retried on the next cycle after a single failure")
	}
}

func TestPollStateAdoptionBlockedWhileStickyAfterThreeFailures(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)

	ps.recordFailure(testUID, testHash, 1)
	ps.recordFailure(testUID, testHash, 1)
	if sticky := ps.recordFailure(testUID, testHash, 1); !sticky {
		t.Fatalf("expected sticky after 3 adoption failures")
	}
	// Sticky engaged: adoption must not be attempted while inside the sticky window.
	if ps.shouldAttemptAdoption(testUID, testHash, 1) {
		t.Fatalf("expected adoption NOT eligible while sticky")
	}
}

func TestPollStateAdoptionEligibleAgainAfterStickyExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)

	ps.recordFailure(testUID, testHash, 1)
	ps.recordFailure(testUID, testHash, 1)
	ps.recordFailure(testUID, testHash, 1)

	// Just before expiry: still sticky, no adoption.
	now = now.Add(stickyBaseDuration - time.Second)
	if ps.shouldAttemptAdoption(testUID, testHash, 1) {
		t.Fatalf("expected adoption NOT eligible just before sticky expiry")
	}
	// After expiry: adoption is re-probed.
	now = now.Add(2 * time.Second)
	if !ps.shouldAttemptAdoption(testUID, testHash, 1) {
		t.Fatalf("expected adoption eligible again after sticky expiry")
	}
}

func TestPollStateAdoptionNotEligibleAfterSuccess(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)

	// A success clears failures and stores an etag; ongoing change detection now flows
	// through shouldPoll, so adoption must no longer be attempted.
	ps.recordSuccess(testUID, testEtag, testHash, 1)
	if ps.shouldAttemptAdoption(testUID, testHash, 1) {
		t.Fatalf("expected adoption NOT eligible after a successful adoption (etag present)")
	}
	if _, ok := ps.shouldPoll(testUID, testHash, 1); !ok {
		t.Fatalf("expected shouldPoll=true after a clean success")
	}
}

// TestPollStateAdoptionEligibleWhenUnsupportedAfterStickyExpiry is Finding 3: a 404/501
// must not permanently exclude a resource from adoption. This supersedes the old
// "NotEligibleWhenUnsupported" expectation from the now-removed permanent v4Supported flag:
// an operator released before the server-side v4 flag is turned on must self-heal once the
// flag flips on, which requires the adoption re-probe to run again after sticky expiry.
func TestPollStateAdoptionEligibleWhenUnsupportedAfterStickyExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)

	ps.recordUnsupported(testUID, testHash, 1)

	now = now.Add(stickyBaseDuration + time.Second)
	if !ps.shouldAttemptAdoption(testUID, testHash, 1) {
		t.Fatalf("expected adoption eligible again after sticky expiry, even after a 404/501 (self-heal re-probe)")
	}
}

// --- Finding 1: token-secret ResourceVersion folded into the identity hash ---

func TestComputeIdentityHashChangesWithTokenSecretResourceVersion(t *testing.T) {
	dopplerSecret := secretsv1alpha1.DopplerSecret{
		Spec: secretsv1alpha1.DopplerSecretSpec{
			Project:         "proj",
			Config:          "cfg",
			Format:          "",
			NameTransformer: "",
			TokenSecretRef:  secretsv1alpha1.TokenSecretReference{Name: "tok", Namespace: "ns"},
		},
	}

	hashV1 := computeIdentityHash(dopplerSecret, "procs-1", "rv-1")
	// In-place credential rotation: same Secret name/namespace, new value → new
	// ResourceVersion, but every other input identical.
	hashV2 := computeIdentityHash(dopplerSecret, "procs-1", "rv-2")

	if hashV1 == hashV2 {
		t.Fatalf("expected identity hash to change when token secret ResourceVersion changes")
	}

	// And it must be stable when the ResourceVersion is unchanged.
	if computeIdentityHash(dopplerSecret, "procs-1", "rv-1") != hashV1 {
		t.Fatalf("expected identity hash to be stable for identical inputs")
	}
}

// TestPollStateTokenRotationBypassesPoll proves the end-to-end effect of Finding 1: a
// changed token-secret ResourceVersion produces a different identityHash, which shouldPoll
// treats as an identity mismatch and rejects (same as the generation/identity tests above).
func TestPollStateTokenRotationBypassesPoll(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)

	dopplerSecret := secretsv1alpha1.DopplerSecret{
		Spec: secretsv1alpha1.DopplerSecretSpec{
			Project:        "proj",
			Config:         "cfg",
			TokenSecretRef: secretsv1alpha1.TokenSecretReference{Name: "tok", Namespace: "ns"},
		},
	}
	hashBefore := computeIdentityHash(dopplerSecret, "procs-1", "rv-1")
	ps.recordSuccess(testUID, testEtag, hashBefore, 1)

	// Same success is poll-eligible with the original identity hash...
	if _, ok := ps.shouldPoll(testUID, hashBefore, 1); !ok {
		t.Fatalf("expected shouldPoll=true before credential rotation")
	}

	// ...but after an in-place credential rotation (new ResourceVersion) the hash differs
	// and shouldPoll rejects, forcing a fresh fetch rather than trusting the stale etag.
	hashAfter := computeIdentityHash(dopplerSecret, "procs-1", "rv-2")
	if _, ok := ps.shouldPoll(testUID, hashAfter, 1); ok {
		t.Fatalf("expected shouldPoll=false after token-secret ResourceVersion change")
	}
}

// --- Finding 1: identity/generation mismatch resets stale state before counting, so
// sticky can actually engage again after a rotation ---

// TestPollStateRecordFailureAfterIdentityChangeEngagesStickyAfterThreeFailures proves the
// bug: without resetting stale state first, a stored identity/generation mismatch made
// recordFailure operate on state that would never itself flip to sticky the way a freshly
// adopted resource's failures would, because subsequent calls kept comparing against the
// OLD identity/generation forever (the entry was never updated to the new identity), so
// callers relying on identity-matched accrual never got a consistent count for the new
// identity. This test proves the fix: 3 consecutive failures against a NEW identity, after
// a prior success under an OLD identity, engages sticky and rebinds the entry to the new
// identity/generation.
func TestPollStateRecordFailureAfterIdentityChangeEngagesStickyAfterThreeFailures(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)

	// Prior success under the OLD identity (e.g. before a credential rotation).
	ps.recordSuccess(testUID, testEtag, "old-identity-hash", 1)

	newIdentity := "new-identity-hash"
	sticky := ps.recordFailure(testUID, newIdentity, 2)
	if sticky {
		t.Fatalf("did not expect sticky after 1 failure under the new identity")
	}
	st, _ := ps.get(testUID)
	if st.identityHash != newIdentity || st.generation != 2 {
		t.Fatalf("expected state rebound to the new identity/generation immediately, got %+v", st)
	}
	if st.failures != 1 {
		t.Fatalf("expected failures=1 after the reset+first failure, got %d", st.failures)
	}
	if st.etag != "" {
		t.Fatalf("expected etag cleared on identity reset, got %q", st.etag)
	}

	sticky = ps.recordFailure(testUID, newIdentity, 2)
	if sticky {
		t.Fatalf("did not expect sticky after 2 consecutive failures under the new identity")
	}
	sticky = ps.recordFailure(testUID, newIdentity, 2)
	if !sticky {
		t.Fatalf("expected sticky after 3 consecutive failures under the new identity")
	}

	st, _ = ps.get(testUID)
	if st.failures != 3 {
		t.Fatalf("expected failures=3, got %d", st.failures)
	}
	if _, ok := ps.shouldPoll(testUID, newIdentity, 2); ok {
		t.Fatalf("expected shouldPoll=false once sticky")
	}
}

// TestPollStateRecordUnsupportedAfterIdentityChangeRebindsState proves the same reset
// applies on the recordUnsupported (404/501) path.
func TestPollStateRecordUnsupportedAfterIdentityChangeRebindsState(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)

	ps.recordSuccess(testUID, testEtag, "old-identity-hash", 1)

	newIdentity := "new-identity-hash"
	ps.recordUnsupported(testUID, newIdentity, 2)

	st, _ := ps.get(testUID)
	if st.identityHash != newIdentity || st.generation != 2 {
		t.Fatalf("expected state rebound to the new identity/generation, got %+v", st)
	}
	if st.etag != "" {
		t.Fatalf("expected etag cleared on identity reset, got %q", st.etag)
	}
	if ps.nowFn().Before(st.stickyUntil) == false {
		t.Fatalf("expected sticky to be engaged immediately after recordUnsupported")
	}
}

// --- Finding 2: PollCurrent clears the consecutive-failure count ---

func TestPollStateRecordPollCurrentClearsFailures(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)
	ps.recordSuccess(testUID, testEtag, testHash, 1)

	// 2 failures, then a "current" poll, then 2 more failures must NOT be sticky: the 3rd
	// consecutive failure is required, and the intervening "current" reset the streak.
	if sticky := ps.recordFailure(testUID, testHash, 1); sticky {
		t.Fatalf("did not expect sticky after 1 failure")
	}
	if sticky := ps.recordFailure(testUID, testHash, 1); sticky {
		t.Fatalf("did not expect sticky after 2 failures")
	}

	ps.recordPollCurrent(testUID)
	st, _ := ps.get(testUID)
	if st.failures != 0 {
		t.Fatalf("expected failures reset to 0 after PollCurrent, got %d", st.failures)
	}
	// etag/identity/generation must be preserved (PollCurrent is not a resync).
	if st.etag != testEtag || st.identityHash != testHash || st.generation != 1 {
		t.Fatalf("expected etag/identity/generation preserved by recordPollCurrent, got %+v", st)
	}

	if sticky := ps.recordFailure(testUID, testHash, 1); sticky {
		t.Fatalf("did not expect sticky after 1 failure post-reset")
	}
	if sticky := ps.recordFailure(testUID, testHash, 1); sticky {
		t.Fatalf("expected NOT sticky: only 2 consecutive failures since the last PollCurrent, needs a 3rd")
	}

	st, _ = ps.get(testUID)
	if st.failures != 2 {
		t.Fatalf("expected failures=2, got %d", st.failures)
	}
	if _, ok := ps.shouldPoll(testUID, testHash, 1); !ok {
		t.Fatalf("expected shouldPoll=true: not yet sticky")
	}
}

func TestPollStateRecordPollCurrentNoOpWhenNoState(t *testing.T) {
	now := time.Unix(1000, 0)
	ps := newTestPollStates(&now)
	// Must not panic when called for a UID with no state.
	ps.recordPollCurrent(testUID)
	if _, ok := ps.get(testUID); ok {
		t.Fatalf("expected no state to be created by recordPollCurrent on a missing entry")
	}
}
