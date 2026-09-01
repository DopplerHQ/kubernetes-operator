package controllers

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
)

// forbiddenUpdate is what the API server returns when the operator's ClusterRole lacks the
// update verb on deployments.
func forbiddenUpdate(name string) error {
	return apierrors.NewForbidden(
		schema.GroupResource{Group: "apps", Resource: "deployments"}, name,
		fmt.Errorf(`User "system:serviceaccount:doppler-operator-system:doppler-operator-controller-manager" cannot update resource "deployments" in API group "apps"`))
}

func reloadDeployment(name string) v1.Deployment {
	return v1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "app",
			Annotations: map[string]string{deploymentRestartAnnotation: "true"},
		},
		Spec: v1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "app",
						EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "creds"}}}},
					}},
				},
			},
		},
	}
}

// reloadReconciler builds a reconciler over the given deployments plus the managed secret
// they reference. updateErr, when non-nil, is returned for every Update.
func reloadReconciler(t *testing.T, deployments []v1.Deployment, updateErr func(string) error) *DopplerSecretReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}

	objects := []client.Object{&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "creds",
			Namespace:   "app",
			Annotations: map[string]string{kubeSecretVersionAnnotation: "v2"},
			Labels:      map[string]string{ManagedSecretLabelKey: ManagedSecretLabelValue},
		},
	}}
	for i := range deployments {
		objects = append(objects, &deployments[i])
	}

	builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...)
	if updateErr != nil {
		builder = builder.WithInterceptorFuncs(interceptor.Funcs{
			Update: func(_ context.Context, _ client.WithWatch, obj client.Object,
				_ ...client.UpdateOption) error {
				return updateErr(obj.GetName())
			},
		})
	}

	// Discarded rather than captured: ReconcileDeployment logs from each goroutine, so a
	// capturing logger would need its own synchronisation to be race-free, and these tests
	// assert on the returned error rather than on log output.
	return &DopplerSecretReconciler{
		Client: builder.Build(),
		Log:    logr.Discard(),
	}
}

func reloadDopplerSecret() secretsv1alpha1.DopplerSecret {
	return secretsv1alpha1.DopplerSecret{
		ObjectMeta: metav1.ObjectMeta{Name: "ds", Namespace: "app"},
		Spec: secretsv1alpha1.DopplerSecretSpec{
			ManagedSecretRef: secretsv1alpha1.ManagedSecretReference{Name: "creds"},
		},
	}
}

// A deployment write that fails must reach the caller: DeploymentReloadReady is derived from
// this error, so a dropped failure reads as a healthy reload path while nothing restarts.
func TestReconcileDeploymentsPropagatesWriteFailures(t *testing.T) {
	r := reloadReconciler(t, []v1.Deployment{reloadDeployment("web")}, forbiddenUpdate)

	_, err := r.ReconcileDeploymentsUsingSecret(context.Background(), reloadDopplerSecret())
	if err == nil {
		t.Fatal("a forbidden deployment update must be propagated, not swallowed: " +
			"DeploymentReloadReady is derived from this error")
	}
	if !strings.Contains(err.Error(), "web") {
		t.Errorf("error should name the failing deployment, got: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot update resource") {
		t.Errorf("error should carry the underlying cause, got: %v", err)
	}
	if !strings.Contains(err.Error(), "1 of 1") {
		t.Errorf("error should report how many deployments failed, got: %v", err)
	}
}

// The success path must stay clean, or every reconcile would requeue with an error.
func TestReconcileDeploymentsReturnsNilWhenUpdatesSucceed(t *testing.T) {
	r := reloadReconciler(t, []v1.Deployment{reloadDeployment("web"), reloadDeployment("api")}, nil)

	numDeployments, err := r.ReconcileDeploymentsUsingSecret(context.Background(), reloadDopplerSecret())
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if numDeployments != 2 {
		t.Errorf("expected 2 deployments, got %d", numDeployments)
	}
}

// Only deployments actually using the secret count toward the failure denominator; an
// unrelated deployment in the same namespace is never written to and must not appear.
func TestReconcileDeploymentsCountsOnlyMatchingDeployments(t *testing.T) {
	unrelated := reloadDeployment("unrelated")
	unrelated.Spec.Template.Spec.Containers[0].EnvFrom = nil

	r := reloadReconciler(t, []v1.Deployment{reloadDeployment("web"), unrelated}, forbiddenUpdate)

	_, err := r.ReconcileDeploymentsUsingSecret(context.Background(), reloadDopplerSecret())
	if err == nil {
		t.Fatal("expected the matching deployment's failure to propagate")
	}
	if !strings.Contains(err.Error(), "1 of 1") {
		t.Errorf("only the deployment using the secret should be counted, got: %v", err)
	}
	if strings.Contains(err.Error(), "unrelated") {
		t.Errorf("a deployment not using the secret must not appear, got: %v", err)
	}
}

// The message lands in a status condition, so it is capped and ordered independently of the
// order the goroutines happen to finish in.
func TestAggregateDeploymentFailuresIsCappedAndDeterministic(t *testing.T) {
	failures := []deploymentFailure{
		{name: "delta", err: forbiddenUpdate("delta")},
		{name: "alpha", err: forbiddenUpdate("alpha")},
		{name: "charlie", err: forbiddenUpdate("charlie")},
		{name: "bravo", err: forbiddenUpdate("bravo")},
	}

	err := aggregateDeploymentFailures(failures, 10)
	if err == nil {
		t.Fatal("expected an error")
	}
	got := err.Error()

	if !strings.Contains(got, "4 of 10 deployments") {
		t.Errorf("expected the full failure count, got: %v", got)
	}
	if !strings.Contains(got, "1 more not shown") {
		t.Errorf("expected the truncation to be disclosed, got: %v", got)
	}
	// Sorted, so the three quoted are the alphabetically first and delta is the one hidden.
	for _, want := range []string{"alpha", "bravo", "charlie"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q to be quoted, got: %v", want, got)
		}
	}
	if strings.Contains(got, "delta") {
		t.Errorf("delta sorts last and should have been truncated, got: %v", got)
	}

	// Same failures in a different order must produce the same message.
	shuffled := []deploymentFailure{failures[3], failures[0], failures[2], failures[1]}
	if other := aggregateDeploymentFailures(shuffled, 10); other.Error() != got {
		t.Errorf("message depends on completion order:\n %q\n %q", got, other.Error())
	}
}

func TestAggregateDeploymentFailuresNilWhenEmpty(t *testing.T) {
	if err := aggregateDeploymentFailures(nil, 3); err != nil {
		t.Errorf("expected nil for no failures, got: %v", err)
	}
}
