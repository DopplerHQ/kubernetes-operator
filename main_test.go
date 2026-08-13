package main

import (
	"testing"

	"k8s.io/apimachinery/pkg/labels"

	"github.com/DopplerHQ/kubernetes-operator/controllers"
)

// The selector decides what the operator holds in memory. Too narrow and secrets it needs
// are read live on every reconcile; too broad and it is back to caching the whole cluster,
// which is the bug this work exists to fix.
func TestCachedSecretSelector(t *testing.T) {
	selector := cachedSecretSelector

	cached := []map[string]string{
		{controllers.ManagedSecretLabelKey: controllers.ManagedSecretLabelValue},
		{controllers.ManagedSecretLabelKey: controllers.TokenSecretLabelValue},
		{controllers.ManagedSecretLabelKey: controllers.ManagedSecretLabelValue, "app": "other"},
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
		{controllers.ManagedSecretLabelKey: "somethingElse"},
	}
	for _, l := range notCached {
		if selector.Matches(labels.Set(l)) {
			t.Errorf("expected selector NOT to match %v; the cluster's other secrets must stay out of the cache", l)
		}
	}
}
