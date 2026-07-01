package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
)

// stubDopplerAPI is an httptest server that emulates the Doppler v3/v4 endpoints and
// records how many times the v4 download endpoint is hit, so tests can assert a poll
// "current" reconcile performed ZERO downloads.
type stubDopplerAPI struct {
	server           *httptest.Server
	v4Downloads      int32
	v3Downloads      int32
	pollRequests     int32
	pollStatus       string // "current" or "changed"
	pollUnavailable  bool   // when true, /v4/secrets/poll returns 503 to simulate an outage
	v4DownloadStatus int    // when non-zero, /v4/configs/config/secrets/download returns this status
	secretsPayload   map[string]string
	pollETag         string
	v3ETag           string
}

func newStubDopplerAPI() *stubDopplerAPI {
	s := &stubDopplerAPI{
		pollStatus:     "current",
		secretsPayload: map[string]string{"HELLO": "world", "DOPPLER_PROJECT": "proj", "DOPPLER_CONFIG": "cfg"},
		pollETag:       "poll-etag-1",
		v3ETag:         "v3-etag-1",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v4/secrets/poll", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.pollRequests, 1)
		if s.pollUnavailable {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// Status-code-only wire protocol: no response body. 304 == current (etag's
		// epoch still matches), 200 == changed.
		switch s.pollStatus {
		case "current":
			w.WriteHeader(http.StatusNotModified)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	mux.HandleFunc("/v4/configs/config/secrets/download", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.v4Downloads, 1)
		if s.v4DownloadStatus != 0 {
			w.WriteHeader(s.v4DownloadStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Poll-ETag", s.pollETag)
		_ = json.NewEncoder(w).Encode(s.secretsPayload)
	})
	mux.HandleFunc("/v3/configs/config/secrets/download", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.v3Downloads, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", s.v3ETag)
		_ = json.NewEncoder(w).Encode(s.secretsPayload)
	})
	s.server = httptest.NewServer(mux)
	return s
}

func (s *stubDopplerAPI) Close() { s.server.Close() }

var _ = Describe("Poll-first reconcile", func() {
	const (
		ns             = "default"
		tokenName      = "poll-token"
		managedName    = "poll-managed"
		dopplerSecName = "poll-dopplersecret"
	)

	var (
		ctx     context.Context
		stub    *stubDopplerAPI
		rec     *DopplerSecretReconciler
		dsecret secretsv1alpha1.DopplerSecret
	)

	BeforeEach(func() {
		ctx = context.Background()
		stub = newStubDopplerAPI()

		// Token secret with a service token.
		tokenSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: tokenName, Namespace: ns},
			Data:       map[string][]byte{"serviceToken": []byte("dp.st.fake")},
		}
		Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())

		dsecret = secretsv1alpha1.DopplerSecret{
			ObjectMeta: metav1.ObjectMeta{Name: dopplerSecName, Namespace: ns},
			Spec: secretsv1alpha1.DopplerSecretSpec{
				Host:             stub.server.URL,
				VerifyTLS:        false,
				TokenSecretRef:   secretsv1alpha1.TokenSecretReference{Name: tokenName, Namespace: ns},
				ManagedSecretRef: secretsv1alpha1.ManagedSecretReference{Name: managedName, Namespace: ns, Type: string(corev1.SecretTypeOpaque)},
			},
		}
		Expect(k8sClient.Create(ctx, &dsecret)).To(Succeed())
		// Fetch to populate UID/generation.
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dopplerSecName, Namespace: ns}, &dsecret)).To(Succeed())

		rec = &DopplerSecretReconciler{
			Client:     k8sClient,
			Log:        ctrllog.Log,
			Scheme:     k8sClient.Scheme(),
			PollStates: newPollStates(),
		}
	})

	AfterEach(func() {
		stub.Close()
		_ = k8sClient.Delete(ctx, &dsecret)
		managed := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: managedName, Namespace: ns}}
		_ = k8sClient.Delete(ctx, managed)
		token := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: tokenName, Namespace: ns}}
		_ = k8sClient.Delete(ctx, token)
	})

	It("does not rewrite the managed secret or hit the v4 download endpoint on a poll-current reconcile", func() {
		// First reconcile: v4 adoption downloads secrets and stores an etag.
		Expect(rec.UpdateSecret(ctx, dsecret)).To(Succeed())
		Expect(atomic.LoadInt32(&stub.v4Downloads)).To(Equal(int32(1)), "adoption should perform exactly one v4 download")

		managed := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: managedName, Namespace: ns}, managed)).To(Succeed())
		firstResourceVersion := managed.ResourceVersion
		Expect(managed.Data).To(HaveKeyWithValue("HELLO", []byte("world")))

		// Second reconcile: poll reports "current".
		stub.pollStatus = "current"
		downloadsBefore := atomic.LoadInt32(&stub.v4Downloads)

		// Re-fetch to get the current managed secret for the reconcile input.
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dopplerSecName, Namespace: ns}, &dsecret)).To(Succeed())
		Expect(rec.UpdateSecret(ctx, dsecret)).To(Succeed())

		// The poll endpoint was consulted...
		Expect(atomic.LoadInt32(&stub.pollRequests)).To(BeNumerically(">=", 1))
		// ...but ZERO additional v4 downloads happened.
		Expect(atomic.LoadInt32(&stub.v4Downloads)).To(Equal(downloadsBefore), "poll-current must not download")
		// ...and no v3 download either.
		Expect(atomic.LoadInt32(&stub.v3Downloads)).To(Equal(int32(0)))

		// The managed secret was NOT rewritten.
		managedAfter := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: managedName, Namespace: ns}, managedAfter)).To(Succeed())
		Expect(managedAfter.ResourceVersion).To(Equal(firstResourceVersion), "managed secret must not be rewritten on poll-current")
	})

	It("downloads via v4 and rewrites the managed secret on a poll-changed reconcile", func() {
		Expect(rec.UpdateSecret(ctx, dsecret)).To(Succeed())
		downloadsAfterAdoption := atomic.LoadInt32(&stub.v4Downloads)

		// After the v4 adoption write, the version annotation must be non-empty (C1:
		// the v4 write path mints a fresh random version rather than leaving the
		// always-empty v4 ETag, which would break deployment auto-reload. On later
		// writes this random version is carried forward unchanged whenever the newly
		// fetched data is byte-identical to the existing managed Secret's data, so a
		// reload only fires when content actually changes — see
		// computeV4VersionAnnotation).
		managedBefore := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: managedName, Namespace: ns}, managedBefore)).To(Succeed())
		versionBefore := managedBefore.Annotations[kubeSecretVersionAnnotation]
		Expect(versionBefore).ToNot(BeEmpty(), "v4 write must set a non-empty version annotation")

		stub.pollStatus = "changed"
		stub.secretsPayload = map[string]string{"HELLO": "planet", "DOPPLER_PROJECT": "proj", "DOPPLER_CONFIG": "cfg"}

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dopplerSecName, Namespace: ns}, &dsecret)).To(Succeed())
		Expect(rec.UpdateSecret(ctx, dsecret)).To(Succeed())

		Expect(atomic.LoadInt32(&stub.v4Downloads)).To(Equal(downloadsAfterAdoption+1), "poll-changed must download once")
		managed := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: managedName, Namespace: ns}, managed)).To(Succeed())
		Expect(managed.Data).To(HaveKeyWithValue("HELLO", []byte("planet")))

		// The version annotation must be non-empty AND differ from before, so
		// ReconcileDeployment sees a change and triggers a reload.
		versionAfter := managed.Annotations[kubeSecretVersionAnnotation]
		Expect(versionAfter).ToNot(BeEmpty(), "v4 write must set a non-empty version annotation")
		Expect(versionAfter).ToNot(Equal(versionBefore), "version annotation must change when payload changes")
	})

	// --- Finding 2 (Pass 2): after v4 adoption the version annotation is a local random
	// value, not a server ETag. If a subsequent poll outage falls back to v3 for a single
	// cycle, the v3 endpoint never matches that local value via If-None-Match and always
	// responds Modified=true, even when the underlying secret content is unchanged. Without a
	// data-equality guard, UpdateManagedSecret would rewrite the version annotation to the v3
	// ETag on every such transition and fire a spurious deployment reload. ---

	It("does not rewrite the managed secret when a v4-adopted resource falls back to v3 during a poll outage and data is unchanged", func() {
		// First reconcile: v4 adoption downloads secrets and stores a random version.
		Expect(rec.UpdateSecret(ctx, dsecret)).To(Succeed())
		Expect(atomic.LoadInt32(&stub.v4Downloads)).To(Equal(int32(1)))

		managed := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: managedName, Namespace: ns}, managed)).To(Succeed())
		firstResourceVersion := managed.ResourceVersion
		versionBefore := managed.Annotations[kubeSecretVersionAnnotation]
		Expect(versionBefore).ToNot(BeEmpty())

		// Simulate a poll outage: the poll endpoint is unavailable, so reconcilePollFirst
		// falls through to the v3 flow for this cycle. The v3 payload is UNCHANGED.
		stub.pollUnavailable = true

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dopplerSecName, Namespace: ns}, &dsecret)).To(Succeed())
		Expect(rec.UpdateSecret(ctx, dsecret)).To(Succeed())

		// The v3 download endpoint was hit (poll was unavailable, so it fell back)...
		Expect(atomic.LoadInt32(&stub.v3Downloads)).To(Equal(int32(1)), "poll outage must fall back to v3")

		// ...but the managed Secret must be untouched: no rewrite, no reload, and the
		// version annotation must remain the original v4 random value (NOT the v3 ETag).
		managedAfter := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: managedName, Namespace: ns}, managedAfter)).To(Succeed())
		Expect(managedAfter.ResourceVersion).To(Equal(firstResourceVersion), "managed secret must not be rewritten when v3 fallback data is unchanged")
		Expect(managedAfter.Annotations[kubeSecretVersionAnnotation]).To(Equal(versionBefore), "version annotation must not be overwritten with the v3 ETag when data is unchanged")
	})

	It("rewrites the managed secret when a v4-adopted resource falls back to v3 during a poll outage and data has changed", func() {
		// First reconcile: v4 adoption downloads secrets and stores a random version.
		Expect(rec.UpdateSecret(ctx, dsecret)).To(Succeed())
		Expect(atomic.LoadInt32(&stub.v4Downloads)).To(Equal(int32(1)))

		managed := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: managedName, Namespace: ns}, managed)).To(Succeed())
		versionBefore := managed.Annotations[kubeSecretVersionAnnotation]
		Expect(versionBefore).ToNot(BeEmpty())

		// Simulate a poll outage AND a real content change on the v3 payload.
		stub.pollUnavailable = true
		stub.secretsPayload = map[string]string{"HELLO": "planet", "DOPPLER_PROJECT": "proj", "DOPPLER_CONFIG": "cfg"}
		stub.v3ETag = "v3-etag-2"

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dopplerSecName, Namespace: ns}, &dsecret)).To(Succeed())
		Expect(rec.UpdateSecret(ctx, dsecret)).To(Succeed())

		Expect(atomic.LoadInt32(&stub.v3Downloads)).To(Equal(int32(1)), "poll outage must fall back to v3")

		// The managed Secret MUST be rewritten: new data, and the annotation becomes the v3
		// ETag (so ReconcileDeployment sees a change and a reload fires, as intended when
		// content genuinely changed).
		managedAfter := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: managedName, Namespace: ns}, managedAfter)).To(Succeed())
		Expect(managedAfter.Data).To(HaveKeyWithValue("HELLO", []byte("planet")))
		Expect(managedAfter.Annotations[kubeSecretVersionAnnotation]).To(Equal("v3-etag-2"), "version annotation must become the v3 ETag when data actually changed")
		Expect(managedAfter.Annotations[kubeSecretVersionAnnotation]).ToNot(Equal(versionBefore))
	})

	// --- 404-vs-501 sticky distinction: a 404 (config not found) is transient and must NOT
	// pin the resource to v3-only sticky mode, while a 501 (no v4 route) is durable and must.
	// Both fall through to v3 for the cycle so the reconcile never breaks. ---

	It("does NOT enter v3-only sticky mode when v4 adoption gets a 404 (config not found)", func() {
		stub.v4DownloadStatus = http.StatusNotFound

		Expect(rec.UpdateSecret(ctx, dsecret)).To(Succeed())

		// v4 was attempted (adoption) and 404'd, so this cycle falls through to v3.
		Expect(atomic.LoadInt32(&stub.v4Downloads)).To(Equal(int32(1)), "adoption should attempt one v4 download")
		Expect(atomic.LoadInt32(&stub.v3Downloads)).To(Equal(int32(1)), "404 must fall back to v3 for this cycle")

		// A single 404 records a non-sticky failure: the resource must NOT be sticky and must
		// still be eligible to re-attempt v4 adoption next cycle.
		st, ok := rec.PollStates.get(dsecret.UID)
		Expect(ok).To(BeTrue(), "a state entry should exist after a 404 failure")
		Expect(st.stickyUntil.IsZero()).To(BeTrue(), "a single 404 must NOT flip the resource to sticky v3-only mode")
		Expect(rec.PollStates.shouldAttemptAdoption(dsecret.UID, st.identityHash, dsecret.Generation)).To(BeTrue(), "404 must leave the resource eligible to retry v4")
	})

	It("enters v3-only sticky mode when v4 adoption gets a 501 (no v4 route)", func() {
		stub.v4DownloadStatus = http.StatusNotImplemented

		Expect(rec.UpdateSecret(ctx, dsecret)).To(Succeed())

		// v4 was attempted (adoption) and 501'd, so this cycle falls through to v3.
		Expect(atomic.LoadInt32(&stub.v4Downloads)).To(Equal(int32(1)), "adoption should attempt one v4 download")
		Expect(atomic.LoadInt32(&stub.v3Downloads)).To(Equal(int32(1)), "501 must fall back to v3 for this cycle")

		// A 501 records unsupported: the resource must immediately be sticky (v3-only) and
		// must NOT re-attempt v4 adoption while sticky.
		st, ok := rec.PollStates.get(dsecret.UID)
		Expect(ok).To(BeTrue(), "a state entry should exist after a 501")
		Expect(st.stickyUntil.IsZero()).To(BeFalse(), "a 501 must flip the resource to sticky v3-only mode")
		Expect(rec.PollStates.shouldAttemptAdoption(dsecret.UID, st.identityHash, dsecret.Generation)).To(BeFalse(), "501 must exclude the resource from v4 while sticky")
	})
})
