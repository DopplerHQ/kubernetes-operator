package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// http.ProxyFromEnvironment reads the environment once per process, so this must be
// the only test in the package that sends a request.
func TestPerformRequestUsesEnvironmentProxy(t *testing.T) {
	var proxiedURL string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxiedURL = r.URL.String()
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer proxy.Close()

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	// Go never proxies loopback hosts, so the target has to look remote.
	_, apiErr := GetRequest(APIContext{Host: "http://doppler.test", APIKey: "dp.st.x", VerifyTLS: true}, "/v3/configs/config/secrets/download", nil, nil)
	if apiErr != nil {
		t.Fatalf("request failed: %v", apiErr)
	}
	if want := "http://doppler.test/v3/configs/config/secrets/download"; proxiedURL != want {
		t.Fatalf("proxy saw %q, want %q", proxiedURL, want)
	}
}
