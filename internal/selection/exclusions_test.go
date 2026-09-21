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

import "testing"

func TestExclusionsExcluded(t *testing.T) {
	e := Exclusions{
		Names:    []string{"kube-system", "default"},
		Prefixes: []string{"openshift-", "kube-"},
	}
	tests := []struct {
		name       string
		exclusions Exclusions
		namespace  string
		want       bool
	}{
		{name: "exact name", exclusions: e, namespace: "kube-system", want: true},
		{name: "other exact name", exclusions: e, namespace: "default", want: true},
		{name: "prefix", exclusions: e, namespace: "openshift-monitoring", want: true},
		{name: "prefix from another entry", exclusions: e, namespace: "kube-public", want: true},
		{name: "not excluded", exclusions: e, namespace: "team-a", want: false},
		{name: "prefix is not a suffix match", exclusions: e, namespace: "my-openshift-tools", want: false},
		{name: "name match is exact", exclusions: Exclusions{Names: []string{"default"}}, namespace: "default-app", want: false},
		{name: "empty exclusions exclude nothing", exclusions: Exclusions{}, namespace: "kube-system", want: false},
		{name: "empty exclusions and empty namespace", exclusions: Exclusions{}, namespace: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.exclusions.Excluded(tc.namespace); got != tc.want {
				t.Errorf("Excluded(%q) = %v, want %v", tc.namespace, got, tc.want)
			}
		})
	}
}
