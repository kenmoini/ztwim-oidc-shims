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

// Package pod implements the mutating admission webhook that injects the
// spiffe-helper containers, volumes, files and environment variables described
// by the OIDCShim and ClusterOIDCShim resources matching a pod being created.
//
// It is the only place where the pure packages meet the API server: selection
// decides which shims match, params resolves their parameters, and injection
// renders and applies the result to the pod.
package pod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
	"github.com/kenmoini/ztwim-oidc-shims/internal/config"
	"github.com/kenmoini/ztwim-oidc-shims/internal/injection"
	"github.com/kenmoini/ztwim-oidc-shims/internal/params"
	"github.com/kenmoini/ztwim-oidc-shims/internal/selection"
)

// defaultServiceAccountName is the ServiceAccount a pod uses when it names none.
const defaultServiceAccountName = "default"

// Handler mutates pods at CREATE according to the OIDCShim/ClusterOIDCShim resources that match them.
type Handler struct {
	Reader    client.Reader // cached: Namespace, ServiceAccount, OIDCShim, ClusterOIDCShim
	APIReader client.Reader // uncached: ConfigMaps (no cluster-wide ConfigMap informer)
	Decoder   admission.Decoder
	Options   config.Options
}

var _ admission.Handler = &Handler{}

// +kubebuilder:webhook:path=/mutate-v1-pod,mutating=true,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create,versions=v1,name=pods.oidcshim.kemo.dev,admissionReviewVersions=v1,matchPolicy=Equivalent,timeoutSeconds=10,reinvocationPolicy=IfNeeded
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces;serviceaccounts;configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=oidcshim.kemo.dev,resources=oidcshims;clusteroidcshims,verbs=get;list;watch

// Handle implements admission.Handler.
func (h *Handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Create {
		return admission.Allowed("only CREATE is mutated")
	}

	pod := corev1.Pod{}
	if err := h.Decoder.Decode(req, &pod); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// During CREATE the pod name may be empty (generateName) and the object namespace
	// may not be set yet, so the request is the authoritative source of the namespace.
	namespace := req.Namespace
	if namespace == "" {
		namespace = pod.Namespace
	}

	if selection.OptedOut(&pod) {
		return admission.Allowed("opted out")
	}
	if pod.Annotations[v1alpha1.StatusAnnotation] == v1alpha1.StatusInjected {
		return admission.Allowed("already injected")
	}

	exclusions := selection.Exclusions{
		Names:    h.Options.ExcludedNamespaceNames(),
		Prefixes: h.Options.ExcludedNamespacePrefixes,
	}
	if exclusions.Excluded(namespace) {
		return admission.Allowed("namespace excluded")
	}

	ns, sa, errResp := h.readTarget(ctx, namespace, &pod)
	if errResp != nil {
		return *errResp
	}

	shims, errResp := h.listShims(ctx, namespace)
	if errResp != nil {
		return *errResp
	}

	target := selection.Target{Pod: &pod, ServiceAccount: sa, Namespace: ns}
	plans, warnings := h.buildPlans(ctx, shims, namespace, target)

	applied, applyWarnings := injection.Apply(&pod, plans)
	warnings = append(warnings, applyWarnings...)

	h.log(ctx, &pod, namespace, applied, warnings)

	if len(applied) == 0 {
		return admission.Allowed("no shim applied").WithWarnings(warnings...)
	}

	marshaled, err := json.Marshal(&pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaled).WithWarnings(warnings...)
}

// readTarget fetches the pod's Namespace and ServiceAccount. A missing object is reported
// as nil (selection and params both treat that as "absent"); any other read failure fails
// the admission request, because injecting on incomplete information would be wrong.
func (h *Handler) readTarget(ctx context.Context, namespace string, pod *corev1.Pod) (
	*corev1.Namespace, *corev1.ServiceAccount, *admission.Response) {
	ns := &corev1.Namespace{}
	if err := h.Reader.Get(ctx, types.NamespacedName{Name: namespace}, ns); err != nil {
		if !apierrors.IsNotFound(err) {
			resp := admission.Errored(http.StatusInternalServerError, fmt.Errorf("get namespace %q: %w", namespace, err))
			return nil, nil, &resp
		}
		ns = nil
	}

	saName := pod.Spec.ServiceAccountName
	if saName == "" {
		saName = defaultServiceAccountName
	}
	sa := &corev1.ServiceAccount{}
	if err := h.Reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: saName}, sa); err != nil {
		if !apierrors.IsNotFound(err) {
			resp := admission.Errored(http.StatusInternalServerError,
				fmt.Errorf("get serviceaccount %q/%q: %w", namespace, saName, err))
			return nil, nil, &resp
		}
		sa = nil
	}

	return ns, sa, nil
}

// listShims returns the OIDCShims of namespace followed by every ClusterOIDCShim,
// each group sorted by name so that injection order is deterministic.
func (h *Handler) listShims(ctx context.Context, namespace string) ([]v1alpha1.Shim, *admission.Response) {
	namespaced := &v1alpha1.OIDCShimList{}
	if err := h.Reader.List(ctx, namespaced, client.InNamespace(namespace)); err != nil {
		resp := admission.Errored(http.StatusInternalServerError, fmt.Errorf("list oidcshims in %q: %w", namespace, err))
		return nil, &resp
	}

	cluster := &v1alpha1.ClusterOIDCShimList{}
	if err := h.Reader.List(ctx, cluster); err != nil {
		resp := admission.Errored(http.StatusInternalServerError, fmt.Errorf("list clusteroidcshims: %w", err))
		return nil, &resp
	}

	sort.Slice(namespaced.Items, func(i, j int) bool { return namespaced.Items[i].Name < namespaced.Items[j].Name })
	sort.Slice(cluster.Items, func(i, j int) bool { return cluster.Items[i].Name < cluster.Items[j].Name })

	shims := make([]v1alpha1.Shim, 0, len(namespaced.Items)+len(cluster.Items))
	for i := range namespaced.Items {
		shims = append(shims, &namespaced.Items[i])
	}
	for i := range cluster.Items {
		shims = append(shims, &cluster.Items[i])
	}
	return shims, nil
}

// buildPlans renders a Plan for every shim that matches the pod. A shim that cannot be
// matched, resolved or rendered is skipped with a warning: a broken shim must never keep
// a pod from being created.
func (h *Handler) buildPlans(ctx context.Context, shims []v1alpha1.Shim, namespace string,
	target selection.Target) (plans []*injection.Plan, warnings []string) {
	log := logf.FromContext(ctx)
	lookup := lookupFor(target)
	defaults := h.defaults()

	for _, shim := range shims {
		spec := shim.ShimSpec()

		matched, err := selection.Matches(shim.GetName(), spec.Selection, target)
		if err != nil {
			warnings = append(warnings, skipped(shim, err))
			continue
		}
		if !matched {
			continue
		}

		tok := injection.ResolveToken(shim.GetName(), spec.Token)
		values, err := params.Resolve(ctx, params.Input{
			Parameters:         spec.Parameters,
			Lookup:             lookup,
			Builtins:           injection.Builtins(shim, target.Pod, namespace, tok),
			ConfigMapNamespace: shim.ConfigMapNamespaceFor(namespace),
			GetConfigMap:       h.getConfigMap,
		})
		if err != nil {
			var missing *params.MissingParameterError
			if errors.As(err, &missing) {
				// Expected for a pod that is simply not configured for this shim.
				log.V(1).Info("shim skipped: required parameter has no value",
					"shim", shim.ShimKey(), "parameter", missing.Name)
			}
			warnings = append(warnings, skipped(shim, err))
			continue
		}

		plan, err := injection.BuildPlan(shim, values, tok, defaults)
		if err != nil {
			warnings = append(warnings, skipped(shim, err))
			continue
		}
		plans = append(plans, plan)
	}

	return plans, warnings
}

// defaults converts the manager options into the per-shim injection defaults.
func (h *Handler) defaults() injection.Defaults {
	return injection.Defaults{
		HelperImage:           h.Options.SpiffeHelperImage,
		HelperImagePullPolicy: h.Options.SpiffeHelperImagePullPolicy,
		CSIDriver:             h.Options.SpiffeCSIDriver,
		SocketMountPath:       h.Options.SpiffeSocketMountPath,
		SocketFile:            h.Options.SpiffeSocketFile,
		MaxRenderedBytes:      h.Options.MaxRenderedBytes,
	}
}

// getConfigMap implements params.ConfigMapGetter. ConfigMaps are read through the uncached
// reader: the operator must not hold a cluster-wide ConfigMap informer.
func (h *Handler) getConfigMap(ctx context.Context, namespace, name string) (map[string]string, error) {
	cm := &corev1.ConfigMap{}
	if err := h.APIReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return cm.Data, nil
}

// log records one line per admitted pod at V(1).
func (h *Handler) log(ctx context.Context, pod *corev1.Pod, namespace string, applied []*injection.Plan,
	warnings []string) {
	log := logf.FromContext(ctx)
	if !log.V(1).Enabled() {
		return
	}

	name := pod.Name
	if name == "" {
		name = pod.GenerateName + "<generated>"
	}
	keys := make([]string, 0, len(applied))
	for _, plan := range applied {
		keys = append(keys, plan.ShimKey)
	}
	log.V(1).Info("pod admission handled",
		"pod", name, "namespace", namespace, "appliedShims", keys, "warnings", warnings)
}

// lookupFor builds the annotation/label lookup from the target objects. A nil object
// contributes a zero Metadata, which params treats as "absent".
func lookupFor(target selection.Target) params.Lookup {
	lookup := params.Lookup{}
	if target.Pod != nil {
		lookup.Pod = params.Metadata{Annotations: target.Pod.Annotations, Labels: target.Pod.Labels}
	}
	if target.ServiceAccount != nil {
		lookup.ServiceAccount = params.Metadata{
			Annotations: target.ServiceAccount.Annotations,
			Labels:      target.ServiceAccount.Labels,
		}
	}
	if target.Namespace != nil {
		lookup.Namespace = params.Metadata{
			Annotations: target.Namespace.Annotations,
			Labels:      target.Namespace.Labels,
		}
	}
	return lookup
}

// skipped renders the warning surfaced to the user for a shim that could not be applied.
func skipped(shim v1alpha1.Shim, err error) string {
	return fmt.Sprintf("shim %s skipped: %v", shim.ShimKey(), err)
}
