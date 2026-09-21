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

package params

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
)

func ptr(s string) *string { return &s }

func annotationParam(name, key string) v1alpha1.Parameter {
	return v1alpha1.Parameter{Name: name, ValueFrom: v1alpha1.ParameterSource{Annotation: key}}
}

func labelParam(name, key string) v1alpha1.Parameter {
	return v1alpha1.Parameter{Name: name, ValueFrom: v1alpha1.ParameterSource{Label: key}}
}

func TestResolveAnnotationAndLabelPrecedence(t *testing.T) {
	tests := []struct {
		name   string
		param  v1alpha1.Parameter
		lookup Lookup
		want   string
	}{
		{
			name:  "annotation pod wins over serviceaccount and namespace",
			param: annotationParam("p", "k"),
			lookup: Lookup{
				Pod:            Metadata{Annotations: map[string]string{"k": "pod"}},
				ServiceAccount: Metadata{Annotations: map[string]string{"k": "sa"}},
				Namespace:      Metadata{Annotations: map[string]string{"k": "ns"}},
			},
			want: "pod",
		},
		{
			name:  "annotation serviceaccount wins over namespace",
			param: annotationParam("p", "k"),
			lookup: Lookup{
				ServiceAccount: Metadata{Annotations: map[string]string{"k": "sa"}},
				Namespace:      Metadata{Annotations: map[string]string{"k": "ns"}},
			},
			want: "sa",
		},
		{
			name:  "annotation falls back to namespace",
			param: annotationParam("p", "k"),
			lookup: Lookup{
				Namespace: Metadata{Annotations: map[string]string{"k": "ns"}},
			},
			want: "ns",
		},
		{
			name:  "annotation present but empty on pod wins over non-empty serviceaccount",
			param: annotationParam("p", "k"),
			lookup: Lookup{
				Pod:            Metadata{Annotations: map[string]string{"k": ""}},
				ServiceAccount: Metadata{Annotations: map[string]string{"k": "sa"}},
			},
			want: "",
		},
		{
			name:  "annotation ignores labels with the same key",
			param: annotationParam("p", "k"),
			lookup: Lookup{
				Pod:       Metadata{Labels: map[string]string{"k": "podlabel"}},
				Namespace: Metadata{Annotations: map[string]string{"k": "ns"}},
			},
			want: "ns",
		},
		{
			name:  "label pod wins over serviceaccount and namespace",
			param: labelParam("p", "k"),
			lookup: Lookup{
				Pod:            Metadata{Labels: map[string]string{"k": "pod"}},
				ServiceAccount: Metadata{Labels: map[string]string{"k": "sa"}},
				Namespace:      Metadata{Labels: map[string]string{"k": "ns"}},
			},
			want: "pod",
		},
		{
			name:  "label present but empty on serviceaccount wins over namespace",
			param: labelParam("p", "k"),
			lookup: Lookup{
				ServiceAccount: Metadata{Labels: map[string]string{"k": ""}},
				Namespace:      Metadata{Labels: map[string]string{"k": "ns"}},
			},
			want: "",
		},
		{
			name:   "label absent everywhere resolves to empty",
			param:  labelParam("p", "k"),
			lookup: Lookup{},
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(context.Background(), Input{
				Parameters: []v1alpha1.Parameter{tt.param},
				Lookup:     tt.lookup,
			})
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got["p"] != tt.want {
				t.Errorf("values[p] = %q, want %q", got["p"], tt.want)
			}
		})
	}
}

func TestResolveStatic(t *testing.T) {
	got, err := Resolve(context.Background(), Input{
		Parameters: []v1alpha1.Parameter{
			{Name: "a", ValueFrom: v1alpha1.ParameterSource{Static: ptr("hello")}},
			{Name: "b", ValueFrom: v1alpha1.ParameterSource{Static: ptr("")}, Default: ptr("unused")},
		},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got["a"] != "hello" {
		t.Errorf("values[a] = %q, want %q", got["a"], "hello")
	}
	if got["b"] != "" {
		t.Errorf("values[b] = %q, want empty string (static empty is a real value)", got["b"])
	}
}

func TestResolveDefaultAndRequired(t *testing.T) {
	t.Run("default used when absent", func(t *testing.T) {
		got, err := Resolve(context.Background(), Input{
			Parameters: []v1alpha1.Parameter{
				{Name: "p", ValueFrom: v1alpha1.ParameterSource{Annotation: "k"}, Default: ptr("fallback")},
			},
		})
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if got["p"] != "fallback" {
			t.Errorf("values[p] = %q, want %q", got["p"], "fallback")
		}
	})

	t.Run("default wins over required when absent", func(t *testing.T) {
		got, err := Resolve(context.Background(), Input{
			Parameters: []v1alpha1.Parameter{
				{Name: "p", ValueFrom: v1alpha1.ParameterSource{Annotation: "k"}, Default: ptr(""), Required: true},
			},
		})
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if got["p"] != "" {
			t.Errorf("values[p] = %q, want empty default", got["p"])
		}
	})

	t.Run("required missing returns MissingParameterError", func(t *testing.T) {
		_, err := Resolve(context.Background(), Input{
			Parameters: []v1alpha1.Parameter{
				{Name: "project", ValueFrom: v1alpha1.ParameterSource{Annotation: "iam.gke.io/gcp-project-number"}, Required: true},
			},
		})
		var missing *MissingParameterError
		if !errors.As(err, &missing) {
			t.Fatalf("Resolve() error = %v, want *MissingParameterError", err)
		}
		if missing.Name != "project" {
			t.Errorf("Name = %q, want %q", missing.Name, "project")
		}
		if !strings.Contains(missing.Source, "iam.gke.io/gcp-project-number") {
			t.Errorf("Source = %q, want it to mention the annotation key", missing.Source)
		}
		if !strings.Contains(missing.Error(), "project") {
			t.Errorf("Error() = %q, want it to mention the parameter name", missing.Error())
		}
	})

	t.Run("optional missing resolves to empty string and is present", func(t *testing.T) {
		got, err := Resolve(context.Background(), Input{
			Parameters: []v1alpha1.Parameter{
				{Name: "p", ValueFrom: v1alpha1.ParameterSource{Label: "k"}},
			},
		})
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		v, ok := got["p"]
		if !ok {
			t.Fatalf("values[p] missing, want present and empty")
		}
		if v != "" {
			t.Errorf("values[p] = %q, want empty", v)
		}
	})
}

func TestResolveBuiltins(t *testing.T) {
	got, err := Resolve(context.Background(), Input{
		Builtins: Builtins{
			PodNamespace:       "team-a",
			ServiceAccountName: "workload",
			ShimName:           "gcp",
			TokenDir:           "/var/run/secrets/oidcshim/gcp",
			TokenPath:          "/var/run/secrets/oidcshim/gcp/token",
		},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := Values{
		KeyPodNamespace:       "team-a",
		KeyServiceAccountName: "workload",
		KeyShimName:           "gcp",
		KeyTokenDir:           "/var/run/secrets/oidcshim/gcp",
		KeyTokenPath:          "/var/run/secrets/oidcshim/gcp/token",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("values[%s] = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("values = %v, want exactly the builtin keys", got)
	}
}

func TestResolveBuiltinKeysAlwaysPresentWhenEmpty(t *testing.T) {
	got, err := Resolve(context.Background(), Input{})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	for _, k := range []string{KeyPodNamespace, KeyServiceAccountName, KeyShimName, KeyTokenDir, KeyTokenPath} {
		if _, ok := got[k]; !ok {
			t.Errorf("values missing builtin key %q", k)
		}
	}
}

func TestResolveTemplate(t *testing.T) {
	t.Run("references earlier parameter and builtins", func(t *testing.T) {
		got, err := Resolve(context.Background(), Input{
			Builtins: Builtins{PodNamespace: "team-a"},
			Parameters: []v1alpha1.Parameter{
				{Name: "project", ValueFrom: v1alpha1.ParameterSource{Static: ptr("12345")}},
				{Name: "audience", ValueFrom: v1alpha1.ParameterSource{
					Template: "//iam.googleapis.com/projects/{{ .project }}/ns/{{ .podNamespace }}",
				}},
			},
		})
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		want := "//iam.googleapis.com/projects/12345/ns/team-a"
		if got["audience"] != want {
			t.Errorf("values[audience] = %q, want %q", got["audience"], want)
		}
	})

	t.Run("unknown key errors", func(t *testing.T) {
		_, err := Resolve(context.Background(), Input{
			Parameters: []v1alpha1.Parameter{
				{Name: "audience", ValueFrom: v1alpha1.ParameterSource{Template: "{{ .nope }}"}},
			},
		})
		if err == nil {
			t.Fatal("Resolve() error = nil, want an error for an unknown template key")
		}
		if !strings.Contains(err.Error(), "audience") {
			t.Errorf("error = %q, want it to mention the parameter name", err)
		}
	})

	t.Run("forward reference to a later parameter errors", func(t *testing.T) {
		_, err := Resolve(context.Background(), Input{
			Parameters: []v1alpha1.Parameter{
				{Name: "a", ValueFrom: v1alpha1.ParameterSource{Template: "{{ .b }}"}},
				{Name: "b", ValueFrom: v1alpha1.ParameterSource{Static: ptr("late")}},
			},
		})
		if err == nil {
			t.Fatal("Resolve() error = nil, want an error for a forward reference")
		}
	})

	t.Run("empty template result honours default", func(t *testing.T) {
		got, err := Resolve(context.Background(), Input{
			Parameters: []v1alpha1.Parameter{
				{Name: "p", ValueFrom: v1alpha1.ParameterSource{Template: "{{ if false }}x{{ end }}"}, Default: ptr("fallback")},
			},
		})
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if got["p"] != "fallback" {
			t.Errorf("values[p] = %q, want %q", got["p"], "fallback")
		}
	})
}

func TestResolveConfigMapKeyRef(t *testing.T) {
	data := map[string]map[string]string{
		"settings": {"project": "12345", "blank": ""},
	}
	getter := func(_ context.Context, namespace, name string) (map[string]string, error) {
		if namespace != "shim-config" {
			return nil, fmt.Errorf("unexpected namespace %q", namespace)
		}
		return data[name], nil
	}
	param := func(name, key string) []v1alpha1.Parameter {
		return []v1alpha1.Parameter{{
			Name:      "p",
			ValueFrom: v1alpha1.ParameterSource{ConfigMapKeyRef: &v1alpha1.ConfigMapKeySelector{Name: name, Key: key}},
		}}
	}

	t.Run("key found", func(t *testing.T) {
		got, err := Resolve(context.Background(), Input{
			Parameters:         param("settings", "project"),
			ConfigMapNamespace: "shim-config",
			GetConfigMap:       getter,
		})
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if got["p"] != "12345" {
			t.Errorf("values[p] = %q, want %q", got["p"], "12345")
		}
	})

	t.Run("key present but empty wins over default", func(t *testing.T) {
		in := Input{
			Parameters:         param("settings", "blank"),
			ConfigMapNamespace: "shim-config",
			GetConfigMap:       getter,
		}
		in.Parameters[0].Default = ptr("fallback")
		got, err := Resolve(context.Background(), in)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if got["p"] != "" {
			t.Errorf("values[p] = %q, want empty", got["p"])
		}
	})

	t.Run("configmap absent uses default", func(t *testing.T) {
		in := Input{
			Parameters:         param("missing", "project"),
			ConfigMapNamespace: "shim-config",
			GetConfigMap:       getter,
		}
		in.Parameters[0].Default = ptr("fallback")
		got, err := Resolve(context.Background(), in)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if got["p"] != "fallback" {
			t.Errorf("values[p] = %q, want %q", got["p"], "fallback")
		}
	})

	t.Run("key absent and required returns MissingParameterError", func(t *testing.T) {
		in := Input{
			Parameters:         param("settings", "nope"),
			ConfigMapNamespace: "shim-config",
			GetConfigMap:       getter,
		}
		in.Parameters[0].Required = true
		_, err := Resolve(context.Background(), in)
		var missing *MissingParameterError
		if !errors.As(err, &missing) {
			t.Fatalf("Resolve() error = %v, want *MissingParameterError", err)
		}
		if !strings.Contains(missing.Source, "settings") || !strings.Contains(missing.Source, "nope") {
			t.Errorf("Source = %q, want it to mention the configmap and key", missing.Source)
		}
	})

	t.Run("nil getter errors", func(t *testing.T) {
		_, err := Resolve(context.Background(), Input{
			Parameters:         param("settings", "project"),
			ConfigMapNamespace: "shim-config",
		})
		if err == nil {
			t.Fatal("Resolve() error = nil, want an error for a nil ConfigMap getter")
		}
		var missing *MissingParameterError
		if errors.As(err, &missing) {
			t.Fatalf("Resolve() error = %v, want a plain error, not *MissingParameterError", err)
		}
	})

	t.Run("getter error propagates", func(t *testing.T) {
		sentinel := errors.New("boom")
		_, err := Resolve(context.Background(), Input{
			Parameters:         param("settings", "project"),
			ConfigMapNamespace: "shim-config",
			GetConfigMap: func(context.Context, string, string) (map[string]string, error) {
				return nil, sentinel
			},
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("Resolve() error = %v, want it to wrap %v", err, sentinel)
		}
	})
}

func TestResolveOrderIsListOrder(t *testing.T) {
	got, err := Resolve(context.Background(), Input{
		Parameters: []v1alpha1.Parameter{
			{Name: "a", ValueFrom: v1alpha1.ParameterSource{Static: ptr("1")}},
			{Name: "b", ValueFrom: v1alpha1.ParameterSource{Template: "{{ .a }}2"}},
			{Name: "c", ValueFrom: v1alpha1.ParameterSource{Template: "{{ .b }}3"}},
		},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got["c"] != "123" {
		t.Errorf("values[c] = %q, want %q", got["c"], "123")
	}
}
