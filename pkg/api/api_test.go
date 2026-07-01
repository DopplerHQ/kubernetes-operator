package api

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testEtag = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"

func testAPIContext(url string) APIContext {
	return APIContext{Host: url, APIKey: "apiKey123", VerifyTLS: true}
}

func TestPollSecretsChangeCurrent(t *testing.T) {
	var capturedMethod string
	var capturedPath string
	var capturedAuth string
	var capturedIfNoneMatch string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		capturedIfNoneMatch = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	result, err := PollSecretsChange(testAPIContext(server.URL), testEtag)
	if err != nil {
		t.Fatalf("expected no error, got %+v", err)
	}
	if result != PollCurrent {
		t.Fatalf("expected PollCurrent, got %v", result)
	}
	if capturedMethod != http.MethodGet {
		t.Fatalf("expected GET, got %s", capturedMethod)
	}
	if capturedPath != "/v4/secrets/poll" {
		t.Fatalf("expected path /v4/secrets/poll, got %s", capturedPath)
	}
	if capturedAuth != "" {
		t.Fatalf("expected no Authorization header on poll request, got %q", capturedAuth)
	}
	expectedIfNoneMatch := fmt.Sprintf("%q", testEtag)
	if capturedIfNoneMatch != expectedIfNoneMatch {
		t.Fatalf("expected quoted canonical If-None-Match %s, got %s", expectedIfNoneMatch, capturedIfNoneMatch)
	}
}

func TestPollSecretsChangeChanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	result, err := PollSecretsChange(testAPIContext(server.URL), testEtag)
	if err != nil {
		t.Fatalf("expected no error, got %+v", err)
	}
	if result != PollChanged {
		t.Fatalf("expected PollChanged, got %v", result)
	}
}

// TestPollSecretsChangeMapping exercises the full status-code mapping table: 304 → current,
// 200 → changed, and everything else → unavailable.
func TestPollSecretsChangeMapping(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		expected   PollResult
	}{
		{"NotModified", http.StatusNotModified, PollCurrent},
		{"OK", http.StatusOK, PollChanged},
		{"BadRequest", http.StatusBadRequest, PollUnavailable},
		{"ServiceUnavailable", http.StatusServiceUnavailable, PollUnavailable},
		{"NotFound", http.StatusNotFound, PollUnavailable},
		{"NotImplemented", http.StatusNotImplemented, PollUnavailable},
		{"InternalServerError", http.StatusInternalServerError, PollUnavailable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
			}))
			defer server.Close()

			result, err := PollSecretsChange(testAPIContext(server.URL), testEtag)
			if result != tc.expected {
				t.Fatalf("status %d: expected %v, got %v", tc.statusCode, tc.expected, result)
			}
			if tc.expected == PollUnavailable && err == nil {
				t.Fatalf("status %d: expected an error", tc.statusCode)
			}
			if tc.expected != PollUnavailable && err != nil {
				t.Fatalf("status %d: expected no error, got %+v", tc.statusCode, err)
			}
		})
	}
}

func TestPollSecretsChangeTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// hang well beyond the expected ~2s client timeout
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	start := time.Now()
	result, err := PollSecretsChange(testAPIContext(server.URL), testEtag)
	elapsed := time.Since(start)

	if result != PollUnavailable {
		t.Fatalf("expected PollUnavailable, got %v", result)
	}
	if err == nil {
		t.Fatalf("expected an error on timeout")
	}
	if elapsed < 1*time.Second || elapsed > 4*time.Second {
		t.Fatalf("expected request to time out around 2s, took %v", elapsed)
	}
}

func TestPollSecretsChangeConnectionError(t *testing.T) {
	// Point at a closed server to force a connection error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	result, err := PollSecretsChange(testAPIContext(server.URL), testEtag)
	if result != PollUnavailable {
		t.Fatalf("expected PollUnavailable, got %v", result)
	}
	if err == nil {
		t.Fatalf("expected an error on connection failure")
	}
}

func TestPollSecretsChangeNoBody(t *testing.T) {
	var capturedMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		body, _ := ioutil.ReadAll(r.Body)
		if len(body) != 0 {
			t.Errorf("expected empty request body, got %q", body)
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	_, _ = PollSecretsChange(testAPIContext(server.URL), testEtag)
	if capturedMethod != http.MethodGet {
		t.Fatalf("expected GET, got %s", capturedMethod)
	}
}

func TestPollSecretsChangeEtagNeverInURL(t *testing.T) {
	var capturedURL string
	var capturedRawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		capturedRawQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	_, _ = PollSecretsChange(testAPIContext(server.URL), testEtag)

	if strings.Contains(capturedURL, testEtag) {
		t.Fatalf("etag leaked into request URL: %s", capturedURL)
	}
	if capturedRawQuery != "" {
		t.Fatalf("expected no query params, got %s", capturedRawQuery)
	}
}

func TestPollSecretsChangeErrorNeverContainsEtag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := PollSecretsChange(testAPIContext(server.URL), testEtag)
	if err != nil && strings.Contains(err.Error(), testEtag) {
		t.Fatalf("etag leaked into error message: %s", err.Error())
	}
}

// TestPollSecretsChangeErrorIsStatusOnly guards against any response text (e.g. an error
// body echoing the bearer etag or other data) ever reaching the returned error. Under the
// new protocol there is no response body to parse at all, so the error must be derived
// solely from the status code.
func TestPollSecretsChangeErrorIsStatusOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(testEtag))
	}))
	defer server.Close()

	result, err := PollSecretsChange(testAPIContext(server.URL), testEtag)
	if result != PollUnavailable {
		t.Fatalf("expected PollUnavailable, got %v", result)
	}
	if err == nil {
		t.Fatalf("expected an error on 400")
	}
	if strings.Contains(err.Error(), testEtag) {
		t.Fatalf("response text leaked into error message: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected error to reference the status code, got: %s", err.Error())
	}
}

func TestGetSecretsV4CapturesPollETag(t *testing.T) {
	var capturedPath string
	var capturedAuth string
	var capturedIfNoneMatch string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		capturedIfNoneMatch = r.Header.Get("If-None-Match")
		w.Header().Set("X-Poll-ETag", testEtag)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"FOO":"bar"}`))
	}))
	defer server.Close()

	result, err := GetSecretsV4(testAPIContext(server.URL), "proj", "cfg", "", "", nil)
	if err != nil {
		t.Fatalf("expected no error, got %+v", err)
	}
	if capturedPath != "/v4/configs/config/secrets/download" {
		t.Fatalf("expected path /v4/configs/config/secrets/download, got %s", capturedPath)
	}
	if capturedAuth == "" {
		t.Fatalf("expected an Authorization header on the download request")
	}
	if capturedIfNoneMatch != "" {
		t.Fatalf("expected no If-None-Match header to be sent, got %s", capturedIfNoneMatch)
	}
	if !result.APISupported {
		t.Fatalf("expected APISupported to be true on 200")
	}
	if result.SecretsResult.PollETag != testEtag {
		t.Fatalf("expected PollETag %s, got %s", testEtag, result.SecretsResult.PollETag)
	}
	if len(result.SecretsResult.Secrets) != 1 || result.SecretsResult.Secrets[0].Name != "FOO" {
		t.Fatalf("expected secret FOO to be parsed, got %+v", result.SecretsResult.Secrets)
	}
}

func TestGetSecretsV4NoPollETagWhenAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	result, err := GetSecretsV4(testAPIContext(server.URL), "proj", "cfg", "", "", nil)
	if err != nil {
		t.Fatalf("expected no error, got %+v", err)
	}
	if result.SecretsResult.PollETag != "" {
		t.Fatalf("expected empty PollETag, got %s", result.SecretsResult.PollETag)
	}
}

func TestGetSecretsV4NotFoundMeansConfigNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	result, err := GetSecretsV4(testAPIContext(server.URL), "proj", "cfg", "", "", nil)
	if err != nil {
		t.Fatalf("expected no error for 404 (caller falls back to v3), got %+v", err)
	}
	// A 404 means the specific config was not found — a transient, per-config condition.
	// The server still supports v4, so APISupported stays true (NOT the sticky signal) and
	// the caller learns about the missing config via ConfigNotFound.
	if !result.APISupported {
		t.Fatalf("expected APISupported to remain true on 404 (404 is not the v4-unsupported signal)")
	}
	if !result.ConfigNotFound {
		t.Fatalf("expected ConfigNotFound to be true on 404")
	}
}

func TestGetSecretsV4NotImplementedMeansAPIUnsupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	}))
	defer server.Close()

	result, err := GetSecretsV4(testAPIContext(server.URL), "proj", "cfg", "", "", nil)
	if err != nil {
		t.Fatalf("expected no error for 501 (caller falls back to v3), got %+v", err)
	}
	// A 501 means the server has no v4 route at all — the durable/sticky signal.
	if result.APISupported {
		t.Fatalf("expected APISupported to be false on 501")
	}
	if result.ConfigNotFound {
		t.Fatalf("expected ConfigNotFound to be false on 501 (501 is not a config-not-found signal)")
	}
}

func TestGetSecretsV4OtherErrorStatusReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"messages":["boom"],"success":false}`))
	}))
	defer server.Close()

	result, err := GetSecretsV4(testAPIContext(server.URL), "proj", "cfg", "", "", nil)
	if err == nil {
		t.Fatalf("expected an error for 500")
	}
	if result != nil {
		t.Fatalf("expected nil result on error, got %+v", result)
	}
}

func TestGetSecretsV4FormatBypassesJSONParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`FOO=bar`))
	}))
	defer server.Close()

	result, err := GetSecretsV4(testAPIContext(server.URL), "proj", "cfg", "", "env", nil)
	if err != nil {
		t.Fatalf("expected no error, got %+v", err)
	}
	if len(result.SecretsResult.Secrets) != 1 || result.SecretsResult.Secrets[0].Value != "FOO=bar" {
		t.Fatalf("expected format download to bypass JSON parsing, got %+v", result.SecretsResult.Secrets)
	}
}

func TestGetSecretsV4QueryParams(t *testing.T) {
	var capturedQuery map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.Query()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	_, err := GetSecretsV4(testAPIContext(server.URL), "proj", "cfg", "upper", "", []string{"FOO", "BAR"})
	if err != nil {
		t.Fatalf("expected no error, got %+v", err)
	}
	if got := capturedQuery["project"]; len(got) != 1 || got[0] != "proj" {
		t.Fatalf("expected project=proj, got %v", got)
	}
	if got := capturedQuery["config"]; len(got) != 1 || got[0] != "cfg" {
		t.Fatalf("expected config=cfg, got %v", got)
	}
	if got := capturedQuery["name_transformer"]; len(got) != 1 || got[0] != "upper" {
		t.Fatalf("expected name_transformer=upper, got %v", got)
	}
	if got := capturedQuery["secrets"]; len(got) != 1 || got[0] != "FOO,BAR" {
		t.Fatalf("expected secrets=FOO,BAR, got %v", got)
	}
}
