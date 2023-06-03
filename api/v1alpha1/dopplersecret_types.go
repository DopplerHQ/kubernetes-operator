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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This file is meant to be modified as specs change.
// Important: Run "make" to regenerate code after modifying this file

// A reference to a Kubernetes secret
type SecretReference struct {
	// The name of the Secret resource
	Name string `json:"name"`

	// Namespace of the resource being referred to. Ignored if not cluster scoped
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

type SecretProcessor struct {
	// The type of process to be performed, either "plain" or "base64"
	// +kubebuilder:validation:Enum=plain;base64
	Type string `json:"type"`
}

type SecretProcessors map[string]*SecretProcessor

var DefaultProcessor = SecretProcessor{Type: "plain"}

// DopplerSecretSpec defines the desired state of DopplerSecret
type DopplerSecretSpec struct {
	// The Kubernetes secret containing the Doppler service token
	TokenSecretRef SecretReference `json:"tokenSecret,omitempty"`

	// The Kubernetes secret where the operator will store and sync the fetched secrets
	ManagedSecretRef SecretReference `json:"managedSecret,omitempty"`

	// The Doppler project
	// +optional
	Project string `json:"project,omitempty"`

	// The Doppler config
	// +optional
	Config string `json:"config,omitempty"`

	// A list of processors to transform the data during ingestion
	// +kubebuilder:default={}
	Processors SecretProcessors `json:"processors,omitempty"`

	// The Doppler API host
	// +kubebuilder:default="https://api.doppler.com"
	Host string `json:"host,omitempty"`

	// The Doppler secret type is transferred to the managed secret type
	// +kubebuilder:validation:Enum=Opaque;kubernetes.io/tls;kubernetes.io/service-account-token;kubernetes.io/dockercfg;kubernetes.io/dockerconfigjson;kubernetes.io/basic-auth;kubernetes.io/ssh-auth;bootstrap.kubernetes.io/token
	// +kubebuilder:default=Opaque
	// +optional
	SecretType string `json:"type,omitempty"`

	// Whether or not to verify TLS
	// +kubebuilder:default=true
	VerifyTLS bool `json:"verifyTLS,omitempty"`

	// The environment variable compatible secrets name transformer to apply
	// +kubebuilder:validation:Enum=upper-camel;camel;lower-snake;tf-var;dotnet-env;tls-var
	// +optional
	NameTransformer string `json:"nameTransformer,omitempty"`

	// Format enables the downloading of secrets as a file
	// +kubebuilder:validation:Enum=json;dotnet-json;env;yaml;docker
	// +optional
	Format string `json:"format,omitempty"`

	// The number of seconds to wait between resyncs
	// +kubebuilder:default=60
	ResyncSeconds int64 `json:"resyncSeconds,omitempty"`
}

// DopplerSecretStatus defines the observed state of DopplerSecret
type DopplerSecretStatus struct {
	Conditions []metav1.Condition `json:"conditions"`
}

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

func (d DopplerSecret) GetNamespacedName() string {
	return fmt.Sprintf("%s/%s", d.Namespace, d.Name)
}

func init() {
	SchemeBuilder.Register(&DopplerSecret{}, &DopplerSecretList{})
}
