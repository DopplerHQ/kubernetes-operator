package controllers

import (
	"context"
	"errors"
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

// emptyCache stands in for the filtered cache reading a secret without the opt-in label.
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

// The operator must never write to a token secret, since the user owns it.
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

// The opt-in hint is logged once per secret, not once per reconcile.
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

// With --enable-secret-cache=false the read still works and still writes nothing.
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

// failingCache stands in for a filtered cache that cannot answer, such as one not yet synced.
type failingCache struct{}

func (failingCache) Get(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return errors.New("cache not synced")
}

func (failingCache) List(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	return errors.New("cache not synced")
}

// A cache error says nothing about whether the secret is labelled, so it must not
// produce the opt-in hint.
func TestGetTokenSecretDoesNotHintOnCacheError(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "doppler-token-secret", Namespace: "doppler-operator-system"},
		Data:       map[string][]byte{kubeSecretServiceTokenKey: []byte("dp.st.dev.example")},
	}
	var logged []string
	r, _ := tokenSecretReconciler(t, secret, &logged)
	r.CachedSecretReader = failingCache{}

	if _, err := r.GetTokenSecret(context.Background(), types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}); err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, line := range logged {
		if strings.Contains(line, TokenSecretLabelValue) {
			t.Errorf("hint logged for a cache error: %v", logged)
		}
	}
}
