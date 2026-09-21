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

package selection

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
)

func ptr[T any](v T) *T { return &v }

func pod(labels, annotations map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:        "app",
		Labels:      labels,
		Annotations: annotations,
	}}
}

func serviceAccount(labels, annotations map[string]string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name:        "default",
		Labels:      labels,
		Annotations: annotations,
	}}
}

func namespace(labels, annotations map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        "team-a",
		Labels:      labels,
		Annotations: annotations,
	}}
}

func TestParseEnrollment(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{name: "empty", value: "", want: nil},
		{name: "only separators", value: " , ,,", want: nil},
		{name: "single", value: "alpha", want: []string{"alpha"}},
		{name: "trims spaces", value: "  alpha  ,\tbeta\n", want: []string{"alpha", "beta"}},
		{name: "drops empty entries", value: "alpha,,beta,", want: []string{"alpha", "beta"}},
		{name: "keeps duplicates", value: "alpha, alpha", want: []string{"alpha", "alpha"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseEnrollment(tc.value)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseEnrollment(%q) = %#v, want %#v", tc.value, got, tc.want)
			}
		})
	}
}

func TestEnrolled(t *testing.T) {
	key := v1alpha1.EnrollmentKey
	tests := []struct {
		name       string
		shim       string
		enrollment *v1alpha1.EnrollmentSpec
		target     Target
		want       bool
	}{
		{
			name:   "annotation on namespace only",
			shim:   "alpha",
			target: Target{Pod: pod(nil, nil), Namespace: namespace(nil, map[string]string{key: "alpha"})},
			want:   true,
		},
		{
			name:   "label on service account",
			shim:   "alpha",
			target: Target{Pod: pod(nil, nil), ServiceAccount: serviceAccount(map[string]string{key: "alpha"}, nil)},
			want:   true,
		},
		{
			name:   "annotation on pod",
			shim:   "alpha",
			target: Target{Pod: pod(nil, map[string]string{key: "alpha"})},
			want:   true,
		},
		{
			name:   "label on pod",
			shim:   "alpha",
			target: Target{Pod: pod(map[string]string{key: "alpha"}, nil)},
			want:   true,
		},
		{
			name:   "comma list containing the name",
			shim:   "beta",
			target: Target{Pod: pod(nil, map[string]string{key: "alpha, beta ,gamma"})},
			want:   true,
		},
		{
			name:   "different name is not enrolled",
			shim:   "alpha",
			target: Target{Pod: pod(nil, map[string]string{key: "alphabet"})},
			want:   false,
		},
		{
			name:       "pods disabled ignores the pod key",
			shim:       "alpha",
			enrollment: &v1alpha1.EnrollmentSpec{Pods: ptr(false)},
			target:     Target{Pod: pod(map[string]string{key: "alpha"}, map[string]string{key: "alpha"})},
			want:       false,
		},
		{
			name:       "pods disabled still honours the namespace key",
			shim:       "alpha",
			enrollment: &v1alpha1.EnrollmentSpec{Pods: ptr(false)},
			target: Target{
				Pod:       pod(map[string]string{key: "alpha"}, nil),
				Namespace: namespace(map[string]string{key: "alpha"}, nil),
			},
			want: true,
		},
		{
			name:       "namespaces disabled ignores the namespace key",
			shim:       "alpha",
			enrollment: &v1alpha1.EnrollmentSpec{Namespaces: ptr(false)},
			target:     Target{Pod: pod(nil, nil), Namespace: namespace(map[string]string{key: "alpha"}, nil)},
			want:       false,
		},
		{
			name:       "service accounts disabled ignores the sa key",
			shim:       "alpha",
			enrollment: &v1alpha1.EnrollmentSpec{ServiceAccounts: ptr(false)},
			target:     Target{Pod: pod(nil, nil), ServiceAccount: serviceAccount(map[string]string{key: "alpha"}, nil)},
			want:       false,
		},
		{
			name:       "nil kind toggle means enabled",
			shim:       "alpha",
			enrollment: &v1alpha1.EnrollmentSpec{Pods: ptr(false)},
			target:     Target{Pod: pod(nil, nil), ServiceAccount: serviceAccount(nil, map[string]string{key: "alpha"})},
			want:       true,
		},
		{
			name:   "missing service account and namespace",
			shim:   "alpha",
			target: Target{Pod: pod(nil, nil)},
			want:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Enrolled(tc.shim, tc.enrollment, tc.target); got != tc.want {
				t.Errorf("Enrolled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatches(t *testing.T) {
	invalid := &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
		{Key: "team", Operator: "Nonsense", Values: []string{"a"}},
	}}
	tests := []struct {
		name    string
		sel     v1alpha1.SelectionSpec
		target  Target
		want    bool
		wantErr bool
	}{
		{
			name: "only pod selector matches",
			sel: v1alpha1.SelectionSpec{
				PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			},
			target: Target{Pod: pod(map[string]string{"app": "api"}, nil)},
			want:   true,
		},
		{
			name: "only pod selector does not match",
			sel: v1alpha1.SelectionSpec{
				PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			},
			target: Target{Pod: pod(map[string]string{"app": "web"}, nil)},
			want:   false,
		},
		{
			name: "namespace and pod selectors both match",
			sel: v1alpha1.SelectionSpec{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			},
			target: Target{
				Pod:       pod(map[string]string{"app": "api"}, nil),
				Namespace: namespace(map[string]string{"tier": "prod"}, nil),
			},
			want: true,
		},
		{
			name: "namespace and pod selectors set but one fails",
			sel: v1alpha1.SelectionSpec{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			},
			target: Target{
				Pod:       pod(map[string]string{"app": "api"}, nil),
				Namespace: namespace(map[string]string{"tier": "dev"}, nil),
			},
			want: false,
		},
		{
			name: "service account selector set but service account is nil",
			sel: v1alpha1.SelectionSpec{
				ServiceAccountSelector: &metav1.LabelSelector{},
			},
			target: Target{Pod: pod(nil, nil)},
			want:   false,
		},
		{
			name: "matching pod selector does not rescue a nil namespace",
			sel: v1alpha1.SelectionSpec{
				NamespaceSelector: &metav1.LabelSelector{},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			},
			target: Target{Pod: pod(map[string]string{"app": "api"}, nil)},
			want:   false,
		},
		{
			name: "matching pod selector does not rescue a nil service account",
			sel: v1alpha1.SelectionSpec{
				ServiceAccountSelector: &metav1.LabelSelector{},
				PodSelector:            &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			},
			target: Target{Pod: pod(map[string]string{"app": "api"}, nil)},
			want:   false,
		},
		{
			name: "namespace selector set but namespace is nil",
			sel: v1alpha1.SelectionSpec{
				NamespaceSelector: &metav1.LabelSelector{},
			},
			target: Target{Pod: pod(nil, nil)},
			want:   false,
		},
		{
			name:   "no selectors and not enrolled",
			sel:    v1alpha1.SelectionSpec{},
			target: Target{Pod: pod(map[string]string{"app": "api"}, nil)},
			want:   false,
		},
		{
			name: "empty selector selects everything",
			sel: v1alpha1.SelectionSpec{
				PodSelector: &metav1.LabelSelector{},
			},
			target: Target{Pod: pod(nil, nil)},
			want:   true,
		},
		{
			name: "enrolled but selectors fail",
			sel: v1alpha1.SelectionSpec{
				PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			},
			target: Target{Pod: pod(nil, map[string]string{v1alpha1.EnrollmentKey: "alpha"})},
			want:   true,
		},
		{
			name: "enrollment kind disabled and selectors fail",
			sel: v1alpha1.SelectionSpec{
				Enrollment:  &v1alpha1.EnrollmentSpec{Pods: ptr(false)},
				PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			},
			target: Target{Pod: pod(nil, map[string]string{v1alpha1.EnrollmentKey: "alpha"})},
			want:   false,
		},
		{
			name:    "invalid pod selector",
			sel:     v1alpha1.SelectionSpec{PodSelector: invalid},
			target:  Target{Pod: pod(nil, nil)},
			wantErr: true,
		},
		{
			name:    "invalid namespace selector",
			sel:     v1alpha1.SelectionSpec{NamespaceSelector: invalid},
			target:  Target{Pod: pod(nil, nil), Namespace: namespace(nil, nil)},
			wantErr: true,
		},
		{
			name:    "invalid service account selector",
			sel:     v1alpha1.SelectionSpec{ServiceAccountSelector: invalid},
			target:  Target{Pod: pod(nil, nil), ServiceAccount: serviceAccount(nil, nil)},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Matches("alpha", tc.sel, tc.target)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Matches() error = nil, want an error")
				}
				if got {
					t.Errorf("Matches() = true on error, want false")
				}
				return
			}
			if err != nil {
				t.Fatalf("Matches() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("Matches() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	invalid := &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
		{Key: "team", Operator: "Nonsense", Values: []string{"a"}},
	}}

	t.Run("no selectors", func(t *testing.T) {
		if err := Validate(v1alpha1.SelectionSpec{}); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})

	t.Run("valid selectors", func(t *testing.T) {
		sel := v1alpha1.SelectionSpec{
			NamespaceSelector:      &metav1.LabelSelector{},
			ServiceAccountSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"a": "b"}},
			PodSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: metav1.LabelSelectorOpExists},
			}},
		}
		if err := Validate(sel); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})

	t.Run("every invalid selector is reported", func(t *testing.T) {
		sel := v1alpha1.SelectionSpec{
			NamespaceSelector:      invalid,
			ServiceAccountSelector: invalid,
			PodSelector:            invalid,
		}
		err := Validate(sel)
		if err == nil {
			t.Fatal("Validate() = nil, want an error")
		}
		for _, field := range []string{"namespaceSelector", "serviceAccountSelector", "podSelector"} {
			if !strings.Contains(err.Error(), field) {
				t.Errorf("Validate() error %q does not mention %q", err, field)
			}
		}
	})
}

func TestOptedOut(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{name: "nil pod", pod: nil, want: false},
		{name: "no annotations", pod: pod(nil, nil), want: false},
		{name: "false", pod: pod(nil, map[string]string{v1alpha1.InjectAnnotation: "false"}), want: true},
		{name: "False", pod: pod(nil, map[string]string{v1alpha1.InjectAnnotation: "False"}), want: true},
		{name: "padded false", pod: pod(nil, map[string]string{v1alpha1.InjectAnnotation: " false "}), want: true},
		{name: "true", pod: pod(nil, map[string]string{v1alpha1.InjectAnnotation: "true"}), want: false},
		{name: "other value", pod: pod(nil, map[string]string{v1alpha1.InjectAnnotation: "no"}), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := OptedOut(tc.pod); got != tc.want {
				t.Errorf("OptedOut() = %v, want %v", got, tc.want)
			}
		})
	}
}
