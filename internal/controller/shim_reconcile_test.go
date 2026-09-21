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

package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	oidcshimv1alpha1 "github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
)

// fieldAudience is the field name validateShim reports problems with spec.audience under.
const fieldAudience = "spec.audience"

// TestValidateShim covers every check group of validateShim: each invalid spec must be
// rejected with a message naming the offending field and, where there is one, the
// offending key or value.
func TestValidateShim(t *testing.T) {
	tests := []struct {
		name string
		// mutate turns the valid spec into the spec under test.
		mutate func(spec *oidcshimv1alpha1.OIDCShimSpec)
		// want are substrings the joined error must contain; empty means "no error".
		want []string
	}{
		{
			name:   "valid spec",
			mutate: func(*oidcshimv1alpha1.OIDCShimSpec) {},
		},
		{
			name: "parameter referencing a later parameter",
			mutate: func(spec *oidcshimv1alpha1.OIDCShimSpec) {
				spec.Parameters = forwardReferenceParameters()
				spec.Audience = "static"
				spec.Inject = oidcshimv1alpha1.InjectSpec{}
			},
			want: []string{`parameter "first"`, `"second"`},
		},
		{
			name: "invalid pod selector operator",
			mutate: func(spec *oidcshimv1alpha1.OIDCShimSpec) {
				spec.Selection.PodSelector = &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: selectorLabelKey, Operator: "Nonsense", Values: []string{"demo"}},
					},
				}
			},
			want: []string{"podSelector", "Nonsense"},
		},
		{
			name: "unknown key in the audience",
			mutate: func(spec *oidcshimv1alpha1.OIDCShimSpec) {
				spec.Audience = "{{ .missing }}"
			},
			want: []string{fieldAudience, `"missing"`},
		},
		{
			name: "unparsable audience template",
			mutate: func(spec *oidcshimv1alpha1.OIDCShimSpec) {
				spec.Audience = "{{ .project"
			},
			want: []string{fieldAudience, "invalid template"},
		},
		{
			name: "unknown key in an extra audience",
			mutate: func(spec *oidcshimv1alpha1.OIDCShimSpec) {
				spec.ExtraAudiences = []string{"{{ .pool }}", "{{ .absent }}"}
			},
			want: []string{"spec.extraAudiences[1]", `"absent"`},
		},
		{
			name: "unknown key in an injected env value",
			mutate: func(spec *oidcshimv1alpha1.OIDCShimSpec) {
				spec.Inject.Env = []oidcshimv1alpha1.EnvVar{{Name: envAudience, Value: unknownKeyTemplate}}
			},
			want: []string{"spec.inject.env[" + envAudience + "]", `"nope"`},
		},
		{
			name: "unknown key in an injected file content",
			mutate: func(spec *oidcshimv1alpha1.OIDCShimSpec) {
				spec.Inject.Files[0].Content = "{{ .unheard }}"
			},
			want: []string{"spec.inject.files[" + testFilePath + "]", `"unheard"`},
		},
		{
			name: "invalid injected file mode",
			mutate: func(spec *oidcshimv1alpha1.OIDCShimSpec) {
				spec.Inject.Files[0].Mode = "0999"
			},
			want: []string{"spec.inject.files[" + testFilePath + "]", `"0999"`},
		},
		{
			name: "duplicate injected file paths",
			mutate: func(spec *oidcshimv1alpha1.OIDCShimSpec) {
				spec.Inject.Files = append(spec.Inject.Files, oidcshimv1alpha1.FileSpec{
					Path:    testFilePath,
					Content: "second",
				})
			},
			want: []string{"spec.inject.files[" + testFilePath + "]", "duplicate path"},
		},
		{
			name: "invalid token file mode",
			mutate: func(spec *oidcshimv1alpha1.OIDCShimSpec) {
				spec.Token.FileMode = "8888"
			},
			want: []string{"spec.token", `"8888"`},
		},
		{
			name: "invalid helper image pull policy",
			mutate: func(spec *oidcshimv1alpha1.OIDCShimSpec) {
				spec.Helper = &oidcshimv1alpha1.HelperSpec{ImagePullPolicy: corev1.PullPolicy("Sometimes")}
			},
			want: []string{"spec.helper.imagePullPolicy", `"Sometimes"`},
		},
		{
			name: "accepted helper image pull policy",
			mutate: func(spec *oidcshimv1alpha1.OIDCShimSpec) {
				spec.Helper = &oidcshimv1alpha1.HelperSpec{ImagePullPolicy: corev1.PullIfNotPresent}
			},
		},
		{
			name: "every problem is reported at once",
			mutate: func(spec *oidcshimv1alpha1.OIDCShimSpec) {
				spec.Parameters = append(spec.Parameters, oidcshimv1alpha1.Parameter{
					Name:      "project",
					ValueFrom: oidcshimv1alpha1.ParameterSource{Static: ptr.To("dup")},
				})
				spec.Audience = "{{ .missing }}"
				spec.Token.FileMode = "8888"
				spec.Helper = &oidcshimv1alpha1.HelperSpec{ImagePullPolicy: corev1.PullPolicy("Sometimes")}
			},
			want: []string{"duplicate name", fieldAudience, "spec.token", "spec.helper.imagePullPolicy"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := validShimSpec()
			tc.mutate(&spec)

			err := validateShim(&spec, "demo-shim")

			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("validateShim() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateShim() = nil, want an error mentioning %q", tc.want)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("validateShim() = %q, want it to mention %q", err.Error(), want)
				}
			}
		})
	}
}
