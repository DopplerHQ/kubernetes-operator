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
	deploymentSecretUpdateAnnotation = "secrets.doppler.com/secretsupdate"
	deploymentRestartAnnotation      = "secrets.doppler.com/reload"
)

// Reconciles deployments marked with the restart annotation and that use the specified DopplerSecret.
func (r *DopplerSecretReconciler) ReconcileDeploymentsUsingSecret(dopplerSecret secretsv1alpha1.DopplerSecret) error {
	deploymentList := &v1.DeploymentList{}
	err := r.Client.List(context.Background(), deploymentList, &client.ListOptions{Namespace: dopplerSecret.Namespace})
	if err != nil {
		return fmt.Errorf("Unable to fetch deployments: %w", err)
	}
	kubeSecretNamespacedName := types.NamespacedName{
		Namespace: dopplerSecret.Namespace,
		Name:      dopplerSecret.Spec.SecretName,
	}
	kubeSecret := &corev1.Secret{}
	err = r.Client.Get(context.Background(), kubeSecretNamespacedName, kubeSecret)
	if err != nil {
		return fmt.Errorf("Unable to fetch Kubernetes secret to update deployment: %w", err)
	}
	var wg sync.WaitGroup
	for _, deployment := range deploymentList.Items {
		if deployment.Annotations[deploymentRestartAnnotation] == "true" && r.IsDeploymentUsingSecret(deployment, dopplerSecret) {
			wg.Add(1)
			go func(deployment v1.Deployment, kubeSecret corev1.Secret, wg *sync.WaitGroup) {
				defer wg.Done()
				err := r.ReconcileDeployment(deployment, kubeSecret)
				if err != nil {
					// Errors reconciling deployments are logged but not propagated up. Failed deployments will be reconciled on the next run.
					r.Log.Error(err, "Unable to reconcile deployment", "deployment", deployment.Name)
				}
			}(deployment, *kubeSecret, &wg)
		}
	}
	wg.Wait()

	return nil
}

// Evaluates whether or not the deployment is using the specified DopplerSecret.
// Specifically, a deployment is using a DopplerSecret if it references it using `envFrom`, `secretKeyRef` or `volumes`.
func (r *DopplerSecretReconciler) IsDeploymentUsingSecret(deployment v1.Deployment, dopplerSecret secretsv1alpha1.DopplerSecret) bool {
	for _, container := range deployment.Spec.Template.Spec.Containers {
		for _, envFrom := range container.EnvFrom {
			if envFrom.SecretRef != nil && envFrom.SecretRef.LocalObjectReference.Name == dopplerSecret.Spec.SecretName {
				return true
			}
		}
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.LocalObjectReference.Name == dopplerSecret.Spec.SecretName {
				return true
			}
		}
	}
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Secret != nil && volume.Secret.SecretName == dopplerSecret.Spec.SecretName {
			return true
		}
	}

	return false
}

// Reconciles a deployment with a Kubernetes secret
// Specifically, if the Kubernetes secret version is different from the deployment's secret version annotation,
// the annotation is updated to restart the deployment.
func (r *DopplerSecretReconciler) ReconcileDeployment(deployment v1.Deployment, secret corev1.Secret) error {
	annotationValue := fmt.Sprintf("%s-%s-%s", secret.Namespace, secret.Name, secret.Annotations[kubeSecretVersionAnnotation])
	if deployment.Annotations[deploymentSecretUpdateAnnotation] == annotationValue &&
		deployment.Spec.Template.Annotations[deploymentSecretUpdateAnnotation] == annotationValue {
		r.Log.Info("[-] Deployment is already running latest version, nothing to do", "deployment", deployment.Name)
		return nil
	}
	deployment.Annotations[deploymentSecretUpdateAnnotation] = annotationValue
	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = make(map[string]string)
	}
	deployment.Spec.Template.Annotations[deploymentSecretUpdateAnnotation] = annotationValue
	err := r.Client.Update(context.Background(), &deployment)
	if err != nil {
		return fmt.Errorf("Failed to update deployment annotation: %w", err)
	}
	r.Log.Info("[/] Updated deployment", "deployment", deployment.Name)
	return nil
}
