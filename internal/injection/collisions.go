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

package injection

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
)

// checkCollisions reports the first reason plan cannot be applied to pod, or "" when it can.
// pendingInits are the init containers earlier plans in the same Apply call already queued up;
// volumes added by earlier plans are already present on pod.
func checkCollisions(pod *corev1.Pod, plan *Plan, pendingInits []corev1.Container, targets []string) string {
	if reason := checkSocketVolume(pod, plan); reason != "" {
		return reason
	}

	for _, name := range []string{plan.Token.VolumeName, configVolumeName(plan.ShimName)} {
		if findVolume(pod, name) != nil {
			return fmt.Sprintf("volume %q already exists on the pod", name)
		}
	}

	for _, name := range []string{initContainerName(plan.ShimName), refreshContainerName(plan.ShimName)} {
		if hasContainerNamed(pod.Spec.InitContainers, name) || hasContainerNamed(pendingInits, name) {
			return fmt.Sprintf("init container %q already exists on the pod", name)
		}
	}

	for _, target := range targets {
		c := findContainer(pod.Spec.Containers, target)
		if c == nil {
			continue
		}
		for _, mountPath := range mountPaths(plan) {
			if hasMountPath(c, mountPath) {
				return fmt.Sprintf("container %q already mounts %q", c.Name, mountPath)
			}
		}
	}

	return ""
}

// checkSocketVolume verifies an existing spiffe-workload-api volume is the CSI volume we want.
func checkSocketVolume(pod *corev1.Pod, plan *Plan) string {
	v := findVolume(pod, socketVolumeName)
	if v == nil {
		return ""
	}
	if v.CSI == nil {
		return fmt.Sprintf("volume %q already exists but is not a CSI volume", socketVolumeName)
	}
	if v.CSI.Driver != plan.CSIDriver {
		return fmt.Sprintf("volume %q already exists with CSI driver %q, want %q",
			socketVolumeName, v.CSI.Driver, plan.CSIDriver)
	}
	return ""
}

// mountPaths are the paths plan needs free on every targeted app container.
func mountPaths(plan *Plan) []string {
	paths := make([]string, 0, 1+len(plan.Files))
	paths = append(paths, plan.Token.MountPath)
	for _, f := range plan.Files {
		paths = append(paths, f.Path)
	}
	return paths
}

// resolveTargets returns the app containers plan applies to, plus a warning per requested
// name that does not exist on the pod. The pod annotation overrides plan.Containers.
func resolveTargets(pod *corev1.Pod, plan *Plan) (targets []string, requested []string, warnings []string) {
	requested = plan.Containers
	if fromAnnotation := containersFromAnnotation(pod); fromAnnotation != nil {
		requested = fromAnnotation
	}

	if len(requested) == 0 {
		for _, c := range pod.Spec.Containers {
			targets = append(targets, c.Name)
		}
		return targets, nil, nil
	}

	for _, name := range requested {
		if findContainer(pod.Spec.Containers, name) == nil {
			warnings = append(warnings, fmt.Sprintf("shim %q: container %q not found on pod", plan.ShimName, name))
			continue
		}
		targets = append(targets, name)
	}
	return targets, requested, warnings
}

// containersFromAnnotation parses the comma-separated container filter annotation.
// It returns nil when the annotation is absent or lists no usable names.
func containersFromAnnotation(pod *corev1.Pod) []string {
	raw, ok := pod.Annotations[v1alpha1.ContainersAnnotation]
	if !ok {
		return nil
	}
	var names []string
	for _, part := range strings.Split(raw, ",") {
		if name := strings.TrimSpace(part); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func findVolume(pod *corev1.Pod, name string) *corev1.VolumeSource {
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == name {
			return &pod.Spec.Volumes[i].VolumeSource
		}
	}
	return nil
}

func findContainer(containers []corev1.Container, name string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}

func hasContainerNamed(containers []corev1.Container, name string) bool {
	return findContainer(containers, name) != nil
}

func hasMountPath(c *corev1.Container, mountPath string) bool {
	for _, m := range c.VolumeMounts {
		if m.MountPath == mountPath {
			return true
		}
	}
	return false
}

func hasEnvNamed(c *corev1.Container, name string) bool {
	for _, e := range c.Env {
		if e.Name == name {
			return true
		}
	}
	return false
}
