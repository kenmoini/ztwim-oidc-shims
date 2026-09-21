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
	"errors"
	"fmt"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
)

// Validate performs static checks on a parameter list without resolving anything:
// unique names, no reserved builtin names, exactly one source set, templates parse,
// and template sources reference only builtins or parameters defined earlier in the list.
// It returns a single error that joins every problem found.
func Validate(parameters []v1alpha1.Parameter) error {
	// defined holds the keys a template may reference: the builtins plus every
	// parameter seen so far, which is what Resolve has available at that point.
	defined := make(map[string]struct{}, len(builtinKeys)+len(parameters))
	for _, k := range builtinKeys {
		defined[k] = struct{}{}
	}
	seen := make(map[string]struct{}, len(parameters))

	var problems []error
	for i := range parameters {
		p := &parameters[i]

		if _, ok := seen[p.Name]; ok {
			problems = append(problems, fmt.Errorf("parameter %q: duplicate name", p.Name))
		}
		seen[p.Name] = struct{}{}

		if isBuiltinKey(p.Name) {
			problems = append(problems, fmt.Errorf("parameter %q: name is reserved for a builtin value", p.Name))
		}

		if n := countSources(p.ValueFrom); n != 1 {
			problems = append(problems,
				fmt.Errorf("parameter %q: exactly one of static, annotation, label, configMapKeyRef or template must be set, got %d", p.Name, n))
		}

		if p.ValueFrom.Template != "" {
			keys, err := ReferencedKeys(p.ValueFrom.Template)
			if err != nil {
				problems = append(problems, fmt.Errorf("parameter %q: invalid template: %w", p.Name, err))
			}
			for _, key := range keys {
				if _, ok := defined[key]; !ok {
					problems = append(problems,
						fmt.Errorf("parameter %q: template references %q, which is not a builtin or a parameter defined before it", p.Name, key))
				}
			}
		}

		defined[p.Name] = struct{}{}
	}

	return errors.Join(problems...)
}

func isBuiltinKey(name string) bool {
	for _, k := range builtinKeys {
		if name == k {
			return true
		}
	}
	return false
}

func countSources(src v1alpha1.ParameterSource) int {
	n := 0
	for _, set := range []bool{
		src.Static != nil,
		src.Annotation != "",
		src.Label != "",
		src.ConfigMapKeyRef != nil,
		src.Template != "",
	} {
		if set {
			n++
		}
	}
	return n
}
