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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GroupPrefix is the prefix used for every annotation and label owned by this operator.
const GroupPrefix = "oidcshim.kemo.dev"

// Annotation and label keys consumed or written by the pod webhook.
const (
	// EnrollmentKey is the annotation or label key (on a Namespace, ServiceAccount or Pod)
	// whose value is a comma-separated list of shim names to enroll the object into.
	EnrollmentKey = GroupPrefix + "/shim"
	// InjectAnnotation set to "false" on a Pod opts it out of injection entirely.
	InjectAnnotation = GroupPrefix + "/inject"
	// StatusAnnotation is written by the webhook once a Pod has been mutated.
	StatusAnnotation = GroupPrefix + "/status"
	// ShimsAnnotation lists the shim keys that were applied to the Pod.
	ShimsAnnotation = GroupPrefix + "/shims"
	// ContainersAnnotation on a Pod restricts which app containers receive env/files.
	ContainersAnnotation = GroupPrefix + "/containers"
	// HelperConfAnnotationPrefix + "<shim>" carries the rendered spiffe-helper config.
	HelperConfAnnotationPrefix = GroupPrefix + "/helper-conf-"
	// FileAnnotationPrefix + "<shim>-<index>" carries a rendered inject.files entry.
	FileAnnotationPrefix = GroupPrefix + "/file-"
	// ShimLabelPrefix + "<shim>" is set on injected Pods so they can be listed per shim.
	ShimLabelPrefix = GroupPrefix + "/shim-"
	// WebhookNamespaceLabel set to "disabled" on a Namespace excludes it via the webhook namespaceSelector.
	WebhookNamespaceLabel = GroupPrefix + "/webhook"

	// StatusInjected is the value of StatusAnnotation after mutation.
	StatusInjected = "injected"
	// ShimLabelValueNamespaced marks a label written for an OIDCShim.
	ShimLabelValueNamespaced = "namespaced"
	// ShimLabelValueCluster marks a label written for a ClusterOIDCShim.
	ShimLabelValueCluster = "cluster"
	// WebhookDisabled is the value of WebhookNamespaceLabel that excludes a namespace.
	WebhookDisabled = "disabled"
)

// Condition types and reasons reported in status.
const (
	ConditionReady    = "Ready"
	ReasonValid       = "Valid"
	ReasonInvalidSpec = "InvalidSpec"
)

// ShimLabelKey returns the per-shim label key written on injected pods.
func ShimLabelKey(shimName string) string { return ShimLabelPrefix + shimName }

// HelperConfAnnotation returns the annotation key carrying the rendered helper config for a shim.
func HelperConfAnnotation(shimName string) string { return HelperConfAnnotationPrefix + shimName }

// Provider is an informational hint about the federated identity provider the shim targets.
// +kubebuilder:validation:Enum=google;aws;azure;generic
type Provider string

const (
	ProviderGoogle  Provider = "google"
	ProviderAWS     Provider = "aws"
	ProviderAzure   Provider = "azure"
	ProviderGeneric Provider = "generic"
)

// OIDCShimSpec is the shared desired state of an OIDCShim or ClusterOIDCShim.
type OIDCShimSpec struct {
	// Provider is an informational hint about the target identity provider.
	// +optional
	Provider Provider `json:"provider,omitempty"`

	// Selection controls which pods this shim is injected into.
	Selection SelectionSpec `json:"selection"`

	// Parameters are resolved in order for each matched pod and exposed to templates
	// by name. A parameter template may reference any parameter defined before it.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Parameters []Parameter `json:"parameters,omitempty"`

	// Audience is the JWT-SVID audience requested from the SPIRE agent. Templated.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	Audience string `json:"audience"`

	// ExtraAudiences are additional audiences added to the JWT-SVID. Templated.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=2048
	ExtraAudiences []string `json:"extraAudiences,omitempty"`

	// Token controls where the JWT-SVID file is written inside the pod.
	// +optional
	Token TokenSpec `json:"token,omitempty"`

	// Inject describes the environment variables and files added to app containers.
	// +optional
	Inject InjectSpec `json:"inject,omitempty"`

	// SPIFFE overrides the operator-level defaults for reaching the SPIRE agent.
	// +optional
	SPIFFE *SPIFFESpec `json:"spiffe,omitempty"`

	// Helper overrides the operator-level defaults for the injected spiffe-helper containers.
	// +optional
	Helper *HelperSpec `json:"helper,omitempty"`
}

// SelectionSpec decides whether a pod is matched by a shim. A pod matches when it is
// enrolled (see Enrollment) OR when at least one selector is set and every set selector
// matches its target object.
type SelectionSpec struct {
	// Enrollment controls which object kinds are consulted for the opt-in
	// "oidcshim.kemo.dev/shim" annotation or label. All kinds are consulted when unset.
	// +optional
	Enrollment *EnrollmentSpec `json:"enrollment,omitempty"`

	// NamespaceSelector is matched against the pod's Namespace labels.
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// ServiceAccountSelector is matched against the pod's ServiceAccount labels.
	// +optional
	ServiceAccountSelector *metav1.LabelSelector `json:"serviceAccountSelector,omitempty"`

	// PodSelector is matched against the pod's labels.
	// +optional
	PodSelector *metav1.LabelSelector `json:"podSelector,omitempty"`
}

// EnrollmentSpec toggles opt-in enrollment per object kind.
type EnrollmentSpec struct {
	// +optional
	// +kubebuilder:default=true
	Namespaces *bool `json:"namespaces,omitempty"`
	// +optional
	// +kubebuilder:default=true
	ServiceAccounts *bool `json:"serviceAccounts,omitempty"`
	// +optional
	// +kubebuilder:default=true
	Pods *bool `json:"pods,omitempty"`
}

// Parameter is a named value resolved per pod and exposed to templates.
type Parameter struct {
	// Name is the template variable name.
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// ValueFrom is the source of the value.
	ValueFrom ParameterSource `json:"valueFrom"`

	// Default is used when the source yields no value.
	// +optional
	Default *string `json:"default,omitempty"`

	// Required causes injection of this shim to be skipped (with a warning)
	// when no value and no default is available.
	// +optional
	Required bool `json:"required,omitempty"`
}

// ParameterSource selects exactly one value source.
// +kubebuilder:validation:XValidation:rule="(has(self.static)?1:0)+(has(self.annotation)?1:0)+(has(self.label)?1:0)+(has(self.configMapKeyRef)?1:0)+(has(self.template)?1:0)==1",message="exactly one of static, annotation, label, configMapKeyRef or template must be set"
type ParameterSource struct {
	// Static is a literal value.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Static *string `json:"static,omitempty"`

	// Annotation is looked up on the Pod, then its ServiceAccount, then its Namespace.
	// +optional
	// +kubebuilder:validation:MaxLength=317
	Annotation string `json:"annotation,omitempty"`

	// Label is looked up on the Pod, then its ServiceAccount, then its Namespace.
	// +optional
	// +kubebuilder:validation:MaxLength=317
	Label string `json:"label,omitempty"`

	// ConfigMapKeyRef reads a key from a ConfigMap in the pod's namespace
	// (or spec.configMapNamespace for a ClusterOIDCShim).
	// +optional
	ConfigMapKeyRef *ConfigMapKeySelector `json:"configMapKeyRef,omitempty"`

	// Template is a Go text/template rendered over previously resolved parameters.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Template string `json:"template,omitempty"`
}

// ConfigMapKeySelector identifies a key in a ConfigMap.
type ConfigMapKeySelector struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// TokenSpec controls the JWT-SVID token file.
type TokenSpec struct {
	// VolumeName of the in-memory emptyDir holding the token. Defaults to "oidcshim-token-<shim>".
	// +optional
	VolumeName string `json:"volumeName,omitempty"`

	// MountPath of the token directory. Defaults to "/var/run/secrets/oidcshim/<shim>".
	// +optional
	// +kubebuilder:validation:Pattern=`^/`
	MountPath string `json:"mountPath,omitempty"`

	// FileName of the token inside MountPath.
	// +optional
	// +kubebuilder:default=token
	FileName string `json:"fileName,omitempty"`

	// FileMode of the token file, as an octal string.
	// +optional
	// +kubebuilder:default="0644"
	// +kubebuilder:validation:Pattern=`^0?[0-7]{3}$`
	FileMode string `json:"fileMode,omitempty"`
}

// InjectSpec describes what is added to the app containers.
type InjectSpec struct {
	// Env variables appended to each targeted container. Values are templated.
	// Existing variables with the same name are left untouched.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Env []EnvVar `json:"env,omitempty"`

	// Files rendered and mounted into each targeted container.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	Files []FileSpec `json:"files,omitempty"`

	// Containers restricts injection to the named app containers. Empty means all.
	// The pod annotation "oidcshim.kemo.dev/containers" takes precedence.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=63
	Containers []string `json:"containers,omitempty"`
}

// EnvVar is a templated environment variable.
type EnvVar struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	Value string `json:"value,omitempty"`
}

// FileSpec is a templated file mounted into app containers.
type FileSpec struct {
	// Path is the absolute path of the file inside the container.
	// +kubebuilder:validation:Pattern=`^/`
	Path string `json:"path"`

	// Content is a Go text/template rendered over the resolved parameters.
	// +kubebuilder:validation:MaxLength=65536
	Content string `json:"content"`

	// Mode of the file, as an octal string.
	// +optional
	// +kubebuilder:default="0644"
	// +kubebuilder:validation:Pattern=`^0?[0-7]{3}$`
	Mode string `json:"mode,omitempty"`
}

// SPIFFESpec overrides how the SPIRE agent Workload API is reached.
type SPIFFESpec struct {
	// CSIDriver name providing the agent socket. Defaults to "csi.spiffe.io".
	// +optional
	CSIDriver string `json:"csiDriver,omitempty"`

	// SocketMountPath where the CSI volume is mounted in helper containers.
	// +optional
	// +kubebuilder:validation:Pattern=`^/`
	SocketMountPath string `json:"socketMountPath,omitempty"`

	// SocketFile name inside SocketMountPath. Defaults to "spire-agent.sock".
	// +optional
	SocketFile string `json:"socketFile,omitempty"`
}

// HelperSpec overrides the injected spiffe-helper containers.
type HelperSpec struct {
	// +optional
	Image string `json:"image,omitempty"`
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`
}

// OIDCShimStatus is the observed state shared by OIDCShim and ClusterOIDCShim.
type OIDCShimStatus struct {
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the generation last validated by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// MatchedPods is the number of pods currently carrying this shim's label.
	// +optional
	MatchedPods *int32 `json:"matchedPods,omitempty"`
}
