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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	oidcshimv1alpha1 "github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
)

var _ = Describe("OIDCShim Controller", func() {
	ctx := context.Background()

	var reconciler *OIDCShimReconciler

	BeforeEach(func() {
		reconciler = &OIDCShimReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			Reader: k8sClient,
		}
	})

	// createShim creates an OIDCShim named name and schedules its removal.
	createShim := func(name string, spec oidcshimv1alpha1.OIDCShimSpec) *oidcshimv1alpha1.OIDCShim {
		shim := &oidcshimv1alpha1.OIDCShim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
			Spec:       spec,
		}
		Expect(k8sClient.Create(ctx, shim)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Delete(ctx, shim)).To(Succeed())
		})
		return shim
	}

	// reconcileShimNamed reconciles name and returns the refreshed object.
	reconcileShimNamed := func(name string) (ctrl.Result, *oidcshimv1alpha1.OIDCShim) {
		key := types.NamespacedName{Name: name, Namespace: testNamespace}
		result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		refreshed := &oidcshimv1alpha1.OIDCShim{}
		Expect(k8sClient.Get(ctx, key, refreshed)).To(Succeed())
		return result, refreshed
	}

	It("reports a valid spec as Ready with no matched pods", func() {
		shim := createShim("valid-shim", validShimSpec())

		result, refreshed := reconcileShimNamed(shim.Name)
		Expect(result.RequeueAfter).To(Equal(requeueInterval))

		condition := readyCondition(refreshed.Status)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		Expect(condition.Reason).To(Equal(oidcshimv1alpha1.ReasonValid))
		Expect(condition.Message).To(BeEmpty())
		Expect(condition.ObservedGeneration).To(Equal(refreshed.Generation))
		Expect(refreshed.Status.ObservedGeneration).To(Equal(refreshed.Generation))
		Expect(refreshed.Status.MatchedPods).To(HaveValue(Equal(int32(0))))
	})

	It("reports a parameter referencing a later parameter as invalid", func() {
		spec := validShimSpec()
		spec.Parameters = forwardReferenceParameters()
		spec.Audience = "static-audience"
		spec.Inject = oidcshimv1alpha1.InjectSpec{}
		shim := createShim("forward-reference-shim", spec)

		result, refreshed := reconcileShimNamed(shim.Name)
		Expect(result.RequeueAfter).To(Equal(requeueInterval))

		condition := readyCondition(refreshed.Status)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal(oidcshimv1alpha1.ReasonInvalidSpec))
		Expect(condition.Message).To(ContainSubstring("second"))
		Expect(refreshed.Status.ObservedGeneration).To(Equal(refreshed.Generation))
	})

	It("reports an unknown template key in inject.env as invalid", func() {
		spec := validShimSpec()
		spec.Inject.Env = []oidcshimv1alpha1.EnvVar{{Name: envAudience, Value: unknownKeyTemplate}}
		shim := createShim("unknown-env-key-shim", spec)

		result, refreshed := reconcileShimNamed(shim.Name)
		Expect(result.RequeueAfter).To(Equal(requeueInterval))

		condition := readyCondition(refreshed.Status)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal(oidcshimv1alpha1.ReasonInvalidSpec))
		Expect(condition.Message).To(ContainSubstring("nope"))
	})

	It("counts pods labelled for the shim in its own namespace", func() {
		shim := createShim("counting-shim", validShimSpec())
		createShimPod(ctx, "counted-pod", testNamespace,
			oidcshimv1alpha1.ShimLabelKey(shim.Name), oidcshimv1alpha1.ShimLabelValueNamespaced)

		_, refreshed := reconcileShimNamed(shim.Name)

		Expect(refreshed.Status.MatchedPods).To(HaveValue(Equal(int32(1))))
	})

	It("does not write status again when nothing changed", func() {
		counting := newCountingClient(k8sClient)
		reconciler.Client = counting

		shim := createShim("unchanged-shim", validShimSpec())

		_, first := reconcileShimNamed(shim.Name)
		Expect(counting.status.updates).To(Equal(1))

		_, second := reconcileShimNamed(shim.Name)
		Expect(counting.status.updates).To(Equal(1))
		Expect(second.ResourceVersion).To(Equal(first.ResourceVersion))
	})

	It("returns a pod listing error but still records the validation result", func() {
		counting := newCountingClient(k8sClient)
		shim := createShim("list-error-shim", validShimSpec())

		_, err := reconcileShim(ctx, counting, failingReader{}, shim)
		Expect(err).To(MatchError(ContainSubstring("list boom")))
		Expect(counting.status.updates).To(Equal(1))

		key := types.NamespacedName{Name: shim.Name, Namespace: testNamespace}
		refreshed := &oidcshimv1alpha1.OIDCShim{}
		Expect(k8sClient.Get(ctx, key, refreshed)).To(Succeed())

		condition := readyCondition(refreshed.Status)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		Expect(refreshed.Status.ObservedGeneration).To(Equal(refreshed.Generation))
		// The count failed, so no pod number is claimed.
		Expect(refreshed.Status.MatchedPods).To(BeNil())
	})

	It("keeps the previous matchedPods when a later pod count fails", func() {
		shim := createShim("stale-count-shim", validShimSpec())
		createShimPod(ctx, "stale-count-pod", testNamespace,
			oidcshimv1alpha1.ShimLabelKey(shim.Name), oidcshimv1alpha1.ShimLabelValueNamespaced)

		_, refreshed := reconcileShimNamed(shim.Name)
		Expect(refreshed.Status.MatchedPods).To(HaveValue(Equal(int32(1))))

		_, err := reconcileShim(ctx, k8sClient, failingReader{}, refreshed)
		Expect(err).To(HaveOccurred())
		Expect(refreshed.Status.MatchedPods).To(HaveValue(Equal(int32(1))))
	})

	It("ignores a shim that no longer exists", func() {
		result, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "missing-shim", Namespace: testNamespace},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))
	})
})
