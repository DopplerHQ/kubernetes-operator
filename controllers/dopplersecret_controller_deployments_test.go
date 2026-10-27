package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
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

// The failures are quoted on one line, and each one stays detectable through the wrapper.
func TestAggregateDeploymentFailuresStaysOnOneLine(t *testing.T) {
	failures := []deploymentFailure{
		{name: "web", err: forbiddenUpdate("web")},
		{name: "api", err: forbiddenUpdate("api")},
	}

	err := aggregateDeploymentFailures(failures, 2)

	if strings.Contains(err.Error(), "\n") {
		t.Errorf("status message should be one line, got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "; web: ") {
		t.Errorf("failures should be separated by \"; \", got: %v", err)
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("the forbidden errors should stay detectable through the wrapper: %v", err)
	}
}

// syncedInformer stands in for the Deployment informer, answering only HasSynced.
type syncedInformer struct {
	crcache.Informer
	synced bool
}

func (i syncedInformer) HasSynced() bool { return i.synced }

// deploymentInformers stands in for the manager's cache.
type deploymentInformers struct {
	synced *bool
	err    error
}

func (d deploymentInformers) GetInformer(_ context.Context, _ client.Object, _ ...crcache.InformerGetOption) (crcache.Informer, error) {
	if d.err != nil {
		return nil, d.err
	}
	return syncedInformer{synced: *d.synced}, nil
}

// fallbackReconciler builds a reconciler whose cached client and API reader share one
// store holding a reload deployment and its secret. cachedList and apiList, when non-nil,
// intercept the List on each side.
func fallbackReconciler(t *testing.T, informers DeploymentInformers, cachedList, apiList func(context.Context) error, log logr.Logger) (*DopplerSecretReconciler, client.Client) {
	t.Helper()
	deployment := reloadDeployment("web")
	store := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:        "creds",
			Namespace:   "app",
			Annotations: map[string]string{kubeSecretVersionAnnotation: "v2"},
			Labels:      map[string]string{SubtypeLabelKey: ManagedSecretLabelValue},
		}},
		&deployment,
	).Build()

	intercept := func(listFn func(context.Context) error) client.WithWatch {
		if listFn == nil {
			return store
		}
		return interceptor.NewClient(store, interceptor.Funcs{
			List: func(ctx context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
				return listFn(ctx)
			},
		})
	}

	return &DopplerSecretReconciler{
		Client:              intercept(cachedList),
		APIReader:           intercept(apiList),
		DeploymentInformers: informers,
		Log:                 log,
	}, store
}

func reloadedVersion(t *testing.T, store client.Client) string {
	t.Helper()
	deployment := &v1.Deployment{}
	if err := store.Get(context.Background(), client.ObjectKey{Namespace: "app", Name: "web"}, deployment); err != nil {
		t.Fatalf("re-reading the deployment: %v", err)
	}
	return deployment.Spec.Template.Annotations[deploymentSecretUpdateAnnotationPrefix+".creds"]
}

func failList(t *testing.T, side string) func(context.Context) error {
	return func(context.Context) error {
		t.Errorf("deployments were listed from the %s", side)
		return errors.New("unexpected List")
	}
}

// Once the Deployment cache has synced, deployments come from it and not the API server.
func TestReconcileDeploymentsReadsTheCacheOnceSynced(t *testing.T) {
	synced := true
	r, store := fallbackReconciler(t, deploymentInformers{synced: &synced}, nil, failList(t, "API server"), logr.Discard())

	if _, err := r.ReconcileDeploymentsUsingSecret(context.Background(), reloadDopplerSecret()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := reloadedVersion(t, store); got != "v2" {
		t.Errorf("deployment was not reloaded, annotation is %q", got)
	}
}

// Until the Deployment cache syncs, deployments are listed from the API server, so a
// namespaced RoleBinding is enough for reloads to work.
func TestReconcileDeploymentsListsDirectlyUntilTheCacheSyncs(t *testing.T) {
	for name, informers := range map[string]deploymentInformers{
		"cache not synced":     {synced: new(bool)},
		"informer unavailable": {err: errors.New("no informer")},
	} {
		t.Run(name, func(t *testing.T) {
			r, store := fallbackReconciler(t, informers, failList(t, "unsynced cache"), nil, logr.Discard())

			if _, err := r.ReconcileDeploymentsUsingSecret(context.Background(), reloadDopplerSecret()); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := reloadedVersion(t, store); got != "v2" {
				t.Errorf("deployment was not reloaded, annotation is %q", got)
			}
		})
	}
}

// With no permission to list deployments, the reload fails at once rather than holding the
// reconcile worker for the whole list deadline.
func TestReconcileDeploymentsFailsFastWhenListingIsRefused(t *testing.T) {
	refused := func(context.Context) error {
		return apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "",
			errors.New(`cannot list resource "deployments" in API group "apps" in the namespace "app"`))
	}
	waitForever := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	r, _ := fallbackReconciler(t, deploymentInformers{synced: new(bool)}, waitForever, refused, logr.Discard())

	start := time.Now()
	_, err := r.ReconcileDeploymentsUsingSecret(context.Background(), reloadDopplerSecret())
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("a refused List should fail at once, took %v", elapsed)
	}
	if !apierrors.IsForbidden(err) {
		t.Fatalf("expected the forbidden List to be returned, got: %v", err)
	}
	if !strings.Contains(err.Error(), "list and watch on apps/deployments, at cluster scope") {
		t.Errorf("expected the RBAC hint, got: %v", err)
	}
}

// A cache that stays unsynced past the grace period is logged once, and so is its recovery.
// One that syncs within it, as at every startup, is not logged.
func TestUnsyncedDeploymentCacheIsLoggedOnlyAfterTheGracePeriod(t *testing.T) {
	for name, tc := range map[string]struct {
		grace                    time.Duration
		wantUnsynced, wantSynced int
	}{
		"syncs at startup": {grace: time.Hour},
		"stays unsynced":   {grace: 0, wantUnsynced: 1, wantSynced: 1},
	} {
		t.Run(name, func(t *testing.T) {
			original := deploymentCacheSyncGrace
			deploymentCacheSyncGrace = tc.grace
			defer func() { deploymentCacheSyncGrace = original }()

			var logged []string
			log := funcr.New(func(_, args string) { logged = append(logged, args) }, funcr.Options{})
			synced := false
			r, _ := fallbackReconciler(t, deploymentInformers{synced: &synced}, nil, nil, log)
			count := func(substr string) int {
				n := 0
				for _, line := range logged {
					if strings.Contains(line, substr) {
						n++
					}
				}
				return n
			}

			for _, cacheSynced := range []bool{false, false, false, true, true} {
				synced = cacheSynced
				if _, err := r.ReconcileDeploymentsUsingSecret(context.Background(), reloadDopplerSecret()); err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			if n := count("has not synced; listing deployments from the API server"); n != tc.wantUnsynced {
				t.Errorf("expected %d log lines for an unsynced cache, got %d: %v", tc.wantUnsynced, n, logged)
			}
			if n := count("has synced; listing deployments from the cache"); n != tc.wantSynced {
				t.Errorf("expected %d log lines for the cache syncing, got %d: %v", tc.wantSynced, n, logged)
			}
		})
	}
}
