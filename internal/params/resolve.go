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

// Package params resolves an OIDCShim's ordered parameter list into the template
// values used to render the environment variables and files injected into a pod.
// It is a pure package: the caller supplies ConfigMap access as a callback.
package params

import (
	"context"
	"fmt"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
)

// Metadata is the annotations and labels of one object. Nil maps are fine.
type Metadata struct {
	Annotations map[string]string
	Labels      map[string]string
}

// Lookup holds the metadata of the objects consulted for annotation/label sources,
// in precedence order Pod > ServiceAccount > Namespace. Zero values mean "absent object".
type Lookup struct {
	Pod            Metadata
	ServiceAccount Metadata
	Namespace      Metadata
}

// Builtins are the template values every shim exposes in addition to its parameters.
type Builtins struct {
	PodNamespace       string
	ServiceAccountName string
	ShimName           string
	TokenDir           string
	TokenPath          string
}

// Builtin template keys.
const (
	KeyPodNamespace       = "podNamespace"
	KeyServiceAccountName = "serviceAccountName"
	KeyShimName           = "shimName"
	KeyTokenDir           = "tokenDir"
	KeyTokenPath          = "tokenPath"
)

// builtinKeys are the template keys Resolve always populates. Parameters may not use them.
var builtinKeys = []string{
	KeyPodNamespace,
	KeyServiceAccountName,
	KeyShimName,
	KeyTokenDir,
	KeyTokenPath,
}

// ConfigMapGetter returns the data of a ConfigMap, or (nil, nil) when it does not exist.
type ConfigMapGetter func(ctx context.Context, namespace, name string) (map[string]string, error)

// Values is the fully resolved template context (builtins + parameters).
type Values map[string]string

// Input is everything needed to resolve one shim's parameters for one pod.
type Input struct {
	Parameters         []v1alpha1.Parameter
	Lookup             Lookup
	Builtins           Builtins
	ConfigMapNamespace string          // namespace used for configMapKeyRef sources
	GetConfigMap       ConfigMapGetter // may be nil when no configMapKeyRef parameters exist
}

// MissingParameterError is returned when a required parameter resolves to nothing.
type MissingParameterError struct {
	Name   string
	Source string // human description, e.g. `annotation "iam.gke.io/gcp-project-number"`
}

func (e *MissingParameterError) Error() string {
	return fmt.Sprintf("required parameter %q has no value: %s yielded nothing and no default is set", e.Name, e.Source)
}

// Resolve resolves in.Parameters in order and returns the values, including builtins.
func Resolve(ctx context.Context, in Input) (Values, error) {
	values := Values{
		KeyPodNamespace:       in.Builtins.PodNamespace,
		KeyServiceAccountName: in.Builtins.ServiceAccountName,
		KeyShimName:           in.Builtins.ShimName,
		KeyTokenDir:           in.Builtins.TokenDir,
		KeyTokenPath:          in.Builtins.TokenPath,
	}

	for i := range in.Parameters {
		p := &in.Parameters[i]
		value, found, err := resolveOne(ctx, in, p, values)
		if err != nil {
			return nil, err
		}
		switch {
		case found:
			// use it as is, even when empty
		case p.Default != nil:
			value = *p.Default
		case p.Required:
			return nil, &MissingParameterError{Name: p.Name, Source: sourceDescription(p.ValueFrom)}
		default:
			value = ""
		}
		values[p.Name] = value
	}

	return values, nil
}

// resolveOne resolves a single parameter against the values resolved so far.
// found reports whether the source yielded a value; an empty value with found=true
// is a real value and takes precedence over the parameter's default.
func resolveOne(ctx context.Context, in Input, p *v1alpha1.Parameter, sofar Values) (string, bool, error) {
	src := p.ValueFrom
	switch {
	case src.Static != nil:
		return *src.Static, true, nil

	case src.Annotation != "":
		v, ok := lookupMetadata(in.Lookup, src.Annotation, func(m Metadata) map[string]string { return m.Annotations })
		return v, ok, nil

	case src.Label != "":
		v, ok := lookupMetadata(in.Lookup, src.Label, func(m Metadata) map[string]string { return m.Labels })
		return v, ok, nil

	case src.ConfigMapKeyRef != nil:
		if in.GetConfigMap == nil {
			return "", false, fmt.Errorf("parameter %q: configMapKeyRef requires a ConfigMap getter", p.Name)
		}
		data, err := in.GetConfigMap(ctx, in.ConfigMapNamespace, src.ConfigMapKeyRef.Name)
		if err != nil {
			return "", false, fmt.Errorf("parameter %q: reading ConfigMap %q: %w", p.Name, src.ConfigMapKeyRef.Name, err)
		}
		v, ok := data[src.ConfigMapKeyRef.Key]
		return v, ok, nil

	case src.Template != "":
		v, err := Render(p.Name, src.Template, sofar)
		if err != nil {
			return "", false, fmt.Errorf("parameter %q: %w", p.Name, err)
		}
		// An empty rendering counts as no value so that "default" still applies.
		return v, v != "", nil
	}

	return "", false, nil
}

// lookupMetadata returns the first value present in Pod, then ServiceAccount, then Namespace.
// A key that is present with an empty value wins over a lower-precedence non-empty value.
func lookupMetadata(l Lookup, key string, pick func(Metadata) map[string]string) (string, bool) {
	for _, m := range []Metadata{l.Pod, l.ServiceAccount, l.Namespace} {
		if v, ok := pick(m)[key]; ok {
			return v, true
		}
	}
	return "", false
}

// sourceDescription renders a parameter source for error messages.
func sourceDescription(src v1alpha1.ParameterSource) string {
	switch {
	case src.Static != nil:
		return "static value"
	case src.Annotation != "":
		return fmt.Sprintf("annotation %q", src.Annotation)
	case src.Label != "":
		return fmt.Sprintf("label %q", src.Label)
	case src.ConfigMapKeyRef != nil:
		return fmt.Sprintf("key %q of ConfigMap %q", src.ConfigMapKeyRef.Key, src.ConfigMapKeyRef.Name)
	case src.Template != "":
		return "template"
	}
	return "no source"
}
