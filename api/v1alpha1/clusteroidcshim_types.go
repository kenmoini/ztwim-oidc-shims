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
)

// ClusterOIDCShimSpec is the desired state of a ClusterOIDCShim.
type ClusterOIDCShimSpec struct {
	OIDCShimSpec `json:",inline"`

	// ConfigMapNamespace is where configMapKeyRef parameters are resolved.
	// Defaults to the pod's namespace.
	// +optional
	ConfigMapNamespace string `json:"configMapNamespace,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Pods",type=integer,JSONPath=`.status.matchedPods`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 46",message="metadata.name must be at most 46 characters (it is embedded in container and volume names)"

// ClusterOIDCShim injects SPIFFE JWT-SVID based OIDC federation into pods in any namespace.
type ClusterOIDCShim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterOIDCShimSpec `json:"spec,omitempty"`
	Status OIDCShimStatus      `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterOIDCShimList contains a list of ClusterOIDCShim.
type ClusterOIDCShimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterOIDCShim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterOIDCShim{}, &ClusterOIDCShimList{})
}
