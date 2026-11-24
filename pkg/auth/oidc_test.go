package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// http.ProxyFromEnvironment reads the environment once per process, so this must be
// the only test in the package that sends a request.
func TestExchangeTokenWithDopplerUsesEnvironmentProxy(t *testing.T) {
	var proxiedURL string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxiedURL = r.URL.String()
		_, _ = w.Write([]byte(`{"success":true,"token":"dp.sa.x","expires_at":"2030-01-01T00:00:00Z"}`))
	}))
	defer proxy.Close()

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	// Go never proxies loopback hosts, so the target has to look remote.
	o := &OIDCAuthProvider{Host: "http://doppler.test", Identity: "id", VerifyTLS: true}
	token, _, err := o.exchangeTokenWithDoppler(context.Background(), "sa-jwt")
	if err != nil {
		t.Fatalf("exchange failed: %v", err)
	}
	if token != "dp.sa.x" {
		t.Fatalf("got token %q", token)
	}
	if want := "http://doppler.test/v3/auth/oidc"; proxiedURL != want {
		t.Fatalf("proxy saw %q, want %q", proxiedURL, want)
	}
}
