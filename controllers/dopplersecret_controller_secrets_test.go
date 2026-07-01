package controllers

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- Finding 5: version annotation is derived from a byte-compare against the existing
// managed Secret's data, not from auth-material-keyed content hashing (the removed
// computeContentVersion HMAC, which was keyed by apiContext.APIKey and therefore churned
// under short-lived OIDC-exchanged keys). ---

func TestComputeV4VersionAnnotationFirstCreateIsNonEmpty(t *testing.T) {
	newData := map[string][]byte{"HELLO": []byte("world")}

	version, err := computeV4VersionAnnotation(nil, newData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version == "" {
		t.Fatalf("expected non-empty version on first create")
	}
}

func TestComputeV4VersionAnnotationUnchangedWhenDataIdentical(t *testing.T) {
	data := map[string][]byte{"HELLO": []byte("world"), "DOPPLER_PROJECT": []byte("proj")}
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{kubeSecretVersionAnnotation: "existing-version-abc"},
		},
		Data: map[string][]byte{"HELLO": []byte("world"), "DOPPLER_PROJECT": []byte("proj")},
	}

	// Re-applying identical content (as if from a fresh process, i.e. no in-memory state
	// carried over) must reproduce the same annotation value, since the comparison is
	// against the persisted Kubernetes Secret rather than any process-local cache.
	version, err := computeV4VersionAnnotation(existing, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "existing-version-abc" {
		t.Fatalf("expected version to carry forward unchanged, got %q", version)
	}
}

func TestComputeV4VersionAnnotationChangesWhenDataDiffers(t *testing.T) {
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{kubeSecretVersionAnnotation: "existing-version-abc"},
		},
		Data: map[string][]byte{"HELLO": []byte("world")},
	}
	newData := map[string][]byte{"HELLO": []byte("planet")}

	version, err := computeV4VersionAnnotation(existing, newData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version == "" {
		t.Fatalf("expected non-empty version")
	}
	if version == "existing-version-abc" {
		t.Fatalf("expected version to change when data differs")
	}
}

func TestComputeV4VersionAnnotationFreshWhenExistingAnnotationEmpty(t *testing.T) {
	data := map[string][]byte{"HELLO": []byte("world")}
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}},
		Data:       data,
	}

	version, err := computeV4VersionAnnotation(existing, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version == "" {
		t.Fatalf("expected a freshly-minted version when the existing annotation is empty, even though data is identical")
	}
}

func TestComputeV4VersionAnnotationNotKeyedByAuthMaterial(t *testing.T) {
	// Regression guard for the removed HMAC design: the version must not depend on any
	// auth-derived input at all (there is no such parameter any more), and must be stable
	// across repeated calls for the same existing/new data pair via the carry-forward path.
	data := map[string][]byte{"HELLO": []byte("world")}
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{kubeSecretVersionAnnotation: "v1"}},
		Data:       data,
	}

	v1, err := computeV4VersionAnnotation(existing, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v2, err := computeV4VersionAnnotation(existing, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v1 != "v1" || v2 != "v1" {
		t.Fatalf("expected stable carry-forward version across repeated calls, got %q and %q", v1, v2)
	}
}
