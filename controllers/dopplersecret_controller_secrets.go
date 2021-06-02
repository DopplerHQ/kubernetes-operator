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
	b64 "encoding/base64"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
	"github.com/DopplerHQ/kubernetes-operator/pkg/api"
)

const (
	kubeSecretVersionAnnotation      = "secrets.doppler.com/version"
	kubeSecretDopplerSecretLabel     = "dopplerSecret"
	kubeSecretDopplerSecretNameLabel = "dopplerSecretName"
)

// Generates an APIContext from a DopplerSecret
func GetAPIContext(dopplerSecret secretsv1alpha1.DopplerSecret) api.APIContext {
	host := "https://api.doppler.com"
	if dopplerSecret.Spec.Host != "" {
		host = dopplerSecret.Spec.Host
	}
	verifyTLS := true
	if dopplerSecret.Spec.VerifyTLS != "" {
		verifyTLS = dopplerSecret.Spec.VerifyTLS == "true"
	}
	return api.APIContext{
		Host:      host,
		VerifyTLS: verifyTLS,
		APIKey:    dopplerSecret.Spec.ServiceToken,
	}
}

// Updates a Kubernetes secret using the configuration specified in a DopplerSecret
func (r *DopplerSecretReconciler) UpdateSecret(dopplerSecret secretsv1alpha1.DopplerSecret) error {
	kubeSecretNamespacedName := types.NamespacedName{
		Namespace: dopplerSecret.Namespace,
		Name:      dopplerSecret.Spec.SecretName,
	}
	existingKubeSecret := &corev1.Secret{}
	err := r.Client.Get(context.Background(), kubeSecretNamespacedName, existingKubeSecret)
	if err != nil {
		// There is no existing secret, we'll need to create it
		existingKubeSecret = nil
	}

	r.Log.Info(fmt.Sprintf("Fetching Doppler secrets for: %s", dopplerSecret.Name))
	secretVersion := ""
	if existingKubeSecret != nil {
		secretVersion = existingKubeSecret.Annotations[kubeSecretVersionAnnotation]
	}
	secretsResult, apiErr := api.GetSecrets(GetAPIContext(dopplerSecret), secretVersion)
	if apiErr != nil {
		return fmt.Errorf("Failed to fetch secrets from Doppler API: %w", apiErr)
	}
	if !secretsResult.Modified {
		r.Log.Info("[-] Doppler secrets not modified.")
		return nil
	}
	r.Log.Info("[/] Secrets have been modified", "oldVersion", secretVersion, "newVersion", secretsResult.ETag)

	kubeSecretData := map[string][]byte{}
	for _, secret := range secretsResult.Secrets {
		kubeSecretData[secret.Name] = []byte(b64.StdEncoding.EncodeToString([]byte(secret.Value)))
	}
	kubeSecretAnnotations := map[string]string{
		kubeSecretVersionAnnotation: secretsResult.ETag,
	}
	kubeSecretLabels := map[string]string{
		kubeSecretDopplerSecretLabel:     "true",
		kubeSecretDopplerSecretNameLabel: fmt.Sprintf("%v.%v", dopplerSecret.Namespace, dopplerSecret.Name),
	}
	if existingKubeSecret == nil {
		newKubeSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        dopplerSecret.Spec.SecretName,
				Namespace:   dopplerSecret.Namespace,
				Annotations: kubeSecretAnnotations,
				Labels:      kubeSecretLabels,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: dopplerSecret.APIVersion,
						Kind:       dopplerSecret.Kind,
						Name:       dopplerSecret.Name,
						UID:        dopplerSecret.UID,
					},
				},
			},
			Type: "Opaque",
			Data: kubeSecretData,
		}
		err := r.Client.Create(context.Background(), newKubeSecret)
		if err != nil {
			return fmt.Errorf("Failed to create Kubernetes secret: %w", err)
		}
		r.Log.Info("[/] Successfully created new Kubernetes secret")
	} else {
		existingKubeSecret.Data = kubeSecretData
		existingKubeSecret.ObjectMeta.Annotations = kubeSecretAnnotations
		err := r.Client.Update(context.Background(), existingKubeSecret)
		if err != nil {
			return fmt.Errorf("Failed to update Kubernetes secret: %w", err)
		}
		r.Log.Info("[/] Successfully updated existing Kubernetes secret")
	}
	return nil
}
