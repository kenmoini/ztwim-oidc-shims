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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	oidcshimv1alpha1 "github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
)

// envAudience is the name of the injected environment variable used in test specs.
const envAudience = "AUDIENCE"

// validShimSpec is a spec exercising parameters, templated audience, env and files.
func validShimSpec() oidcshimv1alpha1.OIDCShimSpec {
	return oidcshimv1alpha1.OIDCShimSpec{
		Selection: oidcshimv1alpha1.SelectionSpec{
			PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "demo"}},
		},
		Parameters: []oidcshimv1alpha1.Parameter{
			{Name: "project", ValueFrom: oidcshimv1alpha1.ParameterSource{Static: ptr.To("demo")}},
			{Name: "pool", ValueFrom: oidcshimv1alpha1.ParameterSource{Template: "{{ .project }}-pool"}},
		},
		Audience: "//iam.googleapis.com/projects/{{ .project }}",
		Inject: oidcshimv1alpha1.InjectSpec{
			Env:   []oidcshimv1alpha1.EnvVar{{Name: envAudience, Value: "{{ .pool }}"}},
			Files: []oidcshimv1alpha1.FileSpec{{Path: "/etc/creds.json", Content: "{{ .tokenPath }}", Mode: "0600"}},
		},
	}
}

// forwardReferenceParameters has a parameter template referencing a parameter defined after it.
func forwardReferenceParameters() []oidcshimv1alpha1.Parameter {
	return []oidcshimv1alpha1.Parameter{
		{Name: "first", ValueFrom: oidcshimv1alpha1.ParameterSource{Template: "{{ .second }}"}},
		{Name: "second", ValueFrom: oidcshimv1alpha1.ParameterSource{Static: ptr.To("x")}},
	}
}

// readyCondition returns the Ready condition of a shim status, or nil when absent.
func readyCondition(status oidcshimv1alpha1.OIDCShimStatus) *metav1.Condition {
	return meta.FindStatusCondition(status.Conditions, oidcshimv1alpha1.ConditionReady)
}

// createShimPod creates a pod carrying a shim label and schedules its removal.
func createShimPod(ctx context.Context, name, namespace, labelKey, labelValue string) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{labelKey: labelValue},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "busybox"}},
		},
	}
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	DeferCleanup(func() {
		Expect(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0))).To(Succeed())
	})
}

// failingReader is a client.Reader whose List always fails.
type failingReader struct {
	client.Reader
}

func (failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("list boom")
}

// countingClient counts the status updates performed through it, so a test can prove
// that an unchanged status is not written back.
type countingClient struct {
	client.Client
	status *countingStatusWriter
}

func newCountingClient(c client.Client) *countingClient {
	return &countingClient{Client: c, status: &countingStatusWriter{SubResourceWriter: c.Status()}}
}

func (c *countingClient) Status() client.StatusWriter { return c.status }

type countingStatusWriter struct {
	client.SubResourceWriter
	updates int
}

func (w *countingStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	w.updates++
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}

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

		_, refreshed := reconcileShimNamed(shim.Name)

		condition := readyCondition(refreshed.Status)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal(oidcshimv1alpha1.ReasonInvalidSpec))
		Expect(condition.Message).To(ContainSubstring("second"))
		Expect(refreshed.Status.ObservedGeneration).To(Equal(refreshed.Generation))
	})

	It("reports an unknown template key in inject.env as invalid", func() {
		spec := validShimSpec()
		spec.Inject.Env = []oidcshimv1alpha1.EnvVar{{Name: envAudience, Value: "{{ .nope }}"}}
		shim := createShim("unknown-env-key-shim", spec)

		_, refreshed := reconcileShimNamed(shim.Name)

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

	It("returns a pod listing error without writing status", func() {
		counting := newCountingClient(k8sClient)
		shim := createShim("list-error-shim", validShimSpec())

		_, err := reconcileShim(ctx, counting, failingReader{}, shim)
		Expect(err).To(MatchError(ContainSubstring("list boom")))
		Expect(counting.status.updates).To(BeZero())
	})

	It("ignores a shim that no longer exists", func() {
		result, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "missing-shim", Namespace: testNamespace},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))
	})
})
