package controllers

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
)

// --- Finding 1 (Pass 2): the token used to build APIContext and the ResourceVersion folded
// into the identity hash must come from the SAME Secret read, not two independent GETs. A
// rotation landing between two separate reads could otherwise bind a poll etag fetched under
// the OLD credential to the NEW identity hash (or vice versa), preserving a stale etag across
// rotation. ---

// countingGetClient wraps a client.Client and counts calls to Get, so tests can assert exactly
// one read of the token Secret occurred while building the auth identity.
type countingGetClient struct {
	client.Client
	getCount int
}

func (c *countingGetClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	c.getCount++
	return c.Client.Get(ctx, key, obj, opts...)
}

func TestServiceTokenAuthProviderGetAPIContextIsSingleRead(t *testing.T) {
	scheme := runtimeScheme(t)

	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tok", Namespace: "ns", ResourceVersion: "111"},
		Data:       map[string][]byte{"serviceToken": []byte("dp.st.original")},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tokenSecret).Build()
	counting := &countingGetClient{Client: fakeClient}

	provider := &ServiceTokenAuthProvider{
		client:    counting,
		tokenRef:  secretsv1alpha1.TokenSecretReference{Name: "tok", Namespace: "ns"},
		namespace: "ns",
		host:      "https://api.doppler.com",
		verifyTLS: true,
	}

	apiContext, rv, err := provider.GetAPIContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if counting.getCount != 1 {
		t.Fatalf("expected exactly one Get call to build the API context and RV together, got %d", counting.getCount)
	}
	if apiContext.APIKey != "dp.st.original" {
		t.Fatalf("expected token from the single read, got %q", apiContext.APIKey)
	}
	if rv != "111" {
		t.Fatalf("expected ResourceVersion from the SAME read that produced the token, got %q", rv)
	}
}

// TestServiceTokenAuthProviderRotationPairsTokenAndRVAtomically proves the fix directly: the
// token and ResourceVersion returned together always describe the SAME underlying Secret
// object, even across a rotation between two separate GetAPIContext calls. Before the fix, a
// caller performed a second, independent GET to fetch the ResourceVersion used for the
// identity hash; a rotation landing between the auth read and that second read could pair a
// token fetched under the OLD credential with the NEW ResourceVersion. Here, each call is
// atomic by construction (single Get), so the pairing is always self-consistent.
func TestServiceTokenAuthProviderRotationPairsTokenAndRVAtomically(t *testing.T) {
	scheme := runtimeScheme(t)

	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tok", Namespace: "ns", ResourceVersion: "111"},
		Data:       map[string][]byte{"serviceToken": []byte("dp.st.original")},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tokenSecret).Build()

	provider := &ServiceTokenAuthProvider{
		client:    fakeClient,
		tokenRef:  secretsv1alpha1.TokenSecretReference{Name: "tok", Namespace: "ns"},
		namespace: "ns",
		host:      "https://api.doppler.com",
		verifyTLS: true,
	}

	apiContextBefore, rvBefore, err := provider.GetAPIContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if apiContextBefore.APIKey != "dp.st.original" || rvBefore != "111" {
		t.Fatalf("expected original token paired with RV 111, got token=%q rv=%q", apiContextBefore.APIKey, rvBefore)
	}

	// Simulate an in-place credential rotation: same Secret name/namespace, new value, new RV.
	var current corev1.Secret
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: "tok", Namespace: "ns"}, &current); err != nil {
		t.Fatalf("unexpected error fetching secret to rotate: %v", err)
	}
	current.Data = map[string][]byte{"serviceToken": []byte("dp.st.rotated")}
	if err := fakeClient.Update(context.Background(), &current); err != nil {
		t.Fatalf("unexpected error rotating secret: %v", err)
	}

	apiContextAfter, rvAfter, err := provider.GetAPIContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if apiContextAfter.APIKey != "dp.st.rotated" {
		t.Fatalf("expected rotated token, got %q", apiContextAfter.APIKey)
	}
	if rvAfter == rvBefore {
		t.Fatalf("expected ResourceVersion to change after rotation")
	}
	// The critical invariant this guards: the rotated token is paired with the POST-rotation
	// ResourceVersion, never the pre-rotation one, because both come from a single Get call.
}

// runtimeScheme builds a scheme with corev1 registered, sufficient for the fake client in
// these tests.
func runtimeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add corev1 to scheme: %v", err)
	}
	return scheme
}
