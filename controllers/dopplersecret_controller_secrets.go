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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/DopplerHQ/kubernetes-operator/pkg/models"

	"github.com/go-logr/logr"
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
	kubeSecretManagedByAnnotation         = "secrets.doppler.com/managed-by"
	kubeSecretLastUpdatedAnnotation       = "secrets.doppler.com/last-updated"
	kubeSecretServiceTokenKey             = "serviceToken"
)

var kubeSecretBuiltInAnnotationKeys = []string{kubeSecretVersionAnnotation, kubeSecretProcessorsVersionAnnotation, kubeSecretFormatVersionAnnotation, kubeSecretDashboardLinkAnnotaion, kubeSecretManagedByAnnotation, kubeSecretLastUpdatedAnnotation}

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
func GetKubeSecretAnnotations(secretsResult models.SecretsResult, processorsVersion string, format string, additionalLabels map[string]string, managedBy string) map[string]string {
	annotations := map[string]string{}

	for k, v := range additionalLabels {
		annotations[k] = v
	}

	annotations[kubeSecretVersionAnnotation] = secretsResult.ETag
	annotations[kubeSecretDashboardLinkAnnotaion] = GetDashboardLink(secretsResult.Secrets)
	annotations[kubeSecretManagedByAnnotation] = managedBy
	annotations[kubeSecretLastUpdatedAnnotation] = time.Now().UTC().Format(time.RFC3339)

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
			Annotations: GetKubeSecretAnnotations(secretsResult, processorsVersion, dopplerSecret.Spec.Format, dopplerSecret.Spec.ManagedSecretRef.Annotations, dopplerSecret.GetNamespacedName()),
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
	secret.ObjectMeta.Annotations = GetKubeSecretAnnotations(secretsResult, processorsVersion, dopplerSecret.Spec.Format, dopplerSecret.Spec.ManagedSecretRef.Annotations, dopplerSecret.GetNamespacedName())
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

	apiContext, tokenSecretResourceVersion, err := authProvider.GetAPIContext(ctx)
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

	// Poll-first reconcile: before falling back to the v3 download, try the in-memory
	// poll/adoption path. Any v4 failure (poll or download) must never block reconcile;
	// on failure we fall through to the existing v3 flow below for this cycle.
	if r.PollStates == nil {
		r.PollStates = newPollStates()
	}
	uid := dopplerSecret.GetUID()
	// Fold the referenced token Secret's ResourceVersion into the identity hash so that an
	// in-place credential rotation (same Secret name/namespace, new value) invalidates the
	// stored poll etag instead of silently trusting an old etag against a new credential.
	// tokenSecretResourceVersion comes from authProvider.GetAPIContext above — the SAME read
	// that produced apiContext's token — rather than a second, independently-timed GET of the
	// token Secret. A rotation landing between two separate reads could otherwise pair a token
	// fetched under the OLD credential with an RV fetched under the NEW one (or vice versa),
	// silently binding a stale poll etag to the wrong identity. Pure spec-identity OIDC has no
	// token Secret; the identity string in the hash already covers that case, so an empty
	// ResourceVersion is fine there.
	identityHash := computeIdentityHash(dopplerSecret, currentProcessorsVersion, tokenSecretResourceVersion)
	generation := dopplerSecret.GetGeneration()

	if handled, pollErr := r.reconcilePollFirst(ctx, log, *apiContext, dopplerSecret, existingKubeSecret, uid, identityHash, generation); handled {
		return pollErr
	}

	// Existing v3 flow (unchanged). Reached on the no-state/identity-mismatch/sticky/
	// v4-failure paths.
	secretsResult, apiErr := api.GetSecrets(*apiContext, requestedSecretVersion, dopplerSecret.Spec.Project, dopplerSecret.Spec.Config, dopplerSecret.Spec.NameTransformer, dopplerSecret.Spec.Format, dopplerSecret.Spec.Secrets)
	if apiErr != nil {
		return apiErr
	}
	if !secretsResult.Modified {
		log.Info("[-] Doppler secrets not modified.")
		return nil
	}

	// Guard against a spurious reload on v4-adoption -> v3-fallback transitions. After v4
	// adoption the version annotation is a local random value (see computeV4VersionAnnotation),
	// not a server-issued ETag. requestedSecretVersion above is sent to the v3 endpoint as
	// If-None-Match, but the server never has a matching ETag for a locally-minted value, so it
	// always responds with Modified=true even when the underlying secret content hasn't
	// actually changed (e.g. a transient poll/download outage that fell back to v3 for a single
	// cycle). Writing here would rewrite the version annotation to the v3 ETag and fire a
	// deployment reload for no real content change. When no other spec-driven change forces a
	// reload (changes is empty), byte-compare the freshly-fetched data against the existing
	// managed Secret's data (same comparison the v4 apply path uses, see
	// computeV4VersionAnnotation) and skip the write entirely when identical: the managed
	// Secret and its annotation are left untouched, so no reload fires. When changes is
	// non-empty (processors/format/labels/annotations changed), always proceed as before, since
	// those cases require rewriting metadata regardless of secret data equality.
	if existingKubeSecret != nil && len(changes) == 0 {
		var includeSecretsByDefault bool
		if dopplerSecret.Spec.ManagedSecretRef.Type == string(corev1.SecretTypeOpaque) {
			includeSecretsByDefault = true
		}
		newData, dataErr := GetKubeSecretData(*secretsResult, dopplerSecret.Spec.Processors, includeSecretsByDefault)
		if dataErr != nil {
			return fmt.Errorf("Failed to build Kubernetes secret data: %w", dataErr)
		}
		if reflect.DeepEqual(existingKubeSecret.Data, newData) {
			log.Info("[-] Doppler secrets unchanged after v3 fallback, skipping write to avoid spurious reload", "oldVersion", secretVersion, "fetchedVersion", secretsResult.ETag)
			return nil
		}
	}

	log.Info("[/] Secrets have been modified", "oldVersion", secretVersion, "newVersion", secretsResult.ETag, "changes", changes)

	if existingKubeSecret == nil {
		return r.CreateManagedSecret(ctx, dopplerSecret, *secretsResult)
	} else {
		return r.UpdateManagedSecret(ctx, *existingKubeSecret, dopplerSecret, *secretsResult)
	}
}

// computeIdentityHash builds the identity hash gating the poll path. Reuses the same
// processor-version input as the v3 client-side change detection above, plus the spec
// fields that change which config/secrets a poll etag corresponds to, plus the referenced
// token Secret's ResourceVersion so that an in-place credential rotation (same Secret
// name/namespace, new value) invalidates a stored poll etag. tokenSecretResourceVersion is
// empty for pure spec-identity OIDC (no token Secret) — that path is already covered by the
// identity string in the hash. If any of these differ from the stored state, the poll path
// is bypassed and a fresh v3 fetch runs. Resource generation is intentionally NOT part of
// this hash: shouldPoll / shouldAttemptAdoption already compare st.generation separately,
// so folding it in here would be redundant.
func computeIdentityHash(dopplerSecret secretsv1alpha1.DopplerSecret, processorsVersion string, tokenSecretResourceVersion string) string {
	// Auth reference: either the OIDC identity or the token secret ref, plus the token
	// Secret's ResourceVersion to catch in-place credential rotation.
	authRef := fmt.Sprintf("%s|%s/%s@%s", dopplerSecret.Spec.Identity, dopplerSecret.Spec.TokenSecretRef.Namespace, dopplerSecret.Spec.TokenSecretRef.Name, tokenSecretResourceVersion)
	input := fmt.Sprintf(
		"%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		authRef,
		dopplerSecret.Spec.Project,
		dopplerSecret.Spec.Config,
		processorsVersion,
		dopplerSecret.Spec.Format,
		dopplerSecret.Spec.NameTransformer,
	)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(input)))
}

// newRandomVersion mints a fresh random 32-hex-character version token for the
// secrets.doppler.com/version annotation using crypto/rand. No secret-derived material is
// used: the value is opaque and unforgeable/unguessable from any other field.
func newRandomVersion() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("Failed to generate random version: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// computeV4VersionAnnotation determines the secrets.doppler.com/version annotation value for
// the v4 write path. The v4 download endpoint leaves SecretsResult.ETag empty (only PollETag
// is populated, and PollETag is randomly minted per download and treated elsewhere as a
// capability token, not a content version), so the version written to the managed Secret's
// annotation must be derived here instead.
//
// This intentionally does NOT derive the version from secret content or any auth material
// (contrast the removed computeContentVersion HMAC, which was keyed by apiContext.APIKey: a
// short-lived OIDC-exchanged key rotates independently of secret content, causing spurious
// version churn and deployment reload storms on every token refresh). Instead the newly built
// Secret data is byte-compared against the existing managed Secret's data (already fetched by
// the caller):
//   - identical data (and an existing, non-empty version annotation) -> carry the existing
//     annotation value forward unchanged, so no deployment reload is triggered;
//   - differing data, or no existing Secret, or an empty existing annotation -> mint a fresh
//     random 32-hex-character version so ReconcileDeployment sees a change and reloads.
//
// This is restart-safe (a controller restart naturally re-derives the same "unchanged"
// outcome by comparing against the persisted Kubernetes Secret) and auth-method-independent.
func computeV4VersionAnnotation(existingKubeSecret *corev1.Secret, newData map[string][]byte) (string, error) {
	if existingKubeSecret != nil {
		existingVersion := existingKubeSecret.Annotations[kubeSecretVersionAnnotation]
		if existingVersion != "" && reflect.DeepEqual(existingKubeSecret.Data, newData) {
			return existingVersion, nil
		}
	}
	return newRandomVersion()
}

// reconcilePollFirst runs the poll-first / v4-adoption logic. It returns handled=true when
// it has fully resolved this reconcile cycle (either by returning early on a poll "current",
// or by writing the managed secret from a v4 download); handled=false means the caller must
// fall through to the existing v3 flow. Any v4 failure results in handled=false so reconcile
// never breaks while v3 still works.
func (r *DopplerSecretReconciler) reconcilePollFirst(ctx context.Context, log logr.Logger, apiContext api.APIContext, dopplerSecret secretsv1alpha1.DopplerSecret, existingKubeSecret *corev1.Secret, uid types.UID, identityHash string, generation int64) (bool, error) {
	// If the managed secret is missing we can't skip a write on "current", so the poll
	// path is only eligible when the managed secret already exists.
	if existingKubeSecret != nil {
		if etag, ok := r.PollStates.shouldPoll(uid, identityHash, generation); ok {
			return r.reconcilePoll(ctx, log, apiContext, dopplerSecret, existingKubeSecret, uid, identityHash, generation, etag)
		}
	}

	// Attempt v4 adoption when eligible: no state yet, or a state that has never produced a
	// usable etag, still supports v4, and is past any sticky back-off. This retries transient
	// first-adoption failures instead of permanently locking the resource onto v3.
	if r.PollStates.shouldAttemptAdoption(uid, identityHash, generation) {
		return r.reconcileV4Adoption(ctx, log, apiContext, dopplerSecret, existingKubeSecret, uid, identityHash, generation)
	}

	// State exists but neither poll nor adoption is eligible (empty etag while sticky, or
	// v4 unsupported): fall through to v3.
	return false, nil
}

// reconcilePoll consults the v4 poll endpoint for an existing, eligible resource. The etag
// is supplied by the caller (copied out of poll state under the mutex by shouldPoll) so this
// function never dereferences the shared *pollState.
func (r *DopplerSecretReconciler) reconcilePoll(ctx context.Context, log logr.Logger, apiContext api.APIContext, dopplerSecret secretsv1alpha1.DopplerSecret, existingKubeSecret *corev1.Secret, uid types.UID, identityHash string, generation int64, etag string) (bool, error) {
	pollResult, err := api.PollSecretsChange(apiContext, etag)
	if err != nil || pollResult == api.PollUnavailable {
		log.Info("[-] Poll unavailable, falling back to v3 for this cycle")
		if nowSticky := r.PollStates.recordFailure(uid, identityHash, generation); nowSticky {
			log.Info("[-] Poll failures exceeded threshold, entering v3-only sticky mode")
		}
		return false, nil
	}

	if pollResult == api.PollCurrent {
		log.Info("[-] Poll reports secrets current, skipping fetch")
		// A "current" result is a healthy poll: clear the consecutive-failure count so
		// "3 consecutive failures" actually requires consecutiveness rather than
		// accumulating indefinitely across intervening successes.
		r.PollStates.recordPollCurrent(uid)
		return true, nil
	}

	// PollChanged: download via v4 and apply.
	return r.downloadV4AndApply(ctx, log, apiContext, dopplerSecret, existingKubeSecret, uid, identityHash, generation)
}

// reconcileV4Adoption performs the first-reconcile v4 download to adopt a resource into the
// poll path.
func (r *DopplerSecretReconciler) reconcileV4Adoption(ctx context.Context, log logr.Logger, apiContext api.APIContext, dopplerSecret secretsv1alpha1.DopplerSecret, existingKubeSecret *corev1.Secret, uid types.UID, identityHash string, generation int64) (bool, error) {
	log.Info("[/] First reconcile, attempting v4 adoption")
	return r.downloadV4AndApply(ctx, log, apiContext, dopplerSecret, existingKubeSecret, uid, identityHash, generation)
}

// downloadV4AndApply downloads secrets via v4 and writes the managed secret using the exact
// same create/update path as the v3 flow. On a clean success it records fresh poll state.
// On 501 (no v4 route) it flags v4 unsupported (sticky) and returns handled=false. On a 404
// (config not found) or any other error it records a NON-sticky failure and returns
// handled=false so the resource retries v4 next cycle rather than being pinned to v3. In all
// non-success cases the caller falls through to v3 for this cycle.
func (r *DopplerSecretReconciler) downloadV4AndApply(ctx context.Context, log logr.Logger, apiContext api.APIContext, dopplerSecret secretsv1alpha1.DopplerSecret, existingKubeSecret *corev1.Secret, uid types.UID, identityHash string, generation int64) (bool, error) {
	v4Result, apiErr := api.GetSecretsV4(apiContext, dopplerSecret.Spec.Project, dopplerSecret.Spec.Config, dopplerSecret.Spec.NameTransformer, dopplerSecret.Spec.Format, dopplerSecret.Spec.Secrets)
	if apiErr != nil {
		log.Info("[-] v4 download failed, falling back to v3 for this cycle")
		if nowSticky := r.PollStates.recordFailure(uid, identityHash, generation); nowSticky {
			log.Info("[-] v4 failures exceeded threshold, entering v3-only sticky mode")
		}
		return false, nil
	}
	if !v4Result.APISupported {
		log.Info("[-] Server has no v4 support (501), entering v3-only sticky mode")
		r.PollStates.recordUnsupported(uid, identityHash, generation)
		return false, nil
	}
	if v4Result.ConfigNotFound {
		// 404: this config was not found (transient — deleted/recreated, not-yet-created, or
		// genuinely missing). The server still supports v4, so treat it as a per-cycle
		// failure (non-sticky) and fall through to v3; v4 is retried next cycle.
		log.Info("[-] v4 config not found (404), falling back to v3 for this cycle")
		if nowSticky := r.PollStates.recordFailure(uid, identityHash, generation); nowSticky {
			log.Info("[-] v4 failures exceeded threshold, entering v3-only sticky mode")
		}
		return false, nil
	}

	secretsResult := v4Result.SecretsResult
	if !secretsResult.Modified {
		// v4 download always returns full secrets; treat non-modified defensively as a
		// no-op success and record fresh state.
		r.PollStates.recordSuccess(uid, secretsResult.PollETag, identityHash, generation)
		return true, nil
	}

	// The v4 download endpoint leaves SecretsResult.ETag empty (only PollETag is populated,
	// and PollETag is randomly minted per download and treated elsewhere as a capability
	// token). GetKubeSecretAnnotations / ReconcileDeployment gate deployment restarts on this
	// annotation changing, so byte-compare the newly-built Secret data against the existing
	// managed Secret's data to decide whether the version annotation should carry forward
	// unchanged or mint a fresh one. See computeV4VersionAnnotation.
	var includeSecretsByDefault bool
	if dopplerSecret.Spec.ManagedSecretRef.Type == string(corev1.SecretTypeOpaque) {
		includeSecretsByDefault = true
	}
	newData, dataErr := GetKubeSecretData(*secretsResult, dopplerSecret.Spec.Processors, includeSecretsByDefault)
	if dataErr != nil {
		log.Info("[-] Failed to build Kubernetes secret data from v4 download, falling back to v3 for this cycle")
		if nowSticky := r.PollStates.recordFailure(uid, identityHash, generation); nowSticky {
			log.Info("[-] v4 failures exceeded threshold, entering v3-only sticky mode")
		}
		return false, nil
	}
	version, versionErr := computeV4VersionAnnotation(existingKubeSecret, newData)
	if versionErr != nil {
		log.Info("[-] Failed to compute version annotation for v4 download, falling back to v3 for this cycle")
		if nowSticky := r.PollStates.recordFailure(uid, identityHash, generation); nowSticky {
			log.Info("[-] v4 failures exceeded threshold, entering v3-only sticky mode")
		}
		return false, nil
	}
	secretsResult.ETag = version

	log.Info("[/] Secrets downloaded via v4", "newVersion", secretsResult.ETag)
	var writeErr error
	if existingKubeSecret == nil {
		writeErr = r.CreateManagedSecret(ctx, dopplerSecret, *secretsResult)
	} else {
		writeErr = r.UpdateManagedSecret(ctx, *existingKubeSecret, dopplerSecret, *secretsResult)
	}
	if writeErr != nil {
		return true, writeErr
	}

	r.PollStates.recordSuccess(uid, secretsResult.PollETag, identityHash, generation)
	return true, nil
}
