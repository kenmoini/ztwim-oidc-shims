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
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
	"github.com/kenmoini/ztwim-oidc-shims/internal/injection"
	"github.com/kenmoini/ztwim-oidc-shims/internal/params"
	"github.com/kenmoini/ztwim-oidc-shims/internal/selection"
	"github.com/kenmoini/ztwim-oidc-shims/internal/spiffehelper"
)

// requeueInterval is how often matchedPods is refreshed.
const requeueInterval = 5 * time.Minute

// podListPageSize bounds a single page of the matchedPods count, so a cluster-wide
// ClusterOIDCShim never asks the API server for every pod in one response.
const podListPageSize = 500

const (
	// validationAudience stands in for the templated audience while validating the
	// helper config: the real one is only known once a pod is admitted.
	validationAudience = "validate"
	// validationAgentAddress stands in for the SPIRE agent socket path, which the
	// webhook derives from the operator defaults; Render rejects an empty address.
	validationAgentAddress = "/spiffe-workload-api/spire-agent.sock"
	// defaultFileMode is the mode of an inject.files entry that leaves it unset.
	defaultFileMode = "0644"
	// maxShimNameLength is the longest metadata.name a shim may carry. The longest derived
	// name is the refresh container's, "oidcshim-refresh-<name>", and a container name is
	// limited to 63 characters: 63 - len("oidcshim-refresh-") = 46. The CRDs carry the same
	// cap as a CEL rule; this check repeats it so an object created before the rule existed
	// is still reported as InvalidSpec.
	maxShimNameLength = 46
)

// reconcileShim validates shim, reports the outcome through the Ready condition and
// observedGeneration, counts the pods currently carrying the shim's label, and writes
// status only when it changed.
//
// A failing pod count does not cost the validation result: the Ready condition is still
// written and the previous matchedPods is left in place, and the error is returned so the
// count is retried.
func reconcileShim(ctx context.Context, c client.Client, reader client.Reader, shim v1alpha1.Shim) (ctrl.Result, error) {
	status := shim.ShimStatus()
	before := status.DeepCopy()

	condition := metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             v1alpha1.ReasonValid,
		ObservedGeneration: shim.GetGeneration(),
	}
	if err := validateShim(shim.ShimSpec(), shim.GetName()); err != nil {
		condition.Status = metav1.ConditionFalse
		condition.Reason = v1alpha1.ReasonInvalidSpec
		condition.Message = err.Error()
	}
	meta.SetStatusCondition(&status.Conditions, condition)
	status.ObservedGeneration = shim.GetGeneration()

	matched, countErr := countMatchedPods(ctx, reader, shim)
	if countErr == nil {
		status.MatchedPods = ptr.To(matched)
	}

	if !apiequality.Semantic.DeepEqual(before, status) {
		if err := c.Status().Update(ctx, shim); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating status of %s: %w", shim.ShimKey(), err)
		}
	}
	if countErr != nil {
		return ctrl.Result{}, countErr
	}

	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// countMatchedPods counts the pods carrying the shim's label: cluster-wide for a
// ClusterOIDCShim, in the shim's own namespace for an OIDCShim.
//
// The list is uncached (the operator holds no pod informer), so it is paged at
// podListPageSize and the first page is served from the API server's watch cache
// (resourceVersion "0") rather than from etcd. A slightly stale count is fine: it is a
// status convenience, refreshed every requeueInterval. Continuation pages must not carry
// a resourceVersion, so it is only set on the first request.
func countMatchedPods(ctx context.Context, reader client.Reader, shim v1alpha1.Shim) (int32, error) {
	labelValue := v1alpha1.ShimLabelValueNamespaced
	if shim.IsClusterScoped() {
		labelValue = v1alpha1.ShimLabelValueCluster
	}

	base := []client.ListOption{
		client.MatchingLabels{v1alpha1.ShimLabelKey(shim.GetName()): labelValue},
		client.Limit(podListPageSize),
	}
	if !shim.IsClusterScoped() {
		base = append(base, client.InNamespace(shim.GetNamespace()))
	}

	count := 0
	continueToken := ""
	for {
		options := make([]client.ListOption, len(base), len(base)+1)
		copy(options, base)
		if continueToken == "" {
			options = append(options, &client.ListOptions{
				Raw: &metav1.ListOptions{ResourceVersion: "0"},
			})
		} else {
			options = append(options, client.Continue(continueToken))
		}

		pods := &corev1.PodList{}
		if err := reader.List(ctx, pods, options...); err != nil {
			return 0, fmt.Errorf("listing pods of %s: %w", shim.ShimKey(), err)
		}
		count += len(pods.Items)

		continueToken = pods.Continue
		if continueToken == "" {
			return int32(count), nil
		}
	}
}

// validateShim performs every static check the webhook would otherwise fail at admission
// time and returns a single error joining all problems found, or nil.
func validateShim(spec *v1alpha1.OIDCShimSpec, shimName string) error {
	return errors.Join(
		validateName(shimName),
		params.Validate(spec.Parameters),
		selection.Validate(spec.Selection),
		validateToken(shimName, spec.Token),
		validateHelper(spec.Helper),
		errors.Join(validateTemplates(spec)...),
		errors.Join(validateFiles(spec.Inject.Files, injection.ResolveToken(shimName, spec.Token).MountPath)...),
	)
}

// validateName rejects a name too long to be embedded in the derived container and
// volume names.
func validateName(shimName string) error {
	if len(shimName) > maxShimNameLength {
		return fmt.Errorf(
			"metadata.name: %q is %d characters, at most %d are allowed (it is embedded in container and volume names)",
			shimName, len(shimName), maxShimNameLength)
	}
	return nil
}

// validateTemplates checks that every templated field parses and references only the
// builtin keys or a parameter of this shim.
func validateTemplates(spec *v1alpha1.OIDCShimSpec) []error {
	known := map[string]struct{}{
		params.KeyPodNamespace:       {},
		params.KeyServiceAccountName: {},
		params.KeyShimName:           {},
		params.KeyTokenDir:           {},
		params.KeyTokenPath:          {},
	}
	for i := range spec.Parameters {
		known[spec.Parameters[i].Name] = struct{}{}
	}

	var problems []error
	check := func(field, tmpl string) {
		keys, err := params.ReferencedKeys(tmpl)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: invalid template: %w", field, err))
			return
		}
		for _, key := range keys {
			if _, ok := known[key]; !ok {
				problems = append(problems,
					fmt.Errorf("%s: template references %q, which is not a builtin or a parameter of this shim", field, key))
			}
		}
	}

	check("spec.audience", spec.Audience)
	for i, audience := range spec.ExtraAudiences {
		check(fmt.Sprintf("spec.extraAudiences[%d]", i), audience)
	}
	for i := range spec.Inject.Env {
		env := &spec.Inject.Env[i]
		check(fmt.Sprintf("spec.inject.env[%s]", env.Name), env.Value)
	}
	for i := range spec.Inject.Files {
		file := &spec.Inject.Files[i]
		check(fmt.Sprintf("spec.inject.files[%s]", file.Path), file.Content)
	}
	return problems
}

// validateToken checks the token layout by rendering the helper config the webhook
// would mount, which rejects an empty file name or an invalid file mode.
func validateToken(shimName string, token v1alpha1.TokenSpec) error {
	resolved := injection.ResolveToken(shimName, token)
	_, err := spiffehelper.Render(spiffehelper.Config{
		AgentAddress:    validationAgentAddress,
		CertDir:         resolved.MountPath,
		JWTAudience:     validationAudience,
		JWTSVIDFileName: resolved.FileName,
		JWTSVIDFileMode: resolved.FileMode,
	})
	if err != nil {
		return fmt.Errorf("spec.token: %w", err)
	}
	return nil
}

// validateFiles checks that every injected file has a valid mode, a unique path, and a
// path that is neither the token mountPath nor under it — app containers mount that
// directory read-only, so a file inside it could never be created.
//
// These are the same rules BuildPlan in internal/injection/plan.go enforces at admission
// time; the two must stay in sync. This copy exists so a shim that would be skipped for
// every pod is reported as InvalidSpec on its Ready condition instead.
func validateFiles(files []v1alpha1.FileSpec, tokenMountPath string) []error {
	var problems []error
	seen := map[string]struct{}{tokenMountPath: {}}
	for i := range files {
		file := &files[i]

		mode := file.Mode
		if mode == "" {
			mode = defaultFileMode
		}
		if !spiffehelper.ModePattern.MatchString(mode) {
			problems = append(problems,
				fmt.Errorf("spec.inject.files[%s]: mode %q is not a valid octal mode", file.Path, mode))
		}

		if _, ok := seen[file.Path]; ok {
			if file.Path == tokenMountPath {
				problems = append(problems, fmt.Errorf(
					"spec.inject.files[%s]: path collides with the token mountPath %q", file.Path, tokenMountPath))
			} else {
				problems = append(problems, fmt.Errorf("spec.inject.files[%s]: duplicate path", file.Path))
			}
		}
		if strings.HasPrefix(file.Path, tokenMountPath+"/") {
			problems = append(problems, fmt.Errorf(
				"spec.inject.files[%s]: path is under the token mountPath %q, which is mounted read-only",
				file.Path, tokenMountPath))
		}
		seen[file.Path] = struct{}{}
	}
	return problems
}

// validateHelper checks the helper image pull policy when one is set.
func validateHelper(helper *v1alpha1.HelperSpec) error {
	if helper == nil || helper.ImagePullPolicy == "" {
		return nil
	}
	switch helper.ImagePullPolicy {
	case corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever:
		return nil
	default:
		return fmt.Errorf("spec.helper.imagePullPolicy: %q must be one of Always, IfNotPresent or Never",
			helper.ImagePullPolicy)
	}
}
