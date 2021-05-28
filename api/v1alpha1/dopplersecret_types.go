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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This file is meant to be modified as specs change.
// Important: Run "make" to regenerate code after modifying this file

// DopplerSecretSpec defines the desired state of DopplerSecret
type DopplerSecretSpec struct {
	// A Doppler service token, used to interact with the Doppler API
	ServiceToken string `json:"serviceToken,omitempty"`

	// The name of the Kubernetes secret where the operator will store the fetched secrets
	SecretName string `json:"secretName,omitempty"`
}

// DopplerSecretStatus defines the observed state of DopplerSecret
type DopplerSecretStatus struct{}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// DopplerSecret is the Schema for the dopplersecrets API
type DopplerSecret struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DopplerSecretSpec   `json:"spec,omitempty"`
	Status DopplerSecretStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DopplerSecretList contains a list of DopplerSecret
type DopplerSecretList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DopplerSecret `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DopplerSecret{}, &DopplerSecretList{})
}
