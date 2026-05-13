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

package csi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/DopplerHQ/kubernetes-operator/pkg/api"
	"github.com/DopplerHQ/kubernetes-operator/pkg/models"
	"github.com/DopplerHQ/kubernetes-operator/pkg/version"

	"sigs.k8s.io/secrets-store-csi-driver/provider/v1alpha1"
)

// Provider implements the Secrets Store CSI Driver provider gRPC interface.
//
// The provider caches the most recent ETag and secret list per (project, config, token)
// so that Mount calls made during rotation can use If-None-Match against the Doppler API.
// A 304 Not Modified response does not count against API rate limits and avoids
// transferring the full secret payload when nothing has changed.
type Provider struct {
	v1alpha1.UnimplementedCSIDriverProviderServer

	mu    sync.Mutex
	cache map[cacheKey]*cacheEntry
}

// cacheKey identifies a unique (project, config, token) combination.
// The token is hashed to avoid keeping plaintext credentials in memory longer than needed.
type cacheKey struct {
	Project   string
	Config    string
	TokenHash string
}

type cacheEntry struct {
	ETag    string
	Secrets []models.Secret
}

// mountParameters holds the parsed SecretProviderClass parameters.
type mountParameters struct {
	Project         string `json:"project"`
	Config          string `json:"config"`
	Host            string `json:"host"`
	NameTransformer string `json:"nameTransformer"`
	Format          string `json:"format"`
	VerifyTLS       bool   `json:"verifyTLS"`
}

func (p *Provider) Version(ctx context.Context, req *v1alpha1.VersionRequest) (*v1alpha1.VersionResponse, error) {
	return &v1alpha1.VersionResponse{
		Version:        "v1alpha1",
		RuntimeName:    "doppler",
		RuntimeVersion: version.ControllerVersion,
	}, nil
}

func (p *Provider) Mount(ctx context.Context, req *v1alpha1.MountRequest) (*v1alpha1.MountResponse, error) {
	params, err := parseParameters(req.GetAttributes())
	if err != nil {
		return nil, fmt.Errorf("Failed to parse parameters: %w", err)
	}

	token, err := extractToken(req.GetSecrets())
	if err != nil {
		return nil, fmt.Errorf("Failed to extract token: %w", err)
	}

	apiCtx := api.APIContext{
		Host:      params.Host,
		APIKey:    token,
		VerifyTLS: params.VerifyTLS,
	}

	key := cacheKey{
		Project:   params.Project,
		Config:    params.Config,
		TokenHash: hashToken(token),
	}

	// Use the cached ETag for If-None-Match. A 304 response does not count
	// against Doppler API rate limits and lets us reuse the cached secrets.
	cachedETag, cachedSecrets := p.getCached(key)

	result, apiErr := api.GetSecrets(apiCtx, cachedETag, params.Project, params.Config, params.NameTransformer, params.Format, nil)
	if apiErr != nil {
		return nil, fmt.Errorf("Failed to fetch secrets from Doppler: %w", apiErr)
	}

	etag := result.ETag
	secrets := result.Secrets
	if !result.Modified {
		etag = cachedETag
		secrets = cachedSecrets
	} else {
		p.setCached(key, etag, secrets)
	}

	var files []*v1alpha1.File
	var versions []*v1alpha1.ObjectVersion

	for _, secret := range secrets {
		if err := validateSecretName(secret.Name); err != nil {
			return nil, fmt.Errorf("Invalid secret name %q: %w", secret.Name, err)
		}
		files = append(files, &v1alpha1.File{
			Path:     secret.Name,
			Mode:     0400,
			Contents: []byte(secret.Value),
		})
		versions = append(versions, &v1alpha1.ObjectVersion{
			Id:      secret.Name,
			Version: etag,
		})
	}

	return &v1alpha1.MountResponse{
		Files:         files,
		ObjectVersion: versions,
	}, nil
}

func (p *Provider) getCached(key cacheKey) (string, []models.Secret) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.cache[key]
	if !ok {
		return "", nil
	}
	return entry.ETag, entry.Secrets
}

func (p *Provider) setCached(key cacheKey, etag string, secrets []models.Secret) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		p.cache = make(map[cacheKey]*cacheEntry)
	}
	p.cache[key] = &cacheEntry{ETag: etag, Secrets: secrets}
}

// hashToken returns a SHA-256 hex digest of the token. Used as a cache key so we
// don't keep plaintext credentials in the cache map.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func parseParameters(attributes string) (*mountParameters, error) {
	params := &mountParameters{
		Host:      "https://api.doppler.com",
		VerifyTLS: true,
	}

	if attributes == "" {
		return nil, fmt.Errorf("Attributes cannot be empty")
	}

	if err := json.Unmarshal([]byte(attributes), params); err != nil {
		return nil, fmt.Errorf("Failed to unmarshal attributes: %w", err)
	}

	if params.Project == "" {
		return nil, fmt.Errorf("Project is required")
	}
	if params.Config == "" {
		return nil, fmt.Errorf("Config is required")
	}

	return params, nil
}

// validateSecretName rejects names that could escape the mount directory or
// produce surprising paths when the CSI driver writes them to the pod volume.
// Doppler secret names are normally UPPER_SNAKE_CASE so these patterns should
// never appear in practice — this is defense in depth.
func validateSecretName(name string) error {
	if name == "" {
		return fmt.Errorf("Name is empty")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("Name must not contain path separators")
	}
	if name == "." || name == ".." || strings.HasPrefix(name, "..") {
		return fmt.Errorf("Name must not be a relative path component")
	}
	return nil
}

func extractToken(secrets string) (string, error) {
	if secrets == "" {
		return "", fmt.Errorf("NodePublishSecretRef is required: must contain a 'serviceToken' key with a Doppler service token")
	}

	var secretMap map[string]string
	if err := json.Unmarshal([]byte(secrets), &secretMap); err != nil {
		return "", fmt.Errorf("Failed to unmarshal secrets: %w", err)
	}

	token, ok := secretMap["serviceToken"]
	if !ok || token == "" {
		return "", fmt.Errorf("NodePublishSecretRef must contain a 'serviceToken' key with a Doppler service token")
	}

	return token, nil
}
