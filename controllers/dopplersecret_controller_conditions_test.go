package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
)

// statusReconciler builds a reconciler over a DopplerSecret that syncs from a stub Doppler API,
// and counts the status writes made through it.
func statusReconciler(t *testing.T, withTokenSecret bool) (*DopplerSecretReconciler, client.Client, *int) {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "doppler-operator-system")

	doppler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", "v1")
		_, _ = w.Write([]byte(`{"API_KEY":"value"}`))
	}))
	t.Cleanup(doppler.Close)

	scheme := testScheme(t)
	if err := secretsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	dopplerSecret := &secretsv1alpha1.DopplerSecret{
		ObjectMeta: metav1.ObjectMeta{Name: "ds", Namespace: "app"},
		Spec: secretsv1alpha1.DopplerSecretSpec{
			Host:             doppler.URL,
			TokenSecretRef:   secretsv1alpha1.TokenSecretReference{Name: "token"},
			ManagedSecretRef: secretsv1alpha1.ManagedSecretReference{Name: "creds", Type: string(corev1.SecretTypeOpaque)},
		},
	}
	objects := []client.Object{dopplerSecret}
	if withTokenSecret {
		objects = append(objects, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "app"},
			Data:       map[string][]byte{kubeSecretServiceTokenKey: []byte("dp.st.test")},
		})
	}

	writes := 0
	api := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(objects...).WithStatusSubresource(dopplerSecret).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				writes++
				return c.SubResource(subResource).Update(ctx, obj, opts...)
			},
		}).Build()
	return &DopplerSecretReconciler{Client: api, Log: logr.Discard()}, api, &writes
}

func reconcileAndGetConditions(t *testing.T, r *DopplerSecretReconciler, api client.Client) []metav1.Condition {
	t.Helper()
	key := client.ObjectKey{Namespace: "app", Name: "ds"}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	stored := &secretsv1alpha1.DopplerSecret{}
	if err := api.Get(context.Background(), key, stored); err != nil {
		t.Fatalf("get: %v", err)
	}
	return stored.Status.Conditions
}

// Reconcile must write status once. A second write's watch event can start the next reconcile
// before the cache has the second write, and that reconcile's write then conflicts.
func TestReconcileWritesStatusOnce(t *testing.T) {
	r, api, writes := statusReconciler(t, true)

	conditions := reconcileAndGetConditions(t, r, api)

	if *writes != 1 {
		t.Errorf("expected 1 status write, got %d", *writes)
	}
	if !meta.IsStatusConditionTrue(conditions, "secrets.doppler.com/SecretSyncReady") ||
		!meta.IsStatusConditionTrue(conditions, "secrets.doppler.com/DeploymentReloadReady") {
		t.Errorf("expected both conditions True, got %+v", conditions)
	}
}

func TestReconcileWritesStatusOnceWhenSyncFails(t *testing.T) {
	r, api, writes := statusReconciler(t, false)

	conditions := reconcileAndGetConditions(t, r, api)

	if *writes != 1 {
		t.Errorf("expected 1 status write, got %d", *writes)
	}
	if !meta.IsStatusConditionFalse(conditions, "secrets.doppler.com/SecretSyncReady") ||
		!meta.IsStatusConditionFalse(conditions, "secrets.doppler.com/DeploymentReloadReady") {
		t.Errorf("expected both conditions False, got %+v", conditions)
	}
}
