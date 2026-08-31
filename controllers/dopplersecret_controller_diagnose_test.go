package controllers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// probeReader stands in for the uncached APIReader used to explain a cache sync timeout.
type probeReader struct{ err error }

func (p probeReader) Get(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return p.err
}

func (p probeReader) List(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	return p.err
}

func syncTimeout() error {
	return apierrors.NewTimeoutError("failed waiting for *v1.Deployment Informer to sync", 0)
}

func forbiddenList() error {
	return apierrors.NewForbidden(
		schema.GroupResource{Group: "apps", Resource: "deployments"}, "",
		errors.New(`User "system:serviceaccount:doppler-operator-system:doppler-operator-controller-manager" cannot list resource "deployments" in API group "apps" at the cluster scope`))
}

// When the uncached read is refused as well, the API server's reply is the answer: it names
// the verb and the identity. Surface it rather than leaving the reader with a bare timeout.
func TestExplainCacheSyncFailureReportsARefusedRead(t *testing.T) {
	r := &DopplerSecretReconciler{Log: logr.Discard(), APIReader: probeReader{err: forbiddenList()}}

	got := r.explainCacheSyncFailure(context.Background(), "app", syncTimeout()).Error()

	for _, want := range []string{
		"did not sync",
		"reading Deployments directly failed too",
		`cannot list resource "deployments"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in the explanation, got: %v", want, got)
		}
	}
}

// When the uncached read succeeds, permissions for a plain read are present, so the fault is
// in what the informer additionally needs. Say which, and at what scope.
func TestExplainCacheSyncFailurePointsAtTheWatchWhenReadsWork(t *testing.T) {
	r := &DopplerSecretReconciler{Log: logr.Discard(), APIReader: probeReader{err: nil}}

	got := r.explainCacheSyncFailure(context.Background(), "app", syncTimeout()).Error()

	for _, want := range []string{
		"directly succeeded",
		"list and watch on apps/deployments",
		"cluster scope",
		"RoleBinding does not cover it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in the explanation, got: %v", want, got)
		}
	}
	// The namespace that was read is worth stating, since the cache is cluster-wide.
	if !strings.Contains(got, "app") {
		t.Errorf("expected the probed namespace, got: %v", got)
	}
}

// Errors that already explain themselves must pass through untouched, and must not spend an
// extra API call.
func TestExplainCacheSyncFailureLeavesOtherErrorsAlone(t *testing.T) {
	// The probe would error if it ran; a passing test proves it did not.
	r := &DopplerSecretReconciler{Log: logr.Discard(), APIReader: probeReader{err: errors.New("probe ran")}}

	cause := apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web",
		errors.New("cannot list"))
	got := r.explainCacheSyncFailure(context.Background(), "app", cause)

	if got.Error() != cause.Error() {
		t.Errorf("a non-timeout error should be returned unchanged, got: %v", got)
	}
}

// With no APIReader wired there is nothing to probe with; the original error must survive.
func TestExplainCacheSyncFailureWithoutAnAPIReader(t *testing.T) {
	r := &DopplerSecretReconciler{Log: logr.Discard()}
	cause := syncTimeout()

	if got := r.explainCacheSyncFailure(context.Background(), "app", cause); got.Error() != cause.Error() {
		t.Errorf("expected the cause unchanged, got: %v", got)
	}
}

// The timeout cause stays wrapped, so callers can still classify it.
func TestExplainCacheSyncFailureKeepsTheCauseUnwrappable(t *testing.T) {
	r := &DopplerSecretReconciler{Log: logr.Discard(), APIReader: probeReader{err: nil}}

	got := r.explainCacheSyncFailure(context.Background(), "app", syncTimeout())
	if !apierrors.IsTimeout(got) {
		t.Errorf("the sync timeout should remain detectable through the wrapper: %v", got)
	}
}

// A refused write is how an out-of-date ClusterRole presents. The API server names the verb;
// the aggregate should name the remedy, once.
func TestAggregateDeploymentFailuresExplainsARefusedWrite(t *testing.T) {
	failures := []deploymentFailure{
		{name: "web", err: forbiddenUpdate("web")},
		{name: "api", err: forbiddenUpdate("api")},
	}

	got := aggregateDeploymentFailures(failures, 2).Error()

	if !strings.Contains(got, "update on apps/deployments") {
		t.Errorf("expected the remedy for a refused write, got: %v", got)
	}
	if !strings.Contains(got, "every namespace it reloads into") {
		t.Errorf("expected the binding-scope half of the remedy, got: %v", got)
	}
	if n := strings.Count(got, "needs\nupdate on apps/deployments") + strings.Count(got, "needs update on apps/deployments"); n != 1 {
		t.Errorf("the remedy should appear once, not per failure, got %d in: %v", n, got)
	}
}

// A failure that is not an authorization problem must not attract RBAC advice.
func TestAggregateDeploymentFailuresOmitsRemedyForOtherErrors(t *testing.T) {
	failures := []deploymentFailure{
		{name: "web", err: apierrors.NewConflict(
			schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", errors.New("object modified"))},
	}

	got := aggregateDeploymentFailures(failures, 1).Error()

	if strings.Contains(got, "update on apps/deployments") {
		t.Errorf("a conflict is not an RBAC problem; advice should be omitted, got: %v", got)
	}
	if !strings.Contains(got, "1 of 1") {
		t.Errorf("expected the count, got: %v", got)
	}
}

// Sanity: the wrapper the reconcile applies keeps the explanation readable end to end.
func TestReconcileDeploymentsWrapsTheExplanation(t *testing.T) {
	r := listReconciler(t, func(ctx context.Context) error { return syncTimeout() })
	r.APIReader = probeReader{err: forbiddenList()}

	_, err := r.ReconcileDeploymentsUsingSecret(context.Background(), reloadDopplerSecret())
	if err == nil {
		t.Fatal("expected an error")
	}
	got := err.Error()
	if !strings.Contains(got, "Unable to fetch deployments") {
		t.Errorf("expected the outer context, got: %v", got)
	}
	if !strings.Contains(got, `cannot list resource "deployments"`) {
		t.Errorf("expected the underlying cause, got: %v", got)
	}
}
