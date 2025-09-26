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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/DopplerHQ/kubernetes-operator/pkg/models"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
	"github.com/DopplerHQ/kubernetes-operator/pkg/api"
	procs "github.com/DopplerHQ/kubernetes-operator/pkg/processors"
)

const (
	kubeSecretVersionAnnotation           = "secrets.doppler.com/version"
	kubeSecretProcessorsVersionAnnotation = "secrets.doppler.com/processor-version"
	kubeSecretFormatVersionAnnotation     = "secrets.doppler.com/format"
	kubeSecretDashboardLinkAnnotaion      = "secrets.doppler.com/dashboard-link"
	kubeSecretServiceTokenKey             = "serviceToken"
)

var kubeSecretBuiltInAnnotationKeys = []string{kubeSecretVersionAnnotation, kubeSecretProcessorsVersionAnnotation, kubeSecretFormatVersionAnnotation, kubeSecretDashboardLinkAnnotaion}

// GetDashboardLink gets a link to the Doppler dashboard from a list of Doppler secrets
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

// GetReferencedSecret gets a Kubernetes secret from a SecretReference
func (r *DopplerSecretReconciler) GetReferencedSecret(ctx context.Context, namespacedName types.NamespacedName) (*corev1.Secret, error) {
	existingKubeSecret := &corev1.Secret{}
	err := r.Client.Get(ctx, namespacedName, existingKubeSecret)
	if err != nil {
		existingKubeSecret = nil
	}
	return existingKubeSecret, err
}

// GetDopplerToken gets the Doppler Service Token referenced by the DopplerSecret
func (r *DopplerSecretReconciler) GetDopplerToken(ctx context.Context, dopplerSecret secretsv1alpha1.DopplerSecret) (string, error) {
	tokenSecretNamespacedName := types.NamespacedName{
		Name:      dopplerSecret.Spec.TokenSecretRef.Name,
		Namespace: dopplerSecret.Spec.TokenSecretRef.Namespace,
	}
	tokenSecret, err := r.GetReferencedSecret(ctx, tokenSecretNamespacedName)
	if err != nil {
		return "", fmt.Errorf("Failed to fetch token secret reference: %w", err)
	}
	dopplerToken := tokenSecret.Data[kubeSecretServiceTokenKey]
	if dopplerToken == nil {
		return "", fmt.Errorf("Could not find secret key %s.%s", dopplerSecret.Spec.TokenSecretRef.Name, kubeSecretServiceTokenKey)
	}
	return string(dopplerToken), nil
}

// GetKubeSecretData generates Kube secret data from a Doppler API secrets result
func GetKubeSecretData(secretsResult models.SecretsResult, processors secretsv1alpha1.SecretProcessors, includeSecretsByDefault bool) (map[string][]byte, error) {
	kubeSecretData := map[string][]byte{}
	for _, secret := range secretsResult.Secrets {
		// Processors
		processor := processors[secret.Name]
		if processor == nil {
			processor = &secretsv1alpha1.DefaultProcessor
		}

		var secretName string

		if processor.AsName != "" {
			secretName = processor.AsName
		} else if includeSecretsByDefault {
			secretName = secret.Name
		} else {
			// Omit this secret entirely
			continue
		}

		processorFunc := procs.All[processor.Type]
		if processorFunc == nil {
			return nil, fmt.Errorf("Failed to process data with unknown processor: %v", processor.Type)
		}
		data, err := processorFunc(secret.Value)
		if err != nil {
			return nil, fmt.Errorf("Failed to process data: %w", err)
		}

		kubeSecretData[secretName] = data
	}
	return kubeSecretData, nil
}

// GetKubeSecretAnnotations generates Kube annotations from a Doppler API secrets result
func GetKubeSecretAnnotations(secretsResult models.SecretsResult, processorsVersion string, format string, additionalLabels map[string]string) map[string]string {
	annotations := map[string]string{}

	for k, v := range additionalLabels {
		annotations[k] = v
	}

	annotations[kubeSecretVersionAnnotation] = secretsResult.ETag
	annotations[kubeSecretDashboardLinkAnnotaion] = GetDashboardLink(secretsResult.Secrets)

	if len(processorsVersion) > 0 {
		annotations[kubeSecretProcessorsVersionAnnotation] = processorsVersion
	}

	if len(format) > 0 {
		annotations[kubeSecretFormatVersionAnnotation] = format
	}

	return annotations
}

// GetKubeSecretLabels generates Kube labels from the provided managed secret spec values
func GetKubeSecretLabels(additionalLabels map[string]string) map[string]string {
	labels := map[string]string{}

	for k, v := range additionalLabels {
		labels[k] = v
	}

	labels["secrets.doppler.com/subtype"] = "dopplerSecret"

	return labels
}

// GetProcessorsVersion generates the version of given processors using a SHA256 hash
func GetProcessorsVersion(processors secretsv1alpha1.SecretProcessors) (string, error) {
	if len(processors) == 0 {
		return "", nil
	}
	processorsJson, err := json.Marshal(processors)
	if err != nil {
		return "", fmt.Errorf("Failed to marshal processors: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(processorsJson)), nil
}

// CreateManagedSecret creates a managed Kubernetes secret
func (r *DopplerSecretReconciler) CreateManagedSecret(ctx context.Context, dopplerSecret secretsv1alpha1.DopplerSecret, secretsResult models.SecretsResult) error {
	var includeSecretsByDefault bool
	if dopplerSecret.Spec.ManagedSecretRef.Type == string(corev1.SecretTypeOpaque) {
		includeSecretsByDefault = true
	}
	secretData, dataErr := GetKubeSecretData(secretsResult, dopplerSecret.Spec.Processors, includeSecretsByDefault)
	if dataErr != nil {
		return fmt.Errorf("Failed to build Kubernetes secret data: %w", dataErr)
	}
	processorsVersion, versErr := GetProcessorsVersion(dopplerSecret.Spec.Processors)
	if versErr != nil {
		return fmt.Errorf("Failed to compute processors version: %w", versErr)
	}
	newKubeSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        dopplerSecret.Spec.ManagedSecretRef.Name,
			Namespace:   dopplerSecret.Spec.ManagedSecretRef.Namespace,
			Annotations: GetKubeSecretAnnotations(secretsResult, processorsVersion, dopplerSecret.Spec.Format, dopplerSecret.Spec.ManagedSecretRef.Annotations),
			Labels:      GetKubeSecretLabels(dopplerSecret.Spec.ManagedSecretRef.Labels),
		},
		Type: corev1.SecretType(dopplerSecret.Spec.ManagedSecretRef.Type),
		Data: secretData,
	}
	err := r.Client.Create(ctx, newKubeSecret)
	if err != nil {
		return fmt.Errorf("Failed to create Kubernetes secret: %w", err)
	}
	r.Log.Info("[/] Successfully created new Kubernetes secret")
	return nil
}

// UpdateManagedSecret updates a managed Kubernetes secret
func (r *DopplerSecretReconciler) UpdateManagedSecret(ctx context.Context, secret corev1.Secret, dopplerSecret secretsv1alpha1.DopplerSecret, secretsResult models.SecretsResult) error {
	var includeSecretsByDefault bool
	if dopplerSecret.Spec.ManagedSecretRef.Type == string(corev1.SecretTypeOpaque) {
		includeSecretsByDefault = true
	}
	secretData, dataErr := GetKubeSecretData(secretsResult, dopplerSecret.Spec.Processors, includeSecretsByDefault)
	if dataErr != nil {
		return fmt.Errorf("Failed to build Kubernetes secret data: %w", dataErr)
	}
	processorsVersion, procsVersErr := GetProcessorsVersion(dopplerSecret.Spec.Processors)
	if procsVersErr != nil {
		return fmt.Errorf("Failed to compute processors version: %w", procsVersErr)
	}
	secret.Data = secretData
	secret.ObjectMeta.Annotations = GetKubeSecretAnnotations(secretsResult, processorsVersion, dopplerSecret.Spec.Format, dopplerSecret.Spec.ManagedSecretRef.Annotations)
	secret.ObjectMeta.Labels = GetKubeSecretLabels((dopplerSecret.Spec.ManagedSecretRef.Labels))
	err := r.Client.Update(ctx, &secret)
	if err != nil {
		return fmt.Errorf("Failed to update Kubernetes secret: %w", err)
	}
	r.Log.Info("[/] Successfully updated existing Kubernetes secret")
	return nil
}

// UpdateSecret updates a Kubernetes secret using the configuration specified in a DopplerSecret
func (r *DopplerSecretReconciler) UpdateSecret(ctx context.Context, dopplerSecret secretsv1alpha1.DopplerSecret) error {
	log := r.Log.WithValues("dopplersecret", dopplerSecret.GetNamespacedName(), "verifyTLS", dopplerSecret.Spec.VerifyTLS, "host", dopplerSecret.Spec.Host)
	if dopplerSecret.Spec.ManagedSecretRef.Namespace == "" {
		dopplerSecret.Spec.ManagedSecretRef.Namespace = dopplerSecret.Namespace
	}

	// Handle namespace defaults
	if dopplerSecret.Spec.TokenSecretRef.Namespace == "" {
		dopplerSecret.Spec.TokenSecretRef.Namespace = dopplerSecret.Namespace
	}

	authProvider, err := r.getAuthProvider(ctx, &dopplerSecret)
	if err != nil {
		return fmt.Errorf("Failed to get auth provider: %w", err)
	}

	apiContext, err := authProvider.GetAPIContext(ctx)
	if err != nil {
		return fmt.Errorf("Failed to get API context: %w", err)
	}

	managedSecretNamespacedName := types.NamespacedName{
		Name:      dopplerSecret.Spec.ManagedSecretRef.Name,
		Namespace: dopplerSecret.Spec.ManagedSecretRef.Namespace,
	}
	existingKubeSecret, err := r.GetReferencedSecret(ctx, managedSecretNamespacedName)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("Failed to fetch managed secret reference: %w", err)
	}
	if existingKubeSecret != nil && existingKubeSecret.Type != corev1.SecretType(dopplerSecret.Spec.ManagedSecretRef.Type) {
		return fmt.Errorf("Cannot change existing managed secret type from %v to %v. Delete the managed secret and re-apply the DopplerSecret.", existingKubeSecret.Type, dopplerSecret.Spec.ManagedSecretRef.Type)
	}

	currentProcessorsVersion, err := GetProcessorsVersion(dopplerSecret.Spec.Processors)
	if err != nil {
		return fmt.Errorf("Failed to compute processors version: %w", err)
	}

	log.Info("Fetching Doppler secrets")
	secretVersion := ""

	// Secret processors
	processorsVersion := ""
	formatVersion := ""
	existingLabels := map[string]string{}
	existingCustomAnnotations := map[string]string{}
	if existingKubeSecret != nil {
		secretVersion = existingKubeSecret.Annotations[kubeSecretVersionAnnotation]
		processorsVersion = existingKubeSecret.Annotations[kubeSecretProcessorsVersionAnnotation]
		formatVersion = existingKubeSecret.Annotations[kubeSecretFormatVersionAnnotation]
		existingLabels = existingKubeSecret.Labels
		// We can't predict the new annotations because it includes the latest secret version.
		// Instead, we'll just compare the custom (non-builtin) annotations on the secret against the spec.
		for k, v := range existingKubeSecret.Annotations {
			if !slices.Contains(kubeSecretBuiltInAnnotationKeys, k) {
				existingCustomAnnotations[k] = v
			}
		}
	}

	changes := []string{}

	// Processors transform secret values so if they've changed, we need to re-fetch the secrets so they can be re-processed.
	if currentProcessorsVersion != processorsVersion {
		changes = append(changes, "processors")
	}

	// The format is computed by the API and it defaults to "json". However, the operator uses the presence of the `format` field
	// to determine whether or not to process the JSON as separate k/v pairs or save the whole payload into a single DOPPLER_SECRETS_FILE secret.
	// If the format changed, we need to re-fetch secrets so we can redetermine this.
	if dopplerSecret.Spec.Format != formatVersion {
		changes = append(changes, "format")
	}

	// If the labels have been changed, we don't technically need to reload the secrets but it's simpler to do.
	if !reflect.DeepEqual(existingLabels, GetKubeSecretLabels(dopplerSecret.Spec.ManagedSecretRef.Labels)) {
		changes = append(changes, "labels")
	}

	customAnnotations := dopplerSecret.Spec.ManagedSecretRef.Annotations
	if customAnnotations == nil {
		// Default to empty for comparison
		customAnnotations = map[string]string{}
	}

	// If the annotations have been changed, we don't technically need to reload the secrets but it's simpler to do.
	if !reflect.DeepEqual(existingCustomAnnotations, customAnnotations) {
		changes = append(changes, "annotations")
	}

	// If any relevant attributes have been changed, set requestedSecretVersion to an empty secret version to reload the secrets.
	requestedSecretVersion := secretVersion
	if len(changes) > 0 {
		log.Info("[/] Attributes have changed, reloading secrets.", "changes", changes)
		requestedSecretVersion = ""
	}

	secretsResult, apiErr := api.GetSecrets(*apiContext, requestedSecretVersion, dopplerSecret.Spec.Project, dopplerSecret.Spec.Config, dopplerSecret.Spec.NameTransformer, dopplerSecret.Spec.Format, dopplerSecret.Spec.Secrets)
	if apiErr != nil {
		return apiErr
	}
	if !secretsResult.Modified {
		log.Info("[-] Doppler secrets not modified.")
		return nil
	}

	log.Info("[/] Secrets have been modified", "oldVersion", secretVersion, "newVersion", secretsResult.ETag, "changes", changes)

	if existingKubeSecret == nil {
		return r.CreateManagedSecret(ctx, dopplerSecret, *secretsResult)
	} else {
		return r.UpdateManagedSecret(ctx, *existingKubeSecret, dopplerSecret, *secretsResult)
	}
}
