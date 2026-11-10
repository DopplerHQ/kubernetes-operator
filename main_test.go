package main

import (
	"testing"

	"k8s.io/apimachinery/pkg/labels"

	"github.com/DopplerHQ/kubernetes-operator/controllers"
)

// The selector must match managed and opted-in token secrets, and nothing else.
func TestCachedSecretSelector(t *testing.T) {
	selector := cachedSecretSelector

	cached := []map[string]string{
		{controllers.SubtypeLabelKey: controllers.ManagedSecretLabelValue},
		{controllers.SubtypeLabelKey: controllers.TokenSecretLabelValue},
		{controllers.SubtypeLabelKey: controllers.ManagedSecretLabelValue, "app": "other"},
	}
	for _, l := range cached {
		if !selector.Matches(labels.Set(l)) {
			t.Errorf("expected selector to match %v", l)
		}
	}

	notCached := []map[string]string{
		{},
		{"app": "unrelated"},
		{"owner": "helm", "name": "sh.helm.release.v1.foo.v1"},
		{controllers.SubtypeLabelKey: "somethingElse"},
	}
	for _, l := range notCached {
		if selector.Matches(labels.Set(l)) {
			t.Errorf("expected selector not to match %v", l)
		}
	}
}
