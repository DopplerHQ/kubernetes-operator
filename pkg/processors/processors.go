package processors

import (
	"encoding/base64"
	"fmt"
)

type ProcessorFunc func(value string) ([]byte, error)

func processPlain(value string) ([]byte, error) {
	return []byte(value), nil
}

func processBase64(value string) ([]byte, error) {
	decodedData, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("Failed to decode base64 string: %w", err)
	}
	return decodedData, nil
}

var All = map[string]ProcessorFunc{
	"plain":  processPlain,
	"base64": processBase64,
}
