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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
)

// SetSecretsSyncReadyCondition records the result of syncing the managed secret. UpdateStatus writes it.
func (r *DopplerSecretReconciler) SetSecretsSyncReadyCondition(dopplerSecret *secretsv1alpha1.DopplerSecret, updateSecretsError error) {
	if updateSecretsError == nil {
		meta.SetStatusCondition(&dopplerSecret.Status.Conditions, metav1.Condition{
			Type:    "secrets.doppler.com/SecretSyncReady",
			Status:  metav1.ConditionTrue,
			Reason:  "OK",
			Message: "Controller is continuously syncing secrets",
		})
	} else {
		meta.SetStatusCondition(&dopplerSecret.Status.Conditions, metav1.Condition{
			Type:    "secrets.doppler.com/SecretSyncReady",
			Status:  metav1.ConditionFalse,
			Reason:  "Error",
			Message: fmt.Sprintf("Secret update failed: %v", updateSecretsError),
		})
		meta.SetStatusCondition(&dopplerSecret.Status.Conditions, metav1.Condition{
			Type:    "secrets.doppler.com/DeploymentReloadReady",
			Status:  metav1.ConditionFalse,
			Reason:  "Stopped",
			Message: "Deployment reload has been stopped due to secrets sync failure",
		})
	}
}

// SetDeploymentReloadReadyCondition records the result of reloading deployments. UpdateStatus writes it.
func (r *DopplerSecretReconciler) SetDeploymentReloadReadyCondition(dopplerSecret *secretsv1alpha1.DopplerSecret, numDeployments int, deploymentError error) {
	if deploymentError == nil {
		meta.SetStatusCondition(&dopplerSecret.Status.Conditions, metav1.Condition{
			Type:    "secrets.doppler.com/DeploymentReloadReady",
			Status:  metav1.ConditionTrue,
			Reason:  "OK",
			Message: fmt.Sprintf("Controller is ready to reload deployments. %v found.", numDeployments),
		})
	} else {
		meta.SetStatusCondition(&dopplerSecret.Status.Conditions, metav1.Condition{
			Type:    "secrets.doppler.com/DeploymentReloadReady",
			Status:  metav1.ConditionFalse,
			Reason:  "Error",
			Message: fmt.Sprintf("Deployment reconcile failed: %v", deploymentError),
		})
	}
}

// UpdateStatus writes the DopplerSecret's conditions. Reconcile calls it once, after setting
// every condition. With two writes, the first one's watch event starts the next reconcile
// before the cache has the second, and that reconcile's write conflicts.
func (r *DopplerSecretReconciler) UpdateStatus(ctx context.Context, dopplerSecret *secretsv1alpha1.DopplerSecret) {
	if err := r.Client.Status().Update(ctx, dopplerSecret); err != nil {
		r.Log.Error(err, "Unable to update dopplersecret status", "dopplersecret", dopplerSecret.GetNamespacedName())
	}
}
