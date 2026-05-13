/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"testing"

	v1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
)

func TestIsDeploymentReferencingDopplerSecret(t *testing.T) {
	dopplerSecret := secretsv1alpha1.DopplerSecret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dopplersecret-test",
			Namespace: "doppler-operator-system",
		},
	}

	cases := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{
			name:        "matching annotation",
			annotations: map[string]string{deploymentDopplerSecretRefAnnotation: "doppler-operator-system/dopplersecret-test"},
			want:        true,
		},
		{
			name:        "different namespace",
			annotations: map[string]string{deploymentDopplerSecretRefAnnotation: "default/dopplersecret-test"},
			want:        false,
		},
		{
			name:        "different name",
			annotations: map[string]string{deploymentDopplerSecretRefAnnotation: "doppler-operator-system/other"},
			want:        false,
		},
		{
			name:        "missing annotation",
			annotations: map[string]string{},
			want:        false,
		},
		{
			name:        "nil annotations",
			annotations: nil,
			want:        false,
		},
		{
			name:        "name-only (no namespace prefix)",
			annotations: map[string]string{deploymentDopplerSecretRefAnnotation: "dopplersecret-test"},
			want:        false,
		},
	}

	r := &DopplerSecretReconciler{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deployment := v1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-deployment",
					Namespace:   "default",
					Annotations: tc.annotations,
				},
			}
			got := r.IsDeploymentReferencingDopplerSecret(deployment, dopplerSecret)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
