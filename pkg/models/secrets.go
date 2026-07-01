package models

type Secret struct {
	Name  string
	Value string
}

type SecretsResult struct {
	Modified bool
	Secrets  []Secret
	ETag     string
	// PollETag is the fresh etag returned by the v4 download endpoint (X-Poll-ETag
	// response header), for use with a subsequent PollSecretsChange call. Empty when
	// the server omitted the header, e.g. when dynamic secret leases are present.
	PollETag string
}
