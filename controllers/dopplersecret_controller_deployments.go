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
	"fmt"
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
	deploymentDopplerSecretRefAnnotation   = "secrets.doppler.com/dopplersecret"
)

// ReconcileDeploymentsForDopplerSecret restarts deployments that are linked to the given DopplerSecret.
// A deployment is matched if it has the reload annotation and either references the managed Kubernetes
// secret (envFrom / secretKeyRef / volumes) or references the DopplerSecret by namespaced name in an
// annotation (used for CSI-mounted deployments where no managed secret exists).
func (r *DopplerSecretReconciler) ReconcileDeploymentsForDopplerSecret(ctx context.Context, dopplerSecret secretsv1alpha1.DopplerSecret, secretVersion string) (int, error) {
	log := r.Log.WithValues("dopplersecret", dopplerSecret.GetNamespacedName())

	// Collect namespaces to scan for deployments
	namespaces := map[string]bool{dopplerSecret.Namespace: true}
	if dopplerSecret.Spec.ManagedSecretRef.Namespace != "" {
		namespaces[dopplerSecret.Spec.ManagedSecretRef.Namespace] = true
	}

	// If there is a managed secret, fetch it for version tracking
	var kubeSecret *corev1.Secret
	if dopplerSecret.Spec.ManagedSecretRef.Name != "" {
		namespace := dopplerSecret.Namespace
		if dopplerSecret.Spec.ManagedSecretRef.Namespace != "" {
			namespace = dopplerSecret.Spec.ManagedSecretRef.Namespace
		}
		kubeSecretNamespacedName := types.NamespacedName{
			Namespace: namespace,
			Name:      dopplerSecret.Spec.ManagedSecretRef.Name,
		}
		secret := &corev1.Secret{}
		err := r.Client.Get(ctx, kubeSecretNamespacedName, secret)
		if err != nil {
			return 0, fmt.Errorf("Unable to fetch Kubernetes secret to update deployment: %w", err)
		}
		kubeSecret = secret
	}

	var wg sync.WaitGroup
	totalDeployments := 0

	for namespace := range namespaces {
		deploymentList := &v1.DeploymentList{}
		err := r.Client.List(ctx, deploymentList, &client.ListOptions{Namespace: namespace})
		if err != nil {
			return 0, fmt.Errorf("Unable to fetch deployments: %w", err)
		}
		totalDeployments += len(deploymentList.Items)

		for _, deployment := range deploymentList.Items {
			if deployment.Annotations[deploymentRestartAnnotation] != "true" {
				continue
			}
			usesSecret := r.IsDeploymentUsingSecret(deployment, dopplerSecret)
			refersViaAnnotation := r.IsDeploymentReferencingDopplerSecret(deployment, dopplerSecret)
			if usesSecret || refersViaAnnotation {
				wg.Add(1)
				go func(deployment v1.Deployment) {
					defer wg.Done()
					var err error
					if kubeSecret != nil {
						err = r.ReconcileDeployment(ctx, deployment, *kubeSecret)
					} else {
						err = r.ReconcileDeploymentWithVersion(ctx, deployment, dopplerSecret.Name, secretVersion)
					}
					if err != nil {
						log.Error(err, "Unable to reconcile deployment")
					}
				}(deployment)
			}
		}
	}
	wg.Wait()

	log.Info("Finished reconciling deployments", "numDeployments", totalDeployments)

	return totalDeployments, nil
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

// IsDeploymentReferencingDopplerSecret checks if a deployment has an annotation directly referencing the DopplerSecret.
// This enables restart support for deployments using CSI-mounted secrets.
func (r *DopplerSecretReconciler) IsDeploymentReferencingDopplerSecret(deployment v1.Deployment, dopplerSecret secretsv1alpha1.DopplerSecret) bool {
	return deployment.Annotations[deploymentDopplerSecretRefAnnotation] == dopplerSecret.GetNamespacedName()
}

// Reconciles a deployment with a Kubernetes secret
// Specifically, if the Kubernetes secret version is different from the deployment's secret version annotation,
// the annotation is updated to restart the deployment.
func (r *DopplerSecretReconciler) ReconcileDeployment(ctx context.Context, deployment v1.Deployment, secret corev1.Secret) error {
	annotationKey := fmt.Sprintf("%s.%s", deploymentSecretUpdateAnnotationPrefix, secret.Name)
	annotationValue := secret.Annotations[kubeSecretVersionAnnotation]
	return r.reconcileDeploymentAnnotation(ctx, deployment, annotationKey, annotationValue)
}

// ReconcileDeploymentWithVersion reconciles a deployment using a version string directly.
// Used when there is no managed Kubernetes secret (e.g. CSI-only mode).
func (r *DopplerSecretReconciler) ReconcileDeploymentWithVersion(ctx context.Context, deployment v1.Deployment, dopplerSecretName string, version string) error {
	annotationKey := fmt.Sprintf("%s.%s", deploymentSecretUpdateAnnotationPrefix, dopplerSecretName)
	return r.reconcileDeploymentAnnotation(ctx, deployment, annotationKey, version)
}

func (r *DopplerSecretReconciler) reconcileDeploymentAnnotation(ctx context.Context, deployment v1.Deployment, annotationKey string, annotationValue string) error {
	log := r.Log.WithValues("deployment", fmt.Sprintf("%s/%s", deployment.Namespace, deployment.Name))
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
