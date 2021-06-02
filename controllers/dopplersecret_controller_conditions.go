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

func (r *DopplerSecretReconciler) SetUpdateSecretCondition(ctx context.Context, dopplerSecret *secretsv1alpha1.DopplerSecret, updateSecretsError error) {
	log := r.Log.WithValues("dopplersecret", dopplerSecret.GetNamespacedName())
	if dopplerSecret.Status.Conditions == nil {
		dopplerSecret.Status.Conditions = []metav1.Condition{}
	}
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
			Message: fmt.Sprintf("Unable to update dopplersecret: %v", updateSecretsError),
		})
		meta.SetStatusCondition(&dopplerSecret.Status.Conditions, metav1.Condition{
			Type:    "secrets.doppler.com/DeploymentReloadReady",
			Status:  metav1.ConditionFalse,
			Reason:  "Stopped",
			Message: "Deployment reload has been stopped due to secrets sync failure",
		})
	}
	err := r.Client.Status().Update(ctx, dopplerSecret)
	if err != nil {
		log.Error(err, "Unable to set update secret condition")
	}
}

func (r *DopplerSecretReconciler) SetReconcileDeploymentsCondition(ctx context.Context, dopplerSecret *secretsv1alpha1.DopplerSecret, deploymentError error) {
	log := r.Log.WithValues("dopplersecret", dopplerSecret.GetNamespacedName())
	if dopplerSecret.Status.Conditions == nil {
		dopplerSecret.Status.Conditions = []metav1.Condition{}
	}
	if deploymentError == nil {
		meta.SetStatusCondition(&dopplerSecret.Status.Conditions, metav1.Condition{
			Type:    "secrets.doppler.com/DeploymentReloadReady",
			Status:  metav1.ConditionTrue,
			Reason:  "OK",
			Message: "Controller is ready to reload deployments",
		})
	} else {
		meta.SetStatusCondition(&dopplerSecret.Status.Conditions, metav1.Condition{
			Type:    "secrets.doppler.com/DeploymentReloadReady",
			Status:  metav1.ConditionFalse,
			Reason:  "Error",
			Message: fmt.Sprintf("Unable to reconcile deployments: %v", deploymentError),
		})
	}
	err := r.Client.Status().Update(ctx, dopplerSecret)
	if err != nil {
		log.Error(err, "Unable to set reconcile deployments condition")
	}
}
