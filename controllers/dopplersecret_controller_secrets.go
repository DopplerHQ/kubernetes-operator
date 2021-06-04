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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
	"github.com/DopplerHQ/kubernetes-operator/pkg/api"
	"github.com/DopplerHQ/kubernetes-operator/pkg/models"
)

const (
	kubeSecretVersionAnnotation      = "secrets.doppler.com/version"
	kubeSecretDopplerSecretLabel     = "dopplerSecret"
	kubeSecretSubtypeLabel           = "subtype"
	kubeSecretSubtypeLabelValue      = "dopplerSecret"
	kubeSecretDopplerSecretNameLabel = "dopplerSecretName"
)

// Generates an APIContext from a DopplerSecret
func GetAPIContext(dopplerSecret secretsv1alpha1.DopplerSecret, dopplerToken string) api.APIContext {
	return api.APIContext{
		Host:      dopplerSecret.Spec.Host,
		VerifyTLS: dopplerSecret.Spec.VerifyTLS,
		APIKey:    dopplerToken,
	}
}

// Get a link to the Doppler dashboard from a list of Doppler secrets
func GetDashboardLink(secrets []models.Secret) string {
	var projectSlug string
	var configSlug string
	for _, secret := range secrets {
		if secret.Name == "DOPPLER_PROJECT" {
			projectSlug = secret.Value
		} else if secret.Name == "DOPPLER_CONFIG" {
			configSlug = secret.Value
		}
	}
	if projectSlug == "" || configSlug == "" {
		return "https://dashboard.doppler.com/workplace"
	}
	return fmt.Sprintf("https://dashboard.doppler.com/workplace/projects/%v/configs/%v", projectSlug, configSlug)
}

// Get a Kubernetes secret from a SecretReference
func (r *DopplerSecretReconciler) GetReferencedSecret(ctx context.Context, ref secretsv1alpha1.SecretReference) (*corev1.Secret, error) {
	kubeSecretNamespacedName := types.NamespacedName{
		Namespace: ref.Namespace,
		Name:      ref.Name,
	}
	existingKubeSecret := &corev1.Secret{}
	err := r.Client.Get(ctx, kubeSecretNamespacedName, existingKubeSecret)
	if err != nil {
		existingKubeSecret = nil
	}
	return existingKubeSecret, err
}

// Get the Doppler Service Token referenced by the DopplerSecret
func (r *DopplerSecretReconciler) GetDopplerToken(ctx context.Context, dopplerSecret secretsv1alpha1.DopplerSecret) (string, error) {
	tokenSecret, err := r.GetReferencedSecret(ctx, dopplerSecret.Spec.TokenSecretRef)
	if err != nil {
		return "", fmt.Errorf("Failed to fetch token secret reference: %w", err)
	}
	dopplerToken := tokenSecret.Data[dopplerSecret.Spec.TokenSecretRef.Key]
	if dopplerToken == nil {
		return "", fmt.Errorf("Could not find secret key %s.%s", dopplerSecret.Spec.TokenSecretRef.Name, dopplerSecret.Spec.TokenSecretRef.Key)
	}
	return string(dopplerToken), nil
}

// Generate Kube secret data from a Doppler API secrets result
func GetKubeSecretData(secretsResult models.SecretsResult) map[string][]byte {
	kubeSecretData := map[string][]byte{}
	for _, secret := range secretsResult.Secrets {
		kubeSecretData[secret.Name] = []byte(b64.StdEncoding.EncodeToString([]byte(secret.Value)))
	}
	return kubeSecretData
}

// Generate Kube annotations from a Doppler API secrets result
func GetKubeSecretAnnotations(secretsResult models.SecretsResult) map[string]string {
	return map[string]string{
		kubeSecretVersionAnnotation:          secretsResult.ETag,
		"secrets.doppler.com/dashboard-link": GetDashboardLink(secretsResult.Secrets),
	}
}

// Create a managed Kubernetes secret
func (r *DopplerSecretReconciler) CreateManagedSecret(ctx context.Context, dopplerSecret secretsv1alpha1.DopplerSecret, secretsResult models.SecretsResult) error {
	newKubeSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        dopplerSecret.Spec.ManagedSecretRef.Name,
			Namespace:   dopplerSecret.Spec.ManagedSecretRef.Namespace,
			Annotations: GetKubeSecretAnnotations(secretsResult),
			Labels: map[string]string{
				"secrets.doppler.com/subtype": "dopplerSecret",
			},
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
		Data: GetKubeSecretData(secretsResult),
	}
	err := r.Client.Create(ctx, newKubeSecret)
	if err != nil {
		return fmt.Errorf("Failed to create Kubernetes secret: %w", err)
	}
	r.Log.Info("[/] Successfully created new Kubernetes secret")
	return nil
}

// Update a managed Kubernetes secret
func (r *DopplerSecretReconciler) UpdateManagedSecret(ctx context.Context, secret corev1.Secret, dopplerSecret secretsv1alpha1.DopplerSecret, secretsResult models.SecretsResult) error {
	secret.Data = GetKubeSecretData(secretsResult)
	secret.ObjectMeta.Annotations = GetKubeSecretAnnotations(secretsResult)
	err := r.Client.Update(ctx, &secret)
	if err != nil {
		return fmt.Errorf("Failed to update Kubernetes secret: %w", err)
	}
	r.Log.Info("[/] Successfully updated existing Kubernetes secret")
	return nil
}

// Updates a Kubernetes secret using the configuration specified in a DopplerSecret
func (r *DopplerSecretReconciler) UpdateSecret(ctx context.Context, dopplerSecret secretsv1alpha1.DopplerSecret) error {
	log := r.Log.WithValues("dopplersecret", dopplerSecret.GetNamespacedName(), "verifyTLS", dopplerSecret.Spec.VerifyTLS, "host", dopplerSecret.Spec.Host)
	if dopplerSecret.Spec.ManagedSecretRef.Namespace == "" {
		dopplerSecret.Spec.ManagedSecretRef.Namespace = dopplerSecret.Namespace
	}
	if dopplerSecret.Spec.TokenSecretRef.Namespace == "" {
		dopplerSecret.Spec.TokenSecretRef.Namespace = dopplerSecret.Namespace
	}

	dopplerToken, err := r.GetDopplerToken(ctx, dopplerSecret)
	if err != nil {
		return fmt.Errorf("Failed to load Doppler Token: %w", err)
	}

	existingKubeSecret, err := r.GetReferencedSecret(ctx, dopplerSecret.Spec.ManagedSecretRef)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("Failed to fetch managed secret reference: %w", err)
	}

	log.Info("Fetching Doppler secrets")
	secretVersion := ""
	if existingKubeSecret != nil {
		secretVersion = existingKubeSecret.Annotations[kubeSecretVersionAnnotation]
	}
	secretsResult, apiErr := api.GetSecrets(GetAPIContext(dopplerSecret, dopplerToken), secretVersion)
	if apiErr != nil {
		return apiErr
	}
	if !secretsResult.Modified {
		log.Info("[-] Doppler secrets not modified.")
		return nil
	}
	log.Info("[/] Secrets have been modified", "oldVersion", secretVersion, "newVersion", secretsResult.ETag)

	if existingKubeSecret == nil {
		return r.CreateManagedSecret(ctx, dopplerSecret, *secretsResult)
	} else {
		return r.UpdateManagedSecret(ctx, *existingKubeSecret, dopplerSecret, *secretsResult)
	}
}
