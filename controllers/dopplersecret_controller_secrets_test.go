package controllers

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// emptyCache stands in for the filtered cache when the secret being read does not carry
// the opt-in label, which is what the cache does: it cannot see it, so it reports NotFound.
type emptyCache struct{}

func (emptyCache) Get(_ context.Context, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, key.Name)
}

func (emptyCache) List(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	return nil
}

func tokenSecretReconciler(t *testing.T, secret *corev1.Secret, logged *[]string) (*DopplerSecretReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	api := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	log := funcr.New(func(prefix, args string) { *logged = append(*logged, args) }, funcr.Options{})
	return &DopplerSecretReconciler{
		Client:             api,
		APIReader:          api,
		CachedSecretReader: emptyCache{},
		Log:                log,
	}, api
}

// The operator must never write to a token secret. Users create these, and often manage
// them with Terraform, Argo CD or Flux, where an injected label shows up as drift and gets
// reverted. Caching is opt-in precisely so that this stays true.
func TestGetTokenSecretNeverWritesToTheSecret(t *testing.T) {
	ctx := context.Background()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "doppler-token-secret", Namespace: "doppler-operator-system"},
		Data:       map[string][]byte{kubeSecretServiceTokenKey: []byte("dp.st.dev.example")},
	}
	var logged []string
	r, api := tokenSecretReconciler(t, secret, &logged)
	name := types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}

	for i := 0; i < 3; i++ {
		got, err := r.GetTokenSecret(ctx, name)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if string(got.Data[kubeSecretServiceTokenKey]) != "dp.st.dev.example" {
			t.Fatalf("read %d returned the wrong secret", i)
		}
	}

	stored := &corev1.Secret{}
	if err := api.Get(ctx, name, stored); err != nil {
		t.Fatalf("re-reading the secret: %v", err)
	}
	if len(stored.Labels) != 0 {
		t.Errorf("operator labelled a token secret it does not own: %v", stored.Labels)
	}
	if len(stored.Annotations) != 0 {
		t.Errorf("operator annotated a token secret it does not own: %v", stored.Annotations)
	}
}

// The hint that points at the opt-in label is logged once per secret, not once per
// reconcile, so a 60 second resync across many DopplerSecrets does not fill the log.
func TestGetTokenSecretHintsOncePerSecret(t *testing.T) {
	ctx := context.Background()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "doppler-token-secret", Namespace: "doppler-operator-system"},
		Data:       map[string][]byte{kubeSecretServiceTokenKey: []byte("dp.st.dev.example")},
	}
	var logged []string
	r, _ := tokenSecretReconciler(t, secret, &logged)
	name := types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}

	for i := 0; i < 5; i++ {
		if _, err := r.GetTokenSecret(ctx, name); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}

	hints := 0
	for _, line := range logged {
		if strings.Contains(line, TokenSecretLabelValue) {
			hints++
		}
	}
	if hints != 1 {
		t.Errorf("expected exactly one hint across 5 reads, got %d: %v", hints, logged)
	}
}

// With no cache configured at all (--enable-secret-cache=false) the read still works and
// still writes nothing.
func TestGetTokenSecretWithCachingDisabled(t *testing.T) {
	ctx := context.Background()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "doppler-token-secret", Namespace: "doppler-operator-system"},
		Data:       map[string][]byte{kubeSecretServiceTokenKey: []byte("dp.st.dev.example")},
	}
	var logged []string
	r, api := tokenSecretReconciler(t, secret, &logged)
	r.CachedSecretReader = nil
	name := types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}

	got, err := r.GetTokenSecret(ctx, name)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got.Data[kubeSecretServiceTokenKey]) != "dp.st.dev.example" {
		t.Error("returned the wrong secret")
	}

	stored := &corev1.Secret{}
	if err := api.Get(ctx, name, stored); err != nil {
		t.Fatalf("re-reading the secret: %v", err)
	}
	if len(stored.Labels) != 0 {
		t.Errorf("operator labelled a token secret it does not own: %v", stored.Labels)
	}
}
