/*
Copyright 2026.

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
	"k8s.io/apimachinery/pkg/runtime"
)

// SecretMirrorSpec defines the desired state of SecretMirror
type SecretMirrorSpec struct {
	// sourceSecret is the name of the Secret to copy. It must live in the same
	// namespace as this SecretMirror.
	// +required
	// +kubebuilder:validation:MinLength=1
	SourceSecret string `json:"sourceSecret"`

	// targetNamespaceSelector selects the namespaces that receive a copy.
	// An empty selector matches every namespace, so it is required rather than
	// optional - a typo that drops the field must not fan out cluster-wide.
	// +required
	TargetNamespaceSelector metav1.LabelSelector `json:"targetNamespaceSelector"`
}

// SecretMirrorStatus defines the observed state of SecretMirror.
type SecretMirrorStatus struct {
	// copies is the number of selected namespaces holding an up-to-date copy.
	// +optional
	Copies int32 `json:"copies"`

	// observedGeneration is the spec generation this status was calculated from.
	// A reader compares it to metadata.generation to tell whether the status
	// below describes the spec they are looking at.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions represent the current state of the SecretMirror resource.
	// Types used here are "Ready" and "SourceMissing".
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.sourceSecret`
// +kubebuilder:printcolumn:name="Copies",type=integer,JSONPath=`.status.copies`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SecretMirror is the Schema for the secretmirrors API
type SecretMirror struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of SecretMirror
	// +required
	Spec SecretMirrorSpec `json:"spec"`

	// status defines the observed state of SecretMirror
	// +optional
	Status SecretMirrorStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SecretMirrorList contains a list of SecretMirror
type SecretMirrorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SecretMirror `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &SecretMirror{}, &SecretMirrorList{})
		return nil
	})
}
