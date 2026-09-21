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

// Package injection turns a matched shim plus its already-resolved parameter values
// into a fully rendered Plan, and applies plans to a corev1.Pod.
//
// It is a pure package: it never talks to the API server. The pod webhook resolves
// parameters (internal/params), calls BuildPlan once per matching shim and then hands
// the plans to Apply.
package injection

import (
	"fmt"
	"path"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
	"github.com/kenmoini/ztwim-oidc-shims/internal/params"
	"github.com/kenmoini/ztwim-oidc-shims/internal/spiffehelper"
)

// Default token settings applied when a shim does not override them.
const (
	defaultTokenVolumePrefix = "oidcshim-token-"
	defaultTokenMountRoot    = "/var/run/secrets/oidcshim"
	defaultTokenFileName     = "token"
	defaultTokenFileMode     = "0644"
	defaultFileMode          = "0644"
	defaultServiceAccount    = "default"
)

// Defaults are the manager-level values a shim may override via spec.spiffe / spec.helper.
type Defaults struct {
	HelperImage           string
	HelperImagePullPolicy corev1.PullPolicy
	CSIDriver             string
	SocketMountPath       string
	SocketFile            string
	// MaxRenderedBytes caps len(HelperConf) + sum(len(file.Content)); 0 means no cap.
	MaxRenderedBytes int
}

// Token is the resolved token file location for one shim.
type Token struct {
	VolumeName string
	MountPath  string
	FileName   string
	FileMode   string
}

// ResolveToken applies the per-shim defaults over spec.
func ResolveToken(shimName string, spec v1alpha1.TokenSpec) Token {
	tok := Token{
		VolumeName: spec.VolumeName,
		MountPath:  spec.MountPath,
		FileName:   spec.FileName,
		FileMode:   spec.FileMode,
	}
	if tok.VolumeName == "" {
		tok.VolumeName = defaultTokenVolumePrefix + shimName
	}
	if tok.MountPath == "" {
		tok.MountPath = path.Join(defaultTokenMountRoot, shimName)
	}
	if tok.FileName == "" {
		tok.FileName = defaultTokenFileName
	}
	if tok.FileMode == "" {
		tok.FileMode = defaultTokenFileMode
	}
	return tok
}

// Path returns the full path of the token file inside the pod.
func (t Token) Path() string { return path.Join(t.MountPath, t.FileName) }

// Builtins returns the template builtins for shim/pod. podNamespace is passed explicitly
// because pod.Namespace may be empty during admission.
func Builtins(shim v1alpha1.Shim, pod *corev1.Pod, podNamespace string, tok Token) params.Builtins {
	serviceAccount := defaultServiceAccount
	if pod != nil && pod.Spec.ServiceAccountName != "" {
		serviceAccount = pod.Spec.ServiceAccountName
	}
	return params.Builtins{
		PodNamespace:       podNamespace,
		ServiceAccountName: serviceAccount,
		ShimName:           shim.GetName(),
		TokenDir:           tok.MountPath,
		TokenPath:          tok.Path(),
	}
}

// RenderedFile is one inject.files entry after templating.
type RenderedFile struct {
	Path    string
	Content string
	Mode    string // octal string, e.g. "0644"
}

// Plan is everything Apply needs for one shim, fully rendered.
type Plan struct {
	ShimName      string
	ShimKey       string
	ClusterScoped bool
	Token         Token
	HelperConf    string
	Files         []RenderedFile
	Env           []corev1.EnvVar
	// Containers comes from spec.inject.containers; empty means all app containers.
	Containers            []string
	CSIDriver             string
	SocketMountPath       string
	SocketFile            string
	HelperImage           string
	HelperImagePullPolicy corev1.PullPolicy
	HelperResources       *corev1.ResourceRequirements
	HelperSecurityContext *corev1.SecurityContext
}

// BuildPlan renders shim's audience, extraAudiences, env values and file contents over
// values, renders the spiffe-helper config, and resolves spiffe/helper overrides over d.
func BuildPlan(shim v1alpha1.Shim, values params.Values, tok Token, d Defaults) (*Plan, error) {
	spec := shim.ShimSpec()
	name := shim.GetName()

	plan := &Plan{
		ShimName:              name,
		ShimKey:               shim.ShimKey(),
		ClusterScoped:         shim.IsClusterScoped(),
		Token:                 tok,
		Containers:            append([]string(nil), spec.Inject.Containers...),
		CSIDriver:             d.CSIDriver,
		SocketMountPath:       d.SocketMountPath,
		SocketFile:            d.SocketFile,
		HelperImage:           d.HelperImage,
		HelperImagePullPolicy: d.HelperImagePullPolicy,
	}

	if s := spec.SPIFFE; s != nil {
		if s.CSIDriver != "" {
			plan.CSIDriver = s.CSIDriver
		}
		if s.SocketMountPath != "" {
			plan.SocketMountPath = s.SocketMountPath
		}
		if s.SocketFile != "" {
			plan.SocketFile = s.SocketFile
		}
	}
	if h := spec.Helper; h != nil {
		if h.Image != "" {
			plan.HelperImage = h.Image
		}
		if h.ImagePullPolicy != "" {
			plan.HelperImagePullPolicy = h.ImagePullPolicy
		}
		// Deep copy: the shim usually comes from a shared informer cache and the plan
		// must not hand the pod aliases into it.
		plan.HelperResources = h.Resources.DeepCopy()
		plan.HelperSecurityContext = h.SecurityContext.DeepCopy()
	}

	audience, err := params.Render("audience", spec.Audience, values)
	if err != nil {
		return nil, fmt.Errorf("injection: shim %q: render audience: %w", name, err)
	}

	var extraAudiences []string
	for i, a := range spec.ExtraAudiences {
		rendered, err := params.Render("extraAudience", a, values)
		if err != nil {
			return nil, fmt.Errorf("injection: shim %q: render extraAudiences[%d]: %w", name, i, err)
		}
		extraAudiences = append(extraAudiences, rendered)
	}

	for i, e := range spec.Inject.Env {
		value, err := params.Render("env", e.Value, values)
		if err != nil {
			return nil, fmt.Errorf("injection: shim %q: render inject.env[%d] %q: %w", name, i, e.Name, err)
		}
		plan.Env = append(plan.Env, corev1.EnvVar{Name: e.Name, Value: value})
	}

	renderedBytes := 0
	for i, f := range spec.Inject.Files {
		content, err := params.Render("file", f.Content, values)
		if err != nil {
			return nil, fmt.Errorf("injection: shim %q: render inject.files[%d] %q: %w", name, i, f.Path, err)
		}
		mode := f.Mode
		if mode == "" {
			mode = defaultFileMode
		}
		if _, err := parseFileMode(mode); err != nil {
			return nil, fmt.Errorf("injection: shim %q: inject.files[%d] %q: %w", name, i, f.Path, err)
		}
		plan.Files = append(plan.Files, RenderedFile{Path: f.Path, Content: content, Mode: mode})
		renderedBytes += len(content)
	}

	helperConf, err := spiffeHelperConf(plan, audience, extraAudiences)
	if err != nil {
		return nil, fmt.Errorf("injection: shim %q: render helper config: %w", name, err)
	}
	plan.HelperConf = helperConf
	renderedBytes += len(helperConf)

	if d.MaxRenderedBytes > 0 && renderedBytes > d.MaxRenderedBytes {
		return nil, fmt.Errorf("injection: shim %q: rendered content is %d bytes, exceeds the %d byte limit",
			name, renderedBytes, d.MaxRenderedBytes)
	}

	return plan, nil
}

// spiffeHelperConf renders the spiffe-helper configuration for plan.
func spiffeHelperConf(plan *Plan, audience string, extraAudiences []string) (string, error) {
	return spiffehelper.Render(spiffehelper.Config{
		AgentAddress:      path.Join(plan.SocketMountPath, plan.SocketFile),
		CertDir:           plan.Token.MountPath,
		JWTAudience:       audience,
		JWTExtraAudiences: extraAudiences,
		JWTSVIDFileName:   plan.Token.FileName,
		JWTSVIDFileMode:   plan.Token.FileMode,
	})
}

// parseFileMode converts an octal mode string such as "0644" into the int32 Kubernetes wants.
func parseFileMode(mode string) (int32, error) {
	parsed, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid octal file mode", mode)
	}
	return int32(parsed), nil
}
