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
	if err != nil {
		return nil, &APIError{Err: err, Message: "Unable to parse response data"}
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
