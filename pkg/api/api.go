package api

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/DopplerHQ/kubernetes-operator/pkg/models"

	"github.com/DopplerHQ/kubernetes-operator/pkg/version"
)

const secretsDownloadFileKey = "DOPPLER_SECRETS_FILE"

type APIContext struct {
	Host      string
	APIKey    string
	VerifyTLS bool
}

type APIResponse struct {
	HTTPResponse *http.Response
	Body         []byte
}

type APIError struct {
	Err     error
	Message string
}

type ErrorResponse struct {
	Messages []string
	Success  bool
}

type QueryParam struct {
	Key   string
	Value string
}

func (e *APIError) Error() string {
	message := fmt.Sprintf("Doppler Error: %s", e.Message)
	if underlyingError := e.Err; underlyingError != nil {
		message = fmt.Sprintf("%s\n%s", message, underlyingError.Error())
	}
	return message
}

func isSuccess(statusCode int) bool {
	return (statusCode >= 200 && statusCode <= 299) || (statusCode >= 300 && statusCode <= 399)
}

func GetRequest(context APIContext, path string, headers map[string]string, params []QueryParam) (*APIResponse, *APIError) {
	url := fmt.Sprintf("%s%s", context.Host, path)
	req, err := http.NewRequest("GET", url, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	query := req.URL.Query()
	for _, param := range params {
		query.Add(param.Key, param.Value)
	}
	req.URL.RawQuery = query.Encode()
	if err != nil {
		return nil, &APIError{Err: err, Message: "Unable to form request"}
	}

	return PerformRequest(context, req)
}

func PerformRequest(context APIContext, req *http.Request) (*APIResponse, *APIError) {
	return performRequest(context, req)
}

// performRequest executes req and builds an APIResponse/APIError, same as PerformRequest.
// Any status code listed in passthroughStatusCodes is treated as a successful response
// (skipping error-body parsing) so callers can distinguish specific non-2xx statuses
// (e.g. 404/501 meaning "endpoint not supported") from genuine failures.
func performRequest(context APIContext, req *http.Request, passthroughStatusCodes ...int) (*APIResponse, *APIError) {
	client := &http.Client{Timeout: 10 * time.Second}

	userAgent := fmt.Sprintf("kubernetes-operator/%s", version.ControllerVersion)
	req.Header.Set("user-agent", userAgent)
	req.SetBasicAuth(context.APIKey, "")
	if req.Header.Get("accept") == "" {
		req.Header.Set("accept", "application/json")
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	if !context.VerifyTLS {
		tlsConfig.InsecureSkipVerify = true
	}

	client.Transport = &http.Transport{
		DisableKeepAlives: true,
		TLSClientConfig:   tlsConfig,
	}

	r, err := client.Do(req)
	if err != nil {
		return nil, &APIError{Err: err, Message: "Unable to load response"}
	}
	defer r.Body.Close()

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return &APIResponse{HTTPResponse: r, Body: nil}, &APIError{Err: err, Message: "Unable to load response data"}
	}
	response := &APIResponse{HTTPResponse: r, Body: body}

	for _, code := range passthroughStatusCodes {
		if r.StatusCode == code {
			return response, nil
		}
	}

	if !isSuccess(r.StatusCode) {
		if contentType := r.Header.Get("content-type"); strings.HasPrefix(contentType, "application/json") {
			var errResponse ErrorResponse
			err := json.Unmarshal(body, &errResponse)
			if err != nil {
				return response, &APIError{Err: err, Message: "Unable to load response"}
			}
			return response, &APIError{Err: nil, Message: strings.Join(errResponse.Messages, "\n")}
		}
		return nil, &APIError{Err: fmt.Errorf("%d status code; %d bytes", r.StatusCode, len(body)), Message: "Unable to load response"}
	}
	return response, nil
}

func GetSecrets(context APIContext, lastETag string, project string, config string, nameTransformer string, format string, secrets []string) (*models.SecretsResult, *APIError) {
	headers := map[string]string{}
	if lastETag != "" {
		headers["If-None-Match"] = lastETag
	}

	params := []QueryParam{}
	if project != "" {
		params = append(params, QueryParam{Key: "project", Value: project})
	}
	if config != "" {
		params = append(params, QueryParam{Key: "config", Value: config})
	}
	if len(secrets) > 0 {
		params = append(params, QueryParam{Key: "secrets", Value: strings.Join(secrets, ",")})
	}
	if nameTransformer != "" {
		params = append(params, QueryParam{Key: "name_transformer", Value: nameTransformer})
	}
	if format != "" {
		params = append(params, QueryParam{Key: "format", Value: format})
	}

	response, err := GetRequest(context, "/v3/configs/config/secrets/download", headers, params)
	if err != nil {
		return nil, err
	}

	if response.HTTPResponse.StatusCode == 304 {
		return &models.SecretsResult{Modified: false, Secrets: nil, ETag: ""}, nil
	}
	eTag := response.HTTPResponse.Header.Get("ETag")

	// Format defeats JSON parsing
	if format != "" {
		secrets := []models.Secret{{
			Name:  secretsDownloadFileKey,
			Value: string(response.Body),
		}}
		return &models.SecretsResult{Modified: true, Secrets: secrets, ETag: eTag}, nil
	}

	result, modelErr := parseSecrets(response.Body, eTag)
	if modelErr != nil {
		return nil, &APIError{Err: modelErr, Message: "Unable to parse secrets"}
	}
	return result, nil
}

// PollResult is the outcome of a v4 secrets change poll.
type PollResult int

const (
	PollCurrent PollResult = iota
	PollChanged
	PollUnavailable
)

// pollTimeout is the dedicated (short) timeout used for poll requests so that
// polling never blocks the caller's reconcile/poll loop.
const pollTimeout = 2 * time.Second

// PollSecretsChange checks whether the secrets for a config have changed since the
// given etag. This hits the unauthenticated v4 poll endpoint via a status-code-only
// wire protocol: the etag is only ever sent in the quoted If-None-Match request
// header and must never appear in a URL, log line, or error message. There is no
// response body to parse on any status code. A 304 means the etag's epoch still
// matches (current); a 200 means it changed; any other status, timeout, or
// connection error is reported as PollUnavailable so callers can safely fall back
// to their existing polling/refresh strategy.
func PollSecretsChange(context APIContext, etag string) (PollResult, error) {
	url := fmt.Sprintf("%s/v4/secrets/poll", context.Host)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return PollUnavailable, fmt.Errorf("unable to build poll request: %w", err)
	}
	req.Header.Set("If-None-Match", fmt.Sprintf("%q", etag))
	req.Header.Set("Cache-Control", "no-store")
	req.Header.Set("user-agent", fmt.Sprintf("kubernetes-operator/%s", version.ControllerVersion))
	// Intentionally no Authorization header: this endpoint is unauthenticated by design.

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if !context.VerifyTLS {
		tlsConfig.InsecureSkipVerify = true
	}
	client := &http.Client{
		Timeout:   pollTimeout,
		Transport: &http.Transport{DisableKeepAlives: true, TLSClientConfig: tlsConfig},
	}

	resp, err := client.Do(req)
	if err != nil {
		return PollUnavailable, fmt.Errorf("unable to poll for secrets changes: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return PollCurrent, nil
	case http.StatusOK:
		return PollChanged, nil
	default:
		// Deliberately report only the status code: the response has no body to parse
		// under the new protocol, and the etag must never appear in an error string
		// that callers may log.
		return PollUnavailable, fmt.Errorf("poll request failed with HTTP %d", resp.StatusCode)
	}
}

// GetSecretsV4Result is the result of a GetSecretsV4 call.
type GetSecretsV4Result struct {
	SecretsResult *models.SecretsResult
	// APISupported is false ONLY when the server responded 501 (Not Implemented),
	// indicating the server has no v4 route at all; this is a durable/sticky signal and
	// callers should fall back to GetSecrets (v3). It remains true for 404.
	APISupported bool
	// ConfigNotFound is true when the server responded 404, meaning this specific
	// project/config was not found (e.g. a deleted-then-recreated config, a not-yet-created
	// config, or a genuinely-missing config now that the v4 endpoint no longer auto-creates
	// missing configs). This is a transient, per-config condition — NOT a v4-unsupported
	// signal — so callers should fall back to v3 for this cycle and retry v4 next cycle
	// rather than permanently pinning the resource to v3.
	ConfigNotFound bool
}

// GetSecretsV4 mirrors GetSecrets but downloads against the v4 endpoint, which never
// accepts If-None-Match and instead returns a fresh etag via the X-Poll-ETag response
// header (empty when the response includes dynamic secret leases). A 501 response means
// the server has no v4 route and is reported via APISupported=false (not an error). A 404
// response means the specific config was not found and is reported via ConfigNotFound=true
// (with APISupported left true); both are surfaced without an error so the caller can fall
// back to GetSecrets, but only 501 is the sticky v3-only signal.
func GetSecretsV4(context APIContext, project string, config string, nameTransformer string, format string, secrets []string) (*GetSecretsV4Result, *APIError) {
	params := []QueryParam{}
	if project != "" {
		params = append(params, QueryParam{Key: "project", Value: project})
	}
	if config != "" {
		params = append(params, QueryParam{Key: "config", Value: config})
	}
	if len(secrets) > 0 {
		params = append(params, QueryParam{Key: "secrets", Value: strings.Join(secrets, ",")})
	}
	if nameTransformer != "" {
		params = append(params, QueryParam{Key: "name_transformer", Value: nameTransformer})
	}
	if format != "" {
		params = append(params, QueryParam{Key: "format", Value: format})
	}

	url := fmt.Sprintf("%s/v4/configs/config/secrets/download", context.Host)
	req, reqErr := http.NewRequest("GET", url, nil)
	if reqErr != nil {
		return nil, &APIError{Err: reqErr, Message: "Unable to form request"}
	}
	query := req.URL.Query()
	for _, param := range params {
		query.Add(param.Key, param.Value)
	}
	req.URL.RawQuery = query.Encode()

	response, err := performRequest(context, req, http.StatusNotFound, http.StatusNotImplemented)
	if err != nil {
		return nil, err
	}

	statusCode := response.HTTPResponse.StatusCode
	if statusCode == http.StatusNotImplemented {
		// No v4 route on the server: durable/sticky signal, fall back to v3 permanently.
		return &GetSecretsV4Result{SecretsResult: nil, APISupported: false}, nil
	}
	if statusCode == http.StatusNotFound {
		// This specific config was not found: transient/per-config, fall back to v3 for
		// this cycle only and retry v4 next cycle. Server still supports v4.
		return &GetSecretsV4Result{SecretsResult: nil, APISupported: true, ConfigNotFound: true}, nil
	}

	pollETag := response.HTTPResponse.Header.Get("X-Poll-ETag")

	// Format defeats JSON parsing
	if format != "" {
		secrets := []models.Secret{{
			Name:  secretsDownloadFileKey,
			Value: string(response.Body),
		}}
		return &GetSecretsV4Result{
			SecretsResult: &models.SecretsResult{Modified: true, Secrets: secrets, ETag: "", PollETag: pollETag},
			APISupported:  true,
		}, nil
	}

	result, modelErr := parseSecrets(response.Body, "")
	if modelErr != nil {
		return nil, &APIError{Err: modelErr, Message: "Unable to parse secrets"}
	}
	result.PollETag = pollETag
	return &GetSecretsV4Result{SecretsResult: result, APISupported: true}, nil
}

func parseSecrets(response []byte, eTag string) (*models.SecretsResult, error) {
	var result map[string]string
	err := json.Unmarshal(response, &result)
	if err != nil {
		return nil, err
	}

	secrets := make([]models.Secret, 0)
	for key, value := range result {
		secret := models.Secret{Name: key, Value: value}
		secrets = append(secrets, secret)
	}
	sort.Slice(secrets, func(i, j int) bool {
		return secrets[i].Name < secrets[j].Name
	})
	return &models.SecretsResult{Modified: true, Secrets: secrets, ETag: eTag}, nil
}
