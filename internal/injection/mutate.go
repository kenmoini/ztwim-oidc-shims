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
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
)

// Names and paths used by the injected artifacts.
const (
	socketVolumeName = "spiffe-workload-api"
	configMountPath  = "/etc/oidcshim"
	helperConfFile   = "helper.conf"
	helperConfPath   = configMountPath + "/" + helperConfFile
	filesSubPathDir  = "files"
)

func configVolumeName(shim string) string     { return "oidcshim-config-" + shim }
func initContainerName(shim string) string    { return "oidcshim-init-" + shim }
func refreshContainerName(shim string) string { return "oidcshim-refresh-" + shim }

// fileAnnotation is the pod annotation carrying the rendered content of files[i] for a shim.
func fileAnnotation(shim string, i int) string {
	return v1alpha1.FileAnnotationPrefix + shim + "-" + strconv.Itoa(i)
}

// Apply mutates pod in place for each plan, in order. It returns the plans actually applied
// and human-readable warnings for anything skipped. It never returns an error: a plan that
// cannot be applied is skipped with a warning.
func Apply(pod *corev1.Pod, plans []*Plan) (applied []*Plan, warnings []string) {
	if pod == nil {
		return nil, nil
	}
	if pod.Annotations[v1alpha1.StatusAnnotation] == v1alpha1.StatusInjected {
		return nil, []string{fmt.Sprintf("pod already carries %s=%s; skipping injection",
			v1alpha1.StatusAnnotation, v1alpha1.StatusInjected)}
	}

	var newInits []corev1.Container

	for _, plan := range plans {
		targets, requested, targetWarnings := resolveTargets(pod, plan)
		warnings = append(warnings, targetWarnings...)

		if len(requested) > 0 && len(targets) == 0 {
			warnings = append(warnings, skipped(plan, fmt.Sprintf(
				"none of the requested containers exist on the pod: %s", strings.Join(requested, ", "))))
			continue
		}

		if reason := checkCollisions(pod, plan, newInits, targets); reason != "" {
			warnings = append(warnings, skipped(plan, reason))
			continue
		}

		addVolumes(pod, plan)
		newInits = append(newInits, helperContainer(plan, false), helperContainer(plan, true))
		warnings = append(warnings, injectIntoContainers(pod, plan, targets)...)
		writePlanMetadata(pod, plan)

		applied = append(applied, plan)
	}

	if len(applied) == 0 {
		return nil, warnings
	}

	pod.Spec.InitContainers = append(newInits, pod.Spec.InitContainers...)

	keys := make([]string, 0, len(applied))
	for _, plan := range applied {
		keys = append(keys, plan.ShimKey)
	}
	setAnnotation(pod, v1alpha1.StatusAnnotation, v1alpha1.StatusInjected)
	setAnnotation(pod, v1alpha1.ShimsAnnotation, strings.Join(keys, ","))

	return applied, warnings
}

func skipped(plan *Plan, reason string) string {
	return fmt.Sprintf("shim %q: skipped: %s", plan.ShimName, reason)
}

// addVolumes appends the SPIRE agent socket volume (once), the in-memory token volume and
// the downwardAPI volume materialising the rendered content from the pod annotations.
func addVolumes(pod *corev1.Pod, plan *Plan) {
	if findVolume(pod, socketVolumeName) == nil {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: socketVolumeName,
			VolumeSource: corev1.VolumeSource{
				CSI: &corev1.CSIVolumeSource{
					Driver:   plan.CSIDriver,
					ReadOnly: ptrTo(true),
				},
			},
		})
	}

	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: plan.Token.VolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory},
		},
	})

	items := []corev1.DownwardAPIVolumeFile{{
		Path:     helperConfFile,
		FieldRef: annotationFieldRef(v1alpha1.HelperConfAnnotation(plan.ShimName)),
	}}
	for i, f := range plan.Files {
		// BuildPlan validates every file mode, so this parse cannot fail for a plan it produced.
		mode, _ := parseFileMode(f.Mode)
		items = append(items, corev1.DownwardAPIVolumeFile{
			Path:     filesSubPath(i),
			FieldRef: annotationFieldRef(fileAnnotation(plan.ShimName, i)),
			Mode:     ptrTo(mode),
		})
	}

	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: configVolumeName(plan.ShimName),
		VolumeSource: corev1.VolumeSource{
			DownwardAPI: &corev1.DownwardAPIVolumeSource{Items: items},
		},
	})
}

// filesSubPath is where files[i] lands inside the downwardAPI volume.
func filesSubPath(i int) string { return filesSubPathDir + "/" + strconv.Itoa(i) }

func annotationFieldRef(key string) *corev1.ObjectFieldSelector {
	return &corev1.ObjectFieldSelector{FieldPath: fmt.Sprintf("metadata.annotations['%s']", key)}
}

// helperContainer builds the one-shot init container or the native sidecar running spiffe-helper.
func helperContainer(plan *Plan, sidecar bool) corev1.Container {
	c := corev1.Container{
		Name:            initContainerName(plan.ShimName),
		Image:           plan.HelperImage,
		ImagePullPolicy: plan.HelperImagePullPolicy,
		Args:            []string{"-config", helperConfPath, "-daemon-mode=false"},
		VolumeMounts: []corev1.VolumeMount{
			{Name: socketVolumeName, MountPath: plan.SocketMountPath, ReadOnly: true},
			{Name: plan.Token.VolumeName, MountPath: plan.Token.MountPath},
			{Name: configVolumeName(plan.ShimName), MountPath: configMountPath, ReadOnly: true},
		},
		SecurityContext: helperSecurityContext(plan),
	}
	if sidecar {
		c.Name = refreshContainerName(plan.ShimName)
		c.Args = []string{"-config", helperConfPath}
		c.RestartPolicy = ptrTo(corev1.ContainerRestartPolicyAlways)
	}
	if plan.HelperResources != nil {
		c.Resources = *plan.HelperResources.DeepCopy()
	}
	return c
}

// helperSecurityContext returns the shim's override or a locked-down default.
func helperSecurityContext(plan *Plan) *corev1.SecurityContext {
	if plan.HelperSecurityContext != nil {
		return plan.HelperSecurityContext.DeepCopy()
	}
	return &corev1.SecurityContext{
		RunAsNonRoot:             ptrTo(true),
		AllowPrivilegeEscalation: ptrTo(false),
		ReadOnlyRootFilesystem:   ptrTo(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// injectIntoContainers adds the token mount, the file mounts and the env vars to each target.
func injectIntoContainers(pod *corev1.Pod, plan *Plan, targets []string) (warnings []string) {
	for _, name := range targets {
		c := findContainer(pod.Spec.Containers, name)
		if c == nil {
			continue
		}

		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name:      plan.Token.VolumeName,
			MountPath: plan.Token.MountPath,
			ReadOnly:  true,
		})
		for i, f := range plan.Files {
			c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
				Name:      configVolumeName(plan.ShimName),
				MountPath: f.Path,
				SubPath:   filesSubPath(i),
				ReadOnly:  true,
			})
		}
		for _, e := range plan.Env {
			if hasEnvNamed(c, e.Name) {
				warnings = append(warnings, fmt.Sprintf(
					"shim %q: container %q already defines env var %q; leaving it untouched",
					plan.ShimName, c.Name, e.Name))
				continue
			}
			c.Env = append(c.Env, e)
		}
	}
	return warnings
}

// writePlanMetadata records the rendered content and the per-shim label on the pod.
func writePlanMetadata(pod *corev1.Pod, plan *Plan) {
	setAnnotation(pod, v1alpha1.HelperConfAnnotation(plan.ShimName), plan.HelperConf)
	for i, f := range plan.Files {
		setAnnotation(pod, fileAnnotation(plan.ShimName, i), f.Content)
	}

	value := v1alpha1.ShimLabelValueNamespaced
	if plan.ClusterScoped {
		value = v1alpha1.ShimLabelValueCluster
	}
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[v1alpha1.ShimLabelKey(plan.ShimName)] = value
}

func setAnnotation(pod *corev1.Pod, key, value string) {
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[key] = value
}

func ptrTo[T any](v T) *T { return &v }
