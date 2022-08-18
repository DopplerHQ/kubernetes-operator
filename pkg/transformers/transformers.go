package transformers

import (
	"strings"
)

func Camel(name string) string {
	var parts []string
	for i, part := range strings.Split(name, "_") {
		if len(part) == 0 {
			continue
		}

		firstChar := part[0:1]
		if i == 0 {
			firstChar = strings.ToLower(firstChar)
		} else {
			firstChar = strings.ToUpper(firstChar)
		}
		camel := firstChar + strings.ToLower(part[1:])
		parts = append(parts, camel)
	}
	return strings.Join(parts, "")
}

func UpperCamel(name string) string {
	var parts []string
	for _, part := range strings.Split(name, "_") {
		if len(part) == 0 {
			continue
		}

		upperCamel := strings.ToUpper(part[0:1]) + strings.ToLower(part[1:])
		parts = append(parts, upperCamel)
	}
	return strings.Join(parts, "")
}

func LowerSnake(name string) string {
	return strings.ToLower(name)
}

func TFVar(name string) string {
	return "TF_VAR_" + strings.ToLower(name)
}

func DotNETEnv(name string) string {
	var parts []string
	for _, part := range strings.Split(name, "__") {
		parts = append(parts, UpperCamel(part))
	}
	return strings.Join(parts, "__")
}

type TransformerFunc func(value string) string

type SecretsNameTransformer struct {
	Type string
	TransformerFunc
}

// Environment variable compatible name transformers
// Use formats for non-environment variable compatible transformations
var CamelTransformer = &SecretsNameTransformer{Type: "camel", TransformerFunc: Camel}
var UpperCamelTransformer = &SecretsNameTransformer{Type: "upper-camel", TransformerFunc: UpperCamel}
var LowerSnakeTransformer = &SecretsNameTransformer{Type: "lower-snake", TransformerFunc: LowerSnake}
var TFVarTransformer = &SecretsNameTransformer{Type: "tf-var", TransformerFunc: TFVar}
var DotNETEnvTransformer = &SecretsNameTransformer{Type: "dotnet-env", TransformerFunc: DotNETEnv}

var SecretsNameTransformersList = []*SecretsNameTransformer{
	UpperCamelTransformer,
	CamelTransformer,
	LowerSnakeTransformer,
	TFVarTransformer,
	DotNETEnvTransformer,
}

var SecretsNameTransformerTypes []string
var SecretsNameTransformers map[string]*SecretsNameTransformer

func init() {
	SecretsNameTransformers = map[string]*SecretsNameTransformer{}
	for _, transformer := range SecretsNameTransformersList {
		SecretsNameTransformerTypes = append(SecretsNameTransformerTypes, transformer.Type)
		SecretsNameTransformers[transformer.Type] = transformer
	}
}
