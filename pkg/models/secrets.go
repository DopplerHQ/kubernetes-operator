package models

type Secret struct {
	Name  string
	Value string
}

type SecretsResult struct {
	Modified bool
	Secrets  []Secret
	ETag     string
}
