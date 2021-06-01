package models

import (
	"encoding/json"
	"sort"
)

type Secret struct {
	Name  string
	Value string
}

type SecretsResult struct {
	Modified bool
	Secrets  []Secret
	ETag     string
}

func ParseSecrets(statusCode int, response []byte, eTag string) (*SecretsResult, error) {
	if statusCode == 304 {
		return &SecretsResult{Modified: false, Secrets: nil, ETag: ""}, nil
	}

	var result map[string]string
	err := json.Unmarshal(response, &result)
	if err != nil {
		return nil, err
	}

	secrets := make([]Secret, 0)
	for key, value := range result {
		secret := Secret{Name: key, Value: value}
		secrets = append(secrets, secret)
	}
	sort.Slice(secrets, func(i, j int) bool {
		return secrets[i].Name < secrets[j].Name
	})
	return &SecretsResult{Modified: true, Secrets: secrets, ETag: eTag}, nil
}
