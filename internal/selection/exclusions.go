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

import "strings"

// Exclusions decides which namespaces the webhook must never touch.
type Exclusions struct {
	Names    []string // exact namespace names
	Prefixes []string // namespace name prefixes
}

// Excluded reports whether namespace is excluded by name or prefix.
func (e Exclusions) Excluded(namespace string) bool {
	for _, name := range e.Names {
		if namespace == name {
			return true
		}
	}
	for _, prefix := range e.Prefixes {
		if strings.HasPrefix(namespace, prefix) {
			return true
		}
	}
	return false
}
