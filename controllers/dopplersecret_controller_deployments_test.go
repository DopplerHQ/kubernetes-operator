package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return scheme
}

// reloadReconciler builds a reconciler over the given deployments plus the managed secret
// they reference. updateErr, when non-nil, is returned for every Update.
func reloadReconciler(t *testing.T, deployments []v1.Deployment, updateErr func(string) error) *DopplerSecretReconciler {
	t.Helper()
	objects := []client.Object{&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "creds",
			Namespace:   "app",
			Annotations: map[string]string{kubeSecretVersionAnnotation: "v2"},
			Labels:      map[string]string{SubtypeLabelKey: ManagedSecretLabelValue},
		},
	}}
	for i := range deployments {
		objects = append(objects, &deployments[i])
	}

	builder := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objects...)
	if updateErr != nil {
		builder = builder.WithInterceptorFuncs(interceptor.Funcs{
			Update: func(_ context.Context, _ client.WithWatch, obj client.Object,
				_ ...client.UpdateOption) error {
				return updateErr(obj.GetName())
			},
		})
	}

	// ReconcileDeployment logs from goroutines, and these tests check the returned error.
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

// A failed deployment write must reach the caller, since DeploymentReloadReady is derived from it.
func TestReconcileDeploymentsPropagatesWriteFailures(t *testing.T) {
	r := reloadReconciler(t, []v1.Deployment{reloadDeployment("web")}, forbiddenUpdate)

	_, err := r.ReconcileDeploymentsUsingSecret(context.Background(), reloadDopplerSecret())
	if err == nil {
		t.Fatal("expected the forbidden update to be returned")
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

// Only deployments using the secret count toward the total.
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

// The status message is capped, and does not depend on the order goroutines finish in.
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
	for _, want := range []string{"alpha", "bravo", "charlie"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q to be quoted, got: %v", want, got)
		}
	}
	if strings.Contains(got, "delta") {
		t.Errorf("delta sorts last and should have been truncated, got: %v", got)
	}

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

// listReconciler builds a reconciler whose Deployment List is handled by listFn, standing in
// for the cached read.
func listReconciler(t *testing.T, listFn func(context.Context) error) *DopplerSecretReconciler {
	t.Helper()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "creds",
		Namespace: "app",
		Labels:    map[string]string{SubtypeLabelKey: ManagedSecretLabelValue},
	}}

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(secret).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, _ client.WithWatch, _ client.ObjectList,
				_ ...client.ListOption) error {
				return listFn(ctx)
			},
		}).Build()

	return &DopplerSecretReconciler{Client: c, Log: logr.Discard()}
}

func TestReconcileDeploymentsBoundsTheCachedList(t *testing.T) {
	var (
		sawDeadline bool
		budget      time.Duration
	)
	r := listReconciler(t, func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		sawDeadline = ok
		if ok {
			budget = time.Until(deadline)
		}
		return nil
	})

	if _, err := r.ReconcileDeploymentsUsingSecret(context.Background(), reloadDopplerSecret()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sawDeadline {
		t.Fatal("the cached deployment List was given a context with no deadline")
	}
	if budget <= 0 || budget > deploymentListTimeout {
		t.Errorf("deadline should be within deploymentListTimeout (%v), got %v", deploymentListTimeout, budget)
	}
}

// An informer that never syncs, under a reconcile context with no deadline of its own,
// must produce an error rather than block.
func TestReconcileDeploymentsReturnsWhenTheCacheNeverSyncs(t *testing.T) {
	original := deploymentListTimeout
	deploymentListTimeout = 150 * time.Millisecond
	defer func() { deploymentListTimeout = original }()

	r := listReconciler(t, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	done := make(chan error, 1)
	go func() {
		_, err := r.ReconcileDeploymentsUsingSecret(context.Background(), reloadDopplerSecret())
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an unsyncable cache must produce an error")
		}
		if !strings.Contains(err.Error(), "Unable to fetch deployments") {
			t.Errorf("error should identify the failing read, got: %v", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error should wrap the deadline, got: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReconcileDeploymentsUsingSecret did not return")
	}
}

func TestReconcileDeploymentsExplainsACacheSyncTimeout(t *testing.T) {
	r := listReconciler(t, func(context.Context) error {
		return apierrors.NewTimeoutError("failed waiting for *v1.Deployment Informer to sync", 0)
	})

	_, err := r.ReconcileDeploymentsUsingSecret(context.Background(), reloadDopplerSecret())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "list and watch on apps/deployments at cluster scope") {
		t.Errorf("expected the RBAC hint, got: %v", err)
	}
	if !apierrors.IsTimeout(err) {
		t.Errorf("the timeout should stay detectable through the wrapper: %v", err)
	}
}

func TestReconcileDeploymentsLeavesOtherListErrorsAlone(t *testing.T) {
	r := listReconciler(t, func(context.Context) error {
		return apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "",
			errors.New("cannot list"))
	})

	_, err := r.ReconcileDeploymentsUsingSecret(context.Background(), reloadDopplerSecret())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "did not sync") {
		t.Errorf("a non-timeout error should not get the sync hint, got: %v", err)
	}
}

func TestAggregateDeploymentFailuresExplainsARefusedWrite(t *testing.T) {
	failures := []deploymentFailure{
		{name: "web", err: forbiddenUpdate("web")},
		{name: "api", err: forbiddenUpdate("api")},
	}

	got := aggregateDeploymentFailures(failures, 2).Error()

	if n := strings.Count(got, "needs update on apps/deployments"); n != 1 {
		t.Errorf("expected the remedy once, got %d in: %v", n, got)
	}
}

func TestAggregateDeploymentFailuresOmitsRemedyForOtherErrors(t *testing.T) {
	failures := []deploymentFailure{
		{name: "web", err: apierrors.NewConflict(
			schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", errors.New("object modified"))},
	}

	got := aggregateDeploymentFailures(failures, 1).Error()

	if strings.Contains(got, "update on apps/deployments") {
		t.Errorf("a conflict is not an RBAC problem, got: %v", got)
	}
}
