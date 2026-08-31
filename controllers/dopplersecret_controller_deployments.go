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

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
)

const (
	deploymentSecretUpdateAnnotationPrefix = "secrets.doppler.com/secretsupdate"
	deploymentRestartAnnotation            = "secrets.doppler.com/reload"

	// maxReportedDeploymentErrors bounds how many per-deployment failures are quoted in the
	// aggregated error, which becomes a DopplerSecret status condition message stored in etcd.
	// These failures are usually identical - a missing RBAC verb fails every deployment the
	// same way - so a handful conveys the cause while the count conveys the scale.
	maxReportedDeploymentErrors = 3
)

// Reconciles deployments marked with the restart annotation and that use the specified DopplerSecret.
func (r *DopplerSecretReconciler) ReconcileDeploymentsUsingSecret(ctx context.Context, dopplerSecret secretsv1alpha1.DopplerSecret) (int, error) {
	log := r.Log.WithValues("dopplersecret", dopplerSecret.GetNamespacedName())
	namespace := dopplerSecret.Namespace
	if dopplerSecret.Spec.ManagedSecretRef.Namespace != "" {
		namespace = dopplerSecret.Spec.ManagedSecretRef.Namespace
	}
	deploymentList := &v1.DeploymentList{}
	err := r.Client.List(ctx, deploymentList, &client.ListOptions{Namespace: namespace})
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

	// Per-deployment failures are aggregated into the returned error rather than only logged.
	// DeploymentReloadReady is derived from it, so dropping them would leave that condition
	// reporting success while annotated deployments never restart - which is how a ClusterRole
	// without the deployments patch verb presents. Returning the error also requeues.
	return len(deploymentList.Items), aggregateDeploymentFailures(failures, numMatched)
}

// deploymentFailure records which deployment a reconcile error came from, so the aggregate
// can name it and order deterministically.
type deploymentFailure struct {
	name string
	err  error
}

// aggregateDeploymentFailures folds per-deployment errors into one error for the
// DeploymentReloadReady condition.
//
// Sorted by deployment name so the message is stable from one reconcile to the next rather
// than reordering with goroutine completion, and capped at maxReportedDeploymentErrors so a
// namespace full of failing deployments cannot produce an unbounded status message.
func aggregateDeploymentFailures(failures []deploymentFailure, numMatched int) error {
	if len(failures) == 0 {
		return nil
	}

	slices.SortFunc(failures, func(a, b deploymentFailure) int {
		return strings.Compare(a.name, b.name)
	})

	reported := failures
	hidden := 0
	if len(reported) > maxReportedDeploymentErrors {
		hidden = len(reported) - maxReportedDeploymentErrors
		reported = reported[:maxReportedDeploymentErrors]
	}

	quoted := make([]error, 0, len(reported))
	for _, failure := range reported {
		quoted = append(quoted, fmt.Errorf("%s: %w", failure.name, failure.err))
	}

	if hidden > 0 {
		return fmt.Errorf("Failed to reconcile %d of %d deployments using this secret (%d more not shown): %w",
			len(failures), numMatched, hidden, errors.Join(quoted...))
	}
	return fmt.Errorf("Failed to reconcile %d of %d deployments using this secret: %w",
		len(failures), numMatched, errors.Join(quoted...))
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
