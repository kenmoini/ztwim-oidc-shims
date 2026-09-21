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

// Package selection decides whether a pod being admitted is matched by a shim,
// either through opt-in enrollment or through label selectors, and which
// namespaces the webhook must never touch.
package selection

import (
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
)

// Target is the pod being admitted plus the objects it belongs to. ServiceAccount and
// Namespace may be nil (not found); Pod is never nil.
type Target struct {
	Pod            *corev1.Pod
	ServiceAccount *corev1.ServiceAccount
	Namespace      *corev1.Namespace
}

// ParseEnrollment splits a comma-separated enrollment value into trimmed, non-empty names.
func ParseEnrollment(value string) []string {
	var names []string
	for _, part := range strings.Split(value, ",") {
		if name := strings.TrimSpace(part); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// Enrolled reports whether any consulted object opts into shimName through the
// v1alpha1.EnrollmentKey annotation or label. A nil enrollment consults all three kinds;
// each kind's *bool nil means true.
func Enrolled(shimName string, enrollment *v1alpha1.EnrollmentSpec, t Target) bool {
	var namespaces, serviceAccounts, pods *bool
	if enrollment != nil {
		namespaces, serviceAccounts, pods = enrollment.Namespaces, enrollment.ServiceAccounts, enrollment.Pods
	}

	if kindEnabled(namespaces) && t.Namespace != nil &&
		enrolls(shimName, t.Namespace.Annotations, t.Namespace.Labels) {
		return true
	}
	if kindEnabled(serviceAccounts) && t.ServiceAccount != nil &&
		enrolls(shimName, t.ServiceAccount.Annotations, t.ServiceAccount.Labels) {
		return true
	}
	if kindEnabled(pods) && t.Pod != nil &&
		enrolls(shimName, t.Pod.Annotations, t.Pod.Labels) {
		return true
	}
	return false
}

// kindEnabled reports whether an enrollment kind toggle is on; nil means on.
func kindEnabled(toggle *bool) bool { return toggle == nil || *toggle }

// enrolls reports whether the enrollment key in either map lists shimName.
func enrolls(shimName string, annotations, objectLabels map[string]string) bool {
	for _, m := range []map[string]string{annotations, objectLabels} {
		for _, name := range ParseEnrollment(m[v1alpha1.EnrollmentKey]) {
			if name == shimName {
				return true
			}
		}
	}
	return false
}

// Matches reports whether t is selected by sel for shimName:
//
//	enrolled(t) || (at least one selector set && every set selector matches its object).
//
// A set selector whose object is nil (SA/Namespace not found) does not match.
// An invalid selector returns an error.
func Matches(shimName string, sel v1alpha1.SelectionSpec, t Target) (bool, error) {
	compiled, err := compile(sel)
	if err != nil {
		return false, err
	}

	if Enrolled(shimName, sel.Enrollment, t) {
		return true, nil
	}

	matched := false
	for _, s := range compiled {
		objectLabels, found := s.target(t)
		if !found {
			// The object this selector applies to does not exist, so it cannot match.
			return false, nil
		}
		if !s.selector.Matches(labels.Set(objectLabels)) {
			return false, nil
		}
		matched = true
	}
	return matched, nil
}

// Validate checks that every set selector converts to a labels.Selector.
// It returns a single error joining every problem (errors.Join).
func Validate(sel v1alpha1.SelectionSpec) error {
	_, err := compile(sel)
	return err
}

// OptedOut reports whether the pod carries v1alpha1.InjectAnnotation with value "false"
// (case-insensitive, trimmed).
func OptedOut(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	value, ok := pod.Annotations[v1alpha1.InjectAnnotation]
	return ok && strings.EqualFold(strings.TrimSpace(value), "false")
}

// compiledSelector is a set selector of a SelectionSpec, converted to a labels.Selector
// and paired with the object it is matched against.
type compiledSelector struct {
	selector labels.Selector
	// target returns the labels of the object the selector applies to and whether
	// that object exists.
	target func(Target) (map[string]string, bool)
}

// compile converts every set selector of sel, joining the errors of all that fail.
func compile(sel v1alpha1.SelectionSpec) ([]compiledSelector, error) {
	sources := []struct {
		field    string
		selector *metav1.LabelSelector
		target   func(Target) (map[string]string, bool)
	}{
		{field: "namespaceSelector", selector: sel.NamespaceSelector, target: namespaceLabels},
		{field: "serviceAccountSelector", selector: sel.ServiceAccountSelector, target: serviceAccountLabels},
		{field: "podSelector", selector: sel.PodSelector, target: podLabels},
	}

	var (
		compiled []compiledSelector
		errs     []error
	)
	for _, s := range sources {
		if s.selector == nil {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(s.selector)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.field, err))
			continue
		}
		compiled = append(compiled, compiledSelector{selector: selector, target: s.target})
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return compiled, nil
}

func namespaceLabels(t Target) (map[string]string, bool) {
	if t.Namespace == nil {
		return nil, false
	}
	return t.Namespace.Labels, true
}

func serviceAccountLabels(t Target) (map[string]string, bool) {
	if t.ServiceAccount == nil {
		return nil, false
	}
	return t.ServiceAccount.Labels, true
}

func podLabels(t Target) (map[string]string, bool) {
	if t.Pod == nil {
		return nil, false
	}
	return t.Pod.Labels, true
}
