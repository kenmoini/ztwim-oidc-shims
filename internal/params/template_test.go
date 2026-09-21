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
	"slices"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	values := Values{"a": "1", "b": "", KeyTokenPath: "/run/token"}

	tests := []struct {
		name    string
		tmpl    string
		want    string
		wantErr bool
	}{
		{name: "literal", tmpl: "plain", want: "plain"},
		{name: "single field", tmpl: "{{ .a }}", want: "1"},
		{name: "two fields", tmpl: "{{ .a }}-{{ .b }}", want: "1-"},
		{name: "builtin", tmpl: "file://{{ .tokenPath }}", want: "file:///run/token"},
		{name: "conditional on empty value", tmpl: "{{ if .b }}yes{{ else }}no{{ end }}", want: "no"},
		{name: "unknown key errors", tmpl: "{{ .nope }}", wantErr: true},
		{name: "unknown key in if errors", tmpl: "{{ if .nope }}x{{ end }}", wantErr: true},
		{name: "unparsable errors", tmpl: "{{ .a ", wantErr: true},
		{name: "functions are unavailable", tmpl: `{{ upper .a }}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Render("audience", tt.tmpl, values)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Render() error = nil, want an error")
				}
				if !strings.Contains(err.Error(), "audience") {
					t.Errorf("error = %q, want it to mention the template name", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Render() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderNilValues(t *testing.T) {
	got, err := Render("x", "static", nil)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got != "static" {
		t.Errorf("Render() = %q, want %q", got, "static")
	}
}

func TestReferencedKeys(t *testing.T) {
	tests := []struct {
		name    string
		tmpl    string
		want    []string
		wantErr bool
	}{
		{name: "none", tmpl: "plain", want: nil},
		{name: "single", tmpl: "{{ .foo }}", want: []string{"foo"}},
		{name: "two actions", tmpl: "{{ .a }}{{ .b }}", want: []string{"a", "b"}},
		{name: "if branch", tmpl: "{{ if .x }}{{ .y }}{{ else }}{{ .z }}{{ end }}", want: []string{"x", "y", "z"}},
		{name: "nested field takes first identifier", tmpl: "{{ .a.b.c }}", want: []string{"a"}},
		{name: "deduplicates", tmpl: "{{ .a }}-{{ .a }}", want: []string{"a"}},
		{name: "inside with", tmpl: "{{ with .a }}{{ . }}{{ end }}", want: []string{"a"}},
		{name: "inside range", tmpl: "{{ range .items }}x{{ end }}", want: []string{"items"}},
		{name: "as function argument", tmpl: `{{ if eq .a .b }}y{{ end }}`, want: []string{"a", "b"}},
		{name: "unparsable", tmpl: "{{ .a ", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReferencedKeys(tt.tmpl)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ReferencedKeys() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ReferencedKeys() error = %v", err)
			}
			slices.Sort(got)
			want := slices.Clone(tt.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("ReferencedKeys() = %v, want %v", got, tt.want)
			}
		})
	}
}
