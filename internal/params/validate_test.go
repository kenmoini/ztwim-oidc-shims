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
	"strings"
	"testing"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name       string
		parameters []v1alpha1.Parameter
		wantErr    bool
		contains   []string
	}{
		{
			name:       "empty list is valid",
			parameters: nil,
		},
		{
			name: "every source kind is valid",
			parameters: []v1alpha1.Parameter{
				{Name: "a", ValueFrom: v1alpha1.ParameterSource{Static: ptr("")}},
				{Name: "b", ValueFrom: v1alpha1.ParameterSource{Annotation: "k"}},
				{Name: "c", ValueFrom: v1alpha1.ParameterSource{Label: "k"}},
				{Name: "d", ValueFrom: v1alpha1.ParameterSource{ConfigMapKeyRef: &v1alpha1.ConfigMapKeySelector{Name: "cm", Key: "k"}}},
				{Name: "e", ValueFrom: v1alpha1.ParameterSource{Template: "{{ .a }}{{ .b }}{{ .podNamespace }}"}},
			},
		},
		{
			name: "duplicate names",
			parameters: []v1alpha1.Parameter{
				{Name: "a", ValueFrom: v1alpha1.ParameterSource{Static: ptr("1")}},
				{Name: "a", ValueFrom: v1alpha1.ParameterSource{Static: ptr("2")}},
			},
			wantErr:  true,
			contains: []string{"duplicate", `"a"`},
		},
		{
			name: "reserved builtin name tokenPath",
			parameters: []v1alpha1.Parameter{
				{Name: KeyTokenPath, ValueFrom: v1alpha1.ParameterSource{Static: ptr("/x")}},
			},
			wantErr:  true,
			contains: []string{"reserved", "tokenPath"},
		},
		{
			name: "reserved builtin name shimName",
			parameters: []v1alpha1.Parameter{
				{Name: KeyShimName, ValueFrom: v1alpha1.ParameterSource{Static: ptr("x")}},
			},
			wantErr:  true,
			contains: []string{"reserved", "shimName"},
		},
		{
			name: "no source set",
			parameters: []v1alpha1.Parameter{
				{Name: "a"},
			},
			wantErr:  true,
			contains: []string{"exactly one"},
		},
		{
			name: "two sources set",
			parameters: []v1alpha1.Parameter{
				{Name: "a", ValueFrom: v1alpha1.ParameterSource{Static: ptr("1"), Annotation: "k"}},
			},
			wantErr:  true,
			contains: []string{"exactly one"},
		},
		{
			name: "unparsable template",
			parameters: []v1alpha1.Parameter{
				{Name: "a", ValueFrom: v1alpha1.ParameterSource{Template: "{{ .a "}},
			},
			wantErr:  true,
			contains: []string{`"a"`},
		},
		{
			name: "template with undefined function",
			parameters: []v1alpha1.Parameter{
				{Name: "a", ValueFrom: v1alpha1.ParameterSource{Template: "{{ upper 1 }}"}},
			},
			wantErr:  true,
			contains: []string{`"a"`},
		},
		{
			name: "template forward reference",
			parameters: []v1alpha1.Parameter{
				{Name: "a", ValueFrom: v1alpha1.ParameterSource{Template: "{{ .b }}"}},
				{Name: "b", ValueFrom: v1alpha1.ParameterSource{Static: ptr("2")}},
			},
			wantErr:  true,
			contains: []string{`"a"`, `"b"`},
		},
		{
			name: "template self reference",
			parameters: []v1alpha1.Parameter{
				{Name: "a", ValueFrom: v1alpha1.ParameterSource{Template: "{{ .a }}"}},
			},
			wantErr:  true,
			contains: []string{`"a"`},
		},
		{
			name: "template unknown key",
			parameters: []v1alpha1.Parameter{
				{Name: "a", ValueFrom: v1alpha1.ParameterSource{Template: "{{ .nope }}"}},
			},
			wantErr:  true,
			contains: []string{`"nope"`},
		},
		{
			name: "template referencing an earlier parameter and builtins",
			parameters: []v1alpha1.Parameter{
				{Name: "project", ValueFrom: v1alpha1.ParameterSource{Static: ptr("1")}},
				{Name: "aud", ValueFrom: v1alpha1.ParameterSource{Template: "{{ .project }}/{{ .podNamespace }}/{{ .serviceAccountName }}/{{ .shimName }}/{{ .tokenDir }}/{{ .tokenPath }}"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.parameters)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() error = nil, want an error")
			}
			for _, want := range tt.contains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Validate() error = %q, want it to contain %q", err, want)
				}
			}
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	err := Validate([]v1alpha1.Parameter{
		{Name: "a"},
		{Name: "a", ValueFrom: v1alpha1.ParameterSource{Template: "{{ .nope }}"}},
		{Name: KeyTokenDir, ValueFrom: v1alpha1.ParameterSource{Static: ptr("/x")}},
	})
	if err == nil {
		t.Fatal("Validate() error = nil, want an error")
	}
	for _, want := range []string{"exactly one", "duplicate", "nope", "reserved"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error = %q, want it to contain %q", err, want)
		}
	}
}
