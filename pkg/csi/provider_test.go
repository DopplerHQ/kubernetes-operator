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
	"strings"
	"testing"

	"github.com/DopplerHQ/kubernetes-operator/pkg/models"
)

func TestParseParameters(t *testing.T) {
	cases := []struct {
		name       string
		attributes string
		wantErr    string
		check      func(*testing.T, *mountParameters)
	}{
		{
			name:       "valid minimal",
			attributes: `{"project":"my-proj","config":"prd"}`,
			check: func(t *testing.T, p *mountParameters) {
				if p.Project != "my-proj" || p.Config != "prd" {
					t.Errorf("unexpected project/config: %+v", p)
				}
				if p.Host != "https://api.doppler.com" {
					t.Errorf("expected default host, got %q", p.Host)
				}
				if !p.VerifyTLS {
					t.Errorf("expected VerifyTLS default true")
				}
			},
		},
		{
			name:       "valid full",
			attributes: `{"project":"p","config":"c","host":"https://example.com","nameTransformer":"camel","format":"json","verifyTLS":false}`,
			check: func(t *testing.T, p *mountParameters) {
				if p.Host != "https://example.com" || p.NameTransformer != "camel" || p.Format != "json" || p.VerifyTLS {
					t.Errorf("unexpected parsed values: %+v", p)
				}
			},
		},
		{
			name:       "missing project",
			attributes: `{"config":"prd"}`,
			wantErr:    "Project is required",
		},
		{
			name:       "missing config",
			attributes: `{"project":"p"}`,
			wantErr:    "Config is required",
		},
		{
			name:       "empty attributes",
			attributes: "",
			wantErr:    "Attributes cannot be empty",
		},
		{
			name:       "malformed json",
			attributes: `{not json`,
			wantErr:    "Failed to unmarshal attributes",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parseParameters(tc.attributes)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, p)
			}
		})
	}
}

func TestExtractToken(t *testing.T) {
	cases := []struct {
		name    string
		secrets string
		want    string
		wantErr string
	}{
		{
			name:    "valid token",
			secrets: `{"serviceToken":"dp.st.dev.abc"}`,
			want:    "dp.st.dev.abc",
		},
		{
			name:    "missing token key",
			secrets: `{"other":"value"}`,
			wantErr: "must contain a 'serviceToken'",
		},
		{
			name:    "empty token value",
			secrets: `{"serviceToken":""}`,
			wantErr: "must contain a 'serviceToken'",
		},
		{
			name:    "empty secrets",
			secrets: "",
			wantErr: "NodePublishSecretRef is required",
		},
		{
			name:    "malformed json",
			secrets: `{nope`,
			wantErr: "Failed to unmarshal secrets",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok, err := extractToken(tc.secrets)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tok != tc.want {
				t.Errorf("got %q, want %q", tok, tc.want)
			}
		})
	}
}

func TestValidateSecretName(t *testing.T) {
	valid := []string{"API_KEY", "DB_PASSWORD", "PROD_TEST_SECRET", "lowercase_ok", "with.dot"}
	invalid := []string{"", "../etc/passwd", "..", ".", "with/slash", "with\\backslash", "..hidden"}

	for _, n := range valid {
		t.Run("valid/"+n, func(t *testing.T) {
			if err := validateSecretName(n); err != nil {
				t.Errorf("expected %q to be valid, got %v", n, err)
			}
		})
	}
	for _, n := range invalid {
		t.Run("invalid/"+n, func(t *testing.T) {
			if err := validateSecretName(n); err == nil {
				t.Errorf("expected %q to be rejected", n)
			}
		})
	}
}

func TestProviderCacheRoundTrip(t *testing.T) {
	p := &Provider{}
	key := cacheKey{Project: "p", Config: "c", TokenHash: "h"}

	etag, secrets := p.getCached(key)
	if etag != "" || secrets != nil {
		t.Fatalf("expected empty cache, got etag=%q secrets=%v", etag, secrets)
	}

	want := []models.Secret{{Name: "API_KEY", Value: "v1"}}
	p.setCached(key, "etag-1", want)

	gotETag, gotSecrets := p.getCached(key)
	if gotETag != "etag-1" {
		t.Errorf("got etag %q, want %q", gotETag, "etag-1")
	}
	if len(gotSecrets) != 1 || gotSecrets[0].Name != "API_KEY" || gotSecrets[0].Value != "v1" {
		t.Errorf("unexpected secrets returned: %+v", gotSecrets)
	}

	// Updating overwrites the previous entry.
	p.setCached(key, "etag-2", []models.Secret{{Name: "DB_URL", Value: "v2"}})
	gotETag, gotSecrets = p.getCached(key)
	if gotETag != "etag-2" || gotSecrets[0].Name != "DB_URL" {
		t.Errorf("expected updated entry, got etag=%q secrets=%+v", gotETag, gotSecrets)
	}

	// Different key returns empty.
	other := cacheKey{Project: "other", Config: "c", TokenHash: "h"}
	gotETag, gotSecrets = p.getCached(other)
	if gotETag != "" || gotSecrets != nil {
		t.Errorf("expected empty for different key, got etag=%q secrets=%+v", gotETag, gotSecrets)
	}
}

func TestHashTokenIsDeterministic(t *testing.T) {
	if hashToken("abc") != hashToken("abc") {
		t.Error("expected identical hashes for identical input")
	}
	if hashToken("abc") == hashToken("abd") {
		t.Error("expected different hashes for different input")
	}
	if strings.Contains(hashToken("dp.st.dev.secret"), "dp.st") {
		t.Error("hash should not contain the original token")
	}
}
