package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Handle OIDC-based authentication
type OIDCAuthProvider struct {
	// Kubernetes client for TokenRequest API
	KubeClient kubernetes.Interface

	Namespace string
	Audiences []string

	Host              string
	Identity          string
	VerifyTLS         bool
	ExpirationSeconds int64

	// Token management
	rwm         sync.RWMutex
	cachedToken string
	tokenExpiry time.Time
}


// Returns a Doppler API token, refreshing if necessary
func (o *OIDCAuthProvider) GetToken(ctx context.Context) (string, error) {
	o.rwm.RLock()
	if o.isTokenValid() {
		token := o.cachedToken
		o.rwm.RUnlock()
		return token, nil
	}
	o.rwm.RUnlock()

	// Token needs refresh
	return o.refreshToken(ctx)
}

// Check if the cached token is still valid
func (o *OIDCAuthProvider) isTokenValid() bool {
	if o.cachedToken == "" {
		return false
	}
	return time.Until(o.tokenExpiry) > 60*time.Second
}

// Obtain a new Doppler Service Account API token
func (o *OIDCAuthProvider) refreshToken(ctx context.Context) (string, error) {
	o.rwm.Lock()
	defer o.rwm.Unlock()

	// Sanity check after acquiring write lock
	if o.isTokenValid() {
		return o.cachedToken, nil
	}

	saToken, err := o.getServiceAccountToken(ctx)
	if err != nil {
		return "", fmt.Errorf("Failed to get service account token: %w", err)
	}

	dopplerToken, expiry, err := o.exchangeTokenWithDoppler(ctx, saToken)
	if err != nil {
		return "", fmt.Errorf("Failed to exchange token with Doppler: %w", err)
	}

	o.cachedToken = dopplerToken
	o.tokenExpiry = expiry

	return dopplerToken, nil
}

// Use the TokenRequest API to get a JWT
func (o *OIDCAuthProvider) getServiceAccountToken(ctx context.Context) (string, error) {
	tokenRequest := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			Audiences:         o.Audiences,
			ExpirationSeconds: &o.ExpirationSeconds,
		},
	}

	tokenResponse, err := o.KubeClient.CoreV1().
		ServiceAccounts(o.Namespace).
		CreateToken(ctx, "doppler-operator-controller-manager", tokenRequest, metav1.CreateOptions{})

	if err != nil {
		return "", fmt.Errorf("Failed to create service account token: %w", err)
	}

	return tokenResponse.Status.Token, nil
}

// Exchange the K8s SA token for a Doppler SA API Token
func (o *OIDCAuthProvider) exchangeTokenWithDoppler(ctx context.Context, saToken string) (string, time.Time, error) {
	url := fmt.Sprintf("%s/v3/auth/oidc", o.Host)

	requestBody := map[string]string{
		"identity": o.Identity,
		"token":    saToken,
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("Failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("Failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: !o.VerifyTLS,
		},
	}

	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("Failed to make request to Doppler: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("Failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("Doppler OIDC auth failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response struct {
		Success   bool   `json:"success"`
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}

	if err := json.Unmarshal(body, &response); err != nil {
		return "", time.Time{}, fmt.Errorf("Failed to parse response: %w", err)
	}

	if !response.Success {
		return "", time.Time{}, fmt.Errorf("Doppler OIDC auth failed")
	}

	expiresAt, err := time.Parse(time.RFC3339, response.ExpiresAt)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("Failed to parse expiration time: %w", err)
	}

	return response.Token, expiresAt, nil
}
