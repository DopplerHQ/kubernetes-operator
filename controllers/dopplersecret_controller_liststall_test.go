package controllers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// listReconciler builds a reconciler whose Deployment List is handled by listFn, standing in
// for the cached read.
func listReconciler(t *testing.T, listFn func(context.Context) error) *DopplerSecretReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "creds",
		Namespace: "app",
		Labels:    map[string]string{ManagedSecretLabelKey: ManagedSecretLabelValue},
	}}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, _ client.WithWatch, _ client.ObjectList,
				_ ...client.ListOption) error {
				return listFn(ctx)
			},
		}).Build()

	return &DopplerSecretReconciler{Client: c, Log: logr.Discard()}
}

// The cached Deployment read must carry a deadline. The informer behind it is started
// lazily, and an informer that cannot sync makes the read wait on its context; a reconcile
// context supplies no deadline, so an unbounded read holds the reconcile worker forever.
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
		t.Fatal("the cached deployment List was given a context with no deadline: " +
			"an informer that cannot sync would block the reconcile worker indefinitely")
	}
	if budget <= 0 || budget > deploymentListTimeout {
		t.Errorf("deadline should be within deploymentListTimeout (%v), got %v", deploymentListTimeout, budget)
	}
}

// A deadline on the parent context is not extended by the bound.
func TestReconcileDeploymentsDoesNotExtendACallerDeadline(t *testing.T) {
	var budget time.Duration
	r := listReconciler(t, func(ctx context.Context) error {
		if deadline, ok := ctx.Deadline(); ok {
			budget = time.Until(deadline)
		}
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := r.ReconcileDeploymentsUsingSecret(ctx, reloadDopplerSecret()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if budget > 2*time.Second {
		t.Errorf("bound must not extend the caller's deadline: got %v", budget)
	}
}

// The scenario the bound exists for: a reconcile context with no deadline of its own, and an
// informer that never syncs. Without the bound this never returns.
func TestReconcileDeploymentsRecoversFromAnUnsyncableCacheWithNoCallerDeadline(t *testing.T) {
	original := deploymentListTimeout
	deploymentListTimeout = 150 * time.Millisecond
	defer func() { deploymentListTimeout = original }()

	r := listReconciler(t, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	done := make(chan error, 1)
	go func() {
		// context.Background(): exactly what a reconcile supplies, no deadline.
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
	case <-time.After(10 * time.Second):
		t.Fatal("ReconcileDeploymentsUsingSecret never returned with an unbounded caller " +
			"context: the reconcile worker would be held indefinitely")
	}
}

// A read that exceeds the bound surfaces as an error rather than hanging, so the condition
// reports it and the reconcile requeues.
func TestReconcileDeploymentsReportsAStalledList(t *testing.T) {
	r := listReconciler(t, func(ctx context.Context) error {
		// Stands in for waiting on an informer that will never sync: block until the
		// context the caller supplied is done, then report why.
		<-ctx.Done()
		return ctx.Err()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := r.ReconcileDeploymentsUsingSecret(ctx, reloadDopplerSecret())
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a stalled deployment List must return an error")
		}
		if !strings.Contains(err.Error(), "Unable to fetch deployments") {
			t.Errorf("error should identify the failing read, got: %v", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error should wrap the deadline cause, got: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReconcileDeploymentsUsingSecret did not return: the List is still unbounded")
	}
}
