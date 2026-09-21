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

// Package config holds manager-level default options for the ztwim-oidc-shims
// operator: the spiffe-helper image and pull policy, the SPIFFE CSI driver and
// workload API socket location, and namespace exclusions. Nothing consumes
// these yet; a later task (the mutating pod webhook) will.
package config

import (
	"errors"
	"flag"
	"fmt"
	"path"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Options are the manager-level defaults that shims may override per spec.
type Options struct {
	SpiffeHelperImage           string
	SpiffeHelperImagePullPolicy corev1.PullPolicy
	SpiffeCSIDriver             string
	SpiffeSocketMountPath       string
	SpiffeSocketFile            string
	ExcludedNamespaces          []string // exact names, in addition to OperatorNamespace and ZTWIMNamespace
	ExcludedNamespacePrefixes   []string
	ZTWIMNamespace              string
	OperatorNamespace           string // from POD_NAMESPACE; may be empty outside a cluster
	MaxRenderedBytes            int    // upper bound on the total size of rendered per-pod files/config
}

// Default values.
const (
	DefaultSpiffeHelperImage     = "ghcr.io/spiffe/spiffe-helper:0.10.0"
	DefaultSpiffeCSIDriver       = "csi.spiffe.io"
	DefaultSpiffeSocketMountPath = "/spiffe-workload-api"
	DefaultSpiffeSocketFile      = "spire-agent.sock"
	DefaultZTWIMNamespace        = "zero-trust-workload-identity-manager"
	DefaultMaxRenderedBytes      = 64 * 1024
)

// Environment variable names consulted by ApplyEnv.
const (
	EnvSpiffeHelperImage        = "SPIFFE_HELPER_IMAGE"
	EnvRelatedImageSpiffeHelper = "RELATED_IMAGE_SPIFFE_HELPER"
	EnvPodNamespace             = "POD_NAMESPACE"
)

// Defaults returns Options with every default applied (pull policy IfNotPresent,
// prefixes ["kube-"], no extra excluded namespaces).
func Defaults() Options {
	return Options{
		SpiffeHelperImage:           DefaultSpiffeHelperImage,
		SpiffeHelperImagePullPolicy: corev1.PullIfNotPresent,
		SpiffeCSIDriver:             DefaultSpiffeCSIDriver,
		SpiffeSocketMountPath:       DefaultSpiffeSocketMountPath,
		SpiffeSocketFile:            DefaultSpiffeSocketFile,
		ExcludedNamespaces:          nil,
		ExcludedNamespacePrefixes:   []string{"kube-"},
		ZTWIMNamespace:              DefaultZTWIMNamespace,
		OperatorNamespace:           "",
		MaxRenderedBytes:            DefaultMaxRenderedBytes,
	}
}

// pullPolicyValue adapts corev1.PullPolicy (a string type) to flag.Value.
type pullPolicyValue corev1.PullPolicy

func (p *pullPolicyValue) String() string {
	if p == nil {
		return ""
	}
	return string(*p)
}

func (p *pullPolicyValue) Set(v string) error {
	*p = pullPolicyValue(v)
	return nil
}

// stringSliceValue implements flag.Value for a comma-separated []string flag,
// trimming whitespace around each element and dropping empty elements.
type stringSliceValue struct {
	target *[]string
}

func (s *stringSliceValue) String() string {
	if s == nil || s.target == nil {
		return ""
	}
	return strings.Join(*s.target, ",")
}

func (s *stringSliceValue) Set(v string) error {
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		result = append(result, p)
	}
	*s.target = result
	return nil
}

// BindFlags registers flags on fs that write into o. Flag defaults are the
// current values of o, so callers do Defaults() then ApplyEnv then BindFlags
// so that env < flags in precedence.
func (o *Options) BindFlags(fs *flag.FlagSet) {
	fs.StringVar(&o.SpiffeHelperImage, "spiffe-helper-image", o.SpiffeHelperImage,
		"Container image for the spiffe-helper sidecar/init container.")
	fs.Var((*pullPolicyValue)(&o.SpiffeHelperImagePullPolicy), "spiffe-helper-image-pull-policy",
		"Image pull policy for the spiffe-helper image (Always, IfNotPresent, or Never).")
	fs.StringVar(&o.SpiffeCSIDriver, "spiffe-csi-driver", o.SpiffeCSIDriver,
		"Name of the SPIFFE CSI driver used to mount the workload API socket.")
	fs.StringVar(&o.SpiffeSocketMountPath, "spiffe-socket-mount-path", o.SpiffeSocketMountPath,
		"Absolute path where the SPIFFE workload API socket volume is mounted.")
	fs.StringVar(&o.SpiffeSocketFile, "spiffe-socket-file", o.SpiffeSocketFile,
		"File name of the SPIFFE workload API socket within the mount path.")
	fs.Var(&stringSliceValue{target: &o.ExcludedNamespaces}, "excluded-namespaces",
		"Comma-separated list of additional namespace names to exclude from shim injection.")
	fs.Var(&stringSliceValue{target: &o.ExcludedNamespacePrefixes}, "excluded-namespace-prefixes",
		"Comma-separated list of namespace name prefixes to exclude from shim injection.")
	fs.StringVar(&o.ZTWIMNamespace, "ztwim-namespace", o.ZTWIMNamespace,
		"Namespace where the Zero Trust Workload Identity Manager is installed.")
	fs.IntVar(&o.MaxRenderedBytes, "max-rendered-bytes", o.MaxRenderedBytes,
		"Upper bound on the total size of rendered per-pod files/config.")
}

// ApplyEnv overlays environment fallbacks: SPIFFE_HELPER_IMAGE, then RELATED_IMAGE_SPIFFE_HELPER
// (first non-empty wins, in that order) for the helper image; POD_NAMESPACE for OperatorNamespace.
func (o *Options) ApplyEnv(getenv func(string) string) {
	if v := getenv(EnvSpiffeHelperImage); v != "" {
		o.SpiffeHelperImage = v
	} else if v := getenv(EnvRelatedImageSpiffeHelper); v != "" {
		o.SpiffeHelperImage = v
	}

	if v := getenv(EnvPodNamespace); v != "" {
		o.OperatorNamespace = v
	}
}

// Validate returns an error (errors.Join of all problems) when: image empty; pull policy not one of
// Always/IfNotPresent/Never; CSI driver empty; socket mount path not absolute; socket file empty or
// contains "/"; MaxRenderedBytes <= 0.
func (o Options) Validate() error {
	var errs []error

	if o.SpiffeHelperImage == "" {
		errs = append(errs, errors.New("spiffe helper image must not be empty"))
	}

	switch o.SpiffeHelperImagePullPolicy {
	case corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever:
	default:
		errs = append(errs, fmt.Errorf("spiffe helper image pull policy must be one of Always, IfNotPresent, Never: %q",
			o.SpiffeHelperImagePullPolicy))
	}

	if o.SpiffeCSIDriver == "" {
		errs = append(errs, errors.New("spiffe CSI driver must not be empty"))
	}

	if !path.IsAbs(o.SpiffeSocketMountPath) {
		errs = append(errs, fmt.Errorf("spiffe socket mount path must be absolute: %q", o.SpiffeSocketMountPath))
	}

	if o.SpiffeSocketFile == "" {
		errs = append(errs, errors.New("spiffe socket file must not be empty"))
	} else if strings.Contains(o.SpiffeSocketFile, "/") {
		errs = append(errs, fmt.Errorf("spiffe socket file must not contain '/': %q", o.SpiffeSocketFile))
	}

	if o.MaxRenderedBytes <= 0 {
		errs = append(errs, fmt.Errorf("max rendered bytes must be positive: %d", o.MaxRenderedBytes))
	}

	return errors.Join(errs...)
}

// SocketPath returns SpiffeSocketMountPath + "/" + SpiffeSocketFile (using path.Join).
func (o Options) SocketPath() string {
	return path.Join(o.SpiffeSocketMountPath, o.SpiffeSocketFile)
}

// ExcludedNamespaceNames returns ExcludedNamespaces plus OperatorNamespace and ZTWIMNamespace
// (non-empty, de-duplicated, sorted).
func (o Options) ExcludedNamespaceNames() []string {
	set := make(map[string]struct{}, len(o.ExcludedNamespaces)+2)

	add := func(n string) {
		if n != "" {
			set[n] = struct{}{}
		}
	}

	for _, n := range o.ExcludedNamespaces {
		add(n)
	}
	add(o.OperatorNamespace)
	add(o.ZTWIMNamespace)

	result := make([]string, 0, len(set))
	for n := range set {
		result = append(result, n)
	}
	sort.Strings(result)

	return result
}
