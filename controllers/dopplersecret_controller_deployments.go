/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
)

const (
	deploymentSecretUpdateAnnotationPrefix = "secrets.doppler.com/secretsupdate"
	deploymentRestartAnnotation            = "secrets.doppler.com/reload"

	// maxReportedDeploymentErrors caps how many failures are quoted in the status message.
	maxReportedDeploymentErrors = 3
)

// deploymentListTimeout bounds the Deployment List. A synced cache answers from memory, so
// this only matters if the cache is read before it syncs. A variable so tests can shorten it.
var deploymentListTimeout = 30 * time.Second

// deploymentCacheSyncGrace is how long the Deployment cache can stay unsynced before it is
// logged. A variable so tests can shorten it.
var deploymentCacheSyncGrace = 30 * time.Second

// DeploymentInformers is the part of the manager's cache used to check whether the
// Deployment informer has synced. cache.Cache satisfies it.
type DeploymentInformers interface {
	GetInformer(ctx context.Context, obj client.Object, opts ...crcache.InformerGetOption) (crcache.Informer, error)
}

// Reconciles deployments marked with the restart annotation and that use the specified DopplerSecret.
func (r *DopplerSecretReconciler) ReconcileDeploymentsUsingSecret(ctx context.Context, dopplerSecret secretsv1alpha1.DopplerSecret) (int, error) {
	log := r.Log.WithValues("dopplersecret", dopplerSecret.GetNamespacedName())
	namespace := dopplerSecret.Namespace
	if dopplerSecret.Spec.ManagedSecretRef.Namespace != "" {
		namespace = dopplerSecret.Spec.ManagedSecretRef.Namespace
	}
	deploymentList, err := r.listDeployments(ctx, namespace)
	if err != nil {
		return 0, fmt.Errorf("Unable to fetch deployments: %w", err)
	}
	kubeSecretNamespacedName := types.NamespacedName{
		Namespace: namespace,
		Name:      dopplerSecret.Spec.ManagedSecretRef.Name,
	}
	kubeSecret, err := r.GetManagedSecret(ctx, kubeSecretNamespacedName)
	if err != nil {
		return 0, fmt.Errorf("Unable to fetch Kubernetes secret to update deployment: %w", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []deploymentFailure
	numMatched := 0
	for _, deployment := range deploymentList.Items {
		if deployment.Annotations[deploymentRestartAnnotation] == "true" && r.IsDeploymentUsingSecret(deployment, dopplerSecret) {
			numMatched++
			wg.Add(1)
			go func(deployment v1.Deployment, kubeSecret corev1.Secret, wg *sync.WaitGroup) {
				defer wg.Done()
				err := r.ReconcileDeployment(ctx, deployment, kubeSecret)
				if err != nil {
					log.Error(err, "Unable to reconcile deployment")
					mu.Lock()
					defer mu.Unlock()
					failures = append(failures, deploymentFailure{name: deployment.Name, err: err})
				}
			}(deployment, *kubeSecret, &wg)
		}
	}
	wg.Wait()

	log.Info("Finished reconciling deployments", "numDeployments", len(deploymentList.Items))

	// Returned so DeploymentReloadReady does not report success while deployments go unreloaded.
	return len(deploymentList.Items), aggregateDeploymentFailures(failures, numMatched)
}

// listDeployments lists the deployments in a namespace, from the cluster-wide Deployment
// cache once it has synced and from the API server until then.
//
// Waiting for the cache instead would hold the reconcile worker for the whole deadline on
// every reconcile, and the controller's priority queue keeps handing that worker to the
// DopplerSecrets already failing, so the ones queued at startup might never sync. Listing
// directly fails fast when the operator cannot list deployments at all, and works when it
// can list only some namespaces through a RoleBinding, whose cache can never sync.
func (r *DopplerSecretReconciler) listDeployments(ctx context.Context, namespace string) (*v1.DeploymentList, error) {
	listCtx, cancelList := context.WithTimeout(ctx, deploymentListTimeout)
	defer cancelList()
	deploymentList := &v1.DeploymentList{}
	listOptions := &client.ListOptions{Namespace: namespace}

	if r.APIReader != nil {
		synced := r.deploymentCacheSynced(ctx)
		r.noteDeploymentCacheSynced(synced)
		if !synced {
			err := r.APIReader.List(listCtx, deploymentList, listOptions)
			if apierrors.IsForbidden(err) {
				err = fmt.Errorf("%w (the Deployment cache has not synced, so the operator listed this namespace "+
					"directly: it needs list and watch on apps/deployments, at cluster scope for the cache to sync)", err)
			}
			return deploymentList, err
		}
	}

	err := r.Client.List(listCtx, deploymentList, listOptions)
	if apierrors.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("%w (the Deployment cache did not sync within %s: the operator needs list and watch "+
			"on apps/deployments at cluster scope, which a namespaced RoleBinding does not grant)",
			err, deploymentListTimeout)
	}
	return deploymentList, err
}

// deploymentCacheSynced reports whether the Deployment informer has synced, starting it if it
// has not started, without waiting for it.
func (r *DopplerSecretReconciler) deploymentCacheSynced(ctx context.Context) bool {
	if r.DeploymentInformers == nil {
		return true
	}
	informer, err := r.DeploymentInformers.GetInformer(ctx, &v1.Deployment{}, crcache.BlockUntilSynced(false))
	if err != nil {
		r.Log.Error(err, "Unable to get the Deployment informer; listing deployments from the API server")
		return false
	}
	return informer.HasSynced()
}

// noteDeploymentCacheSynced logs when the Deployment cache has stayed unsynced for
// deploymentCacheSyncGrace, and when it syncs after that. The informer starts on the first
// reconcile, so a cache still syncing then is normal and not logged.
func (r *DopplerSecretReconciler) noteDeploymentCacheSynced(synced bool) {
	state := &r.deploymentCacheState
	state.mu.Lock()
	defer state.mu.Unlock()

	if synced {
		if state.reported {
			r.Log.Info("Deployment cache has synced; listing deployments from the cache")
		}
		state.unsyncedSince = time.Time{}
		state.reported = false
		return
	}
	if state.unsyncedSince.IsZero() {
		state.unsyncedSince = time.Now()
	}
	if !state.reported && time.Since(state.unsyncedSince) >= deploymentCacheSyncGrace {
		r.Log.Info("Deployment cache has not synced; listing deployments from the API server until it does. "+
			"The cache needs list and watch on apps/deployments at cluster scope.",
			"unsyncedFor", time.Since(state.unsyncedSince).Round(time.Second).String())
		state.reported = true
	}
}

// deploymentCacheState tracks how long the Deployment cache has gone unsynced.
type deploymentCacheState struct {
	mu            sync.Mutex
	unsyncedSince time.Time
	reported      bool
}

// deploymentFailure pairs a reconcile error with the deployment it came from.
type deploymentFailure struct {
	name string
	err  error
}

// aggregateDeploymentFailures folds per-deployment errors into one error, sorted by name so
// the status message is stable across reconciles.
func aggregateDeploymentFailures(failures []deploymentFailure, numMatched int) error {
	if len(failures) == 0 {
		return nil
	}

	slices.SortFunc(failures, func(a, b deploymentFailure) int {
		return strings.Compare(a.name, b.name)
	})

	omitted := ""
	if len(failures) > maxReportedDeploymentErrors {
		omitted = fmt.Sprintf(" (%d more not shown)", len(failures)-maxReportedDeploymentErrors)
	}
	quoted := []error{}
	for _, failure := range failures[:min(len(failures), maxReportedDeploymentErrors)] {
		quoted = append(quoted, fmt.Errorf("%s: %w", failure.name, failure.err))
	}

	remedy := ""
	if slices.ContainsFunc(failures, func(f deploymentFailure) bool { return apierrors.IsForbidden(f.err) }) {
		remedy = "; the operator needs update on apps/deployments in this namespace"
	}

	return fmt.Errorf("Failed to reconcile %d of %d deployments using this secret%s%s: %w",
		len(failures), numMatched, omitted, remedy, errors.Join(quoted...))
}

// Evaluates whether or not the deployment is using the specified DopplerSecret.
// Specifically, a deployment is using a DopplerSecret if it references it using `envFrom`, `secretKeyRef` or `volumes`.
func (r *DopplerSecretReconciler) IsDeploymentUsingSecret(deployment v1.Deployment, dopplerSecret secretsv1alpha1.DopplerSecret) bool {
	managedSecretName := dopplerSecret.Spec.ManagedSecretRef.Name
	for _, container := range deployment.Spec.Template.Spec.Containers {
		for _, envFrom := range container.EnvFrom {
			if envFrom.SecretRef != nil && envFrom.SecretRef.LocalObjectReference.Name == managedSecretName {
				return true
			}
		}
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.LocalObjectReference.Name == managedSecretName {
				return true
			}
		}
	}
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Secret != nil && volume.Secret.SecretName == managedSecretName {
			return true
		}
	}

	return false
}

// Reconciles a deployment with a Kubernetes secret
// Specifically, if the Kubernetes secret version is different from the deployment's secret version annotation,
// the annotation is updated to restart the deployment.
func (r *DopplerSecretReconciler) ReconcileDeployment(ctx context.Context, deployment v1.Deployment, secret corev1.Secret) error {
	log := r.Log.WithValues("deployment", fmt.Sprintf("%s/%s", deployment.Namespace, deployment.Name))
	annotationKey := fmt.Sprintf("%s.%s", deploymentSecretUpdateAnnotationPrefix, secret.Name)
	annotationValue := secret.Annotations[kubeSecretVersionAnnotation]
	if deployment.Annotations[annotationKey] == annotationValue &&
		deployment.Spec.Template.Annotations[annotationKey] == annotationValue {
		log.Info("[-] Deployment is already running latest version, nothing to do")
		return nil
	}
	deployment.Annotations[annotationKey] = annotationValue
	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = make(map[string]string)
	}
	deployment.Spec.Template.Annotations[annotationKey] = annotationValue
	err := r.Client.Update(ctx, &deployment)
	if err != nil {
		return fmt.Errorf("Failed to update deployment annotation: %w", err)
	}
	log.Info("[/] Updated deployment")
	return nil
}
