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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Shim is implemented by both OIDCShim and ClusterOIDCShim so that the webhook and
// controllers can treat them uniformly.
// +kubebuilder:object:generate=false
type Shim interface {
	client.Object
	// ShimSpec returns the shared spec.
	ShimSpec() *OIDCShimSpec
	// ShimStatus returns the shared status.
	ShimStatus() *OIDCShimStatus
	// IsClusterScoped reports whether this is a ClusterOIDCShim.
	IsClusterScoped() bool
	// ConfigMapNamespaceFor returns the namespace in which configMapKeyRef parameters
	// are resolved for a pod in podNamespace.
	ConfigMapNamespaceFor(podNamespace string) string
	// ShimKey returns a stable human-readable identifier for logs and annotations.
	ShimKey() string
}

var (
	_ Shim = &OIDCShim{}
	_ Shim = &ClusterOIDCShim{}
)

// ShimSpec implements Shim.
func (s *OIDCShim) ShimSpec() *OIDCShimSpec { return &s.Spec }

// ShimStatus implements Shim.
func (s *OIDCShim) ShimStatus() *OIDCShimStatus { return &s.Status }

// IsClusterScoped implements Shim.
func (s *OIDCShim) IsClusterScoped() bool { return false }

// ConfigMapNamespaceFor implements Shim.
func (s *OIDCShim) ConfigMapNamespaceFor(string) string { return s.Namespace }

// ShimKey implements Shim.
func (s *OIDCShim) ShimKey() string { return "OIDCShim/" + s.Namespace + "/" + s.Name }

// ShimSpec implements Shim.
func (s *ClusterOIDCShim) ShimSpec() *OIDCShimSpec { return &s.Spec.OIDCShimSpec }

// ShimStatus implements Shim.
func (s *ClusterOIDCShim) ShimStatus() *OIDCShimStatus { return &s.Status }

// IsClusterScoped implements Shim.
func (s *ClusterOIDCShim) IsClusterScoped() bool { return true }

// ConfigMapNamespaceFor implements Shim.
func (s *ClusterOIDCShim) ConfigMapNamespaceFor(podNamespace string) string {
	if s.Spec.ConfigMapNamespace != "" {
		return s.Spec.ConfigMapNamespace
	}
	return podNamespace
}

// ShimKey implements Shim.
func (s *ClusterOIDCShim) ShimKey() string { return "ClusterOIDCShim/" + s.Name }
