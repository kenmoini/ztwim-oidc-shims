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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	oidcshimv1alpha1 "github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
)

const (
	// envAudience is the name of the injected environment variable used in test specs.
	envAudience = "AUDIENCE"
	// testFilePath is the path of the injected file used in test specs.
	testFilePath = "/etc/creds.json"
	// selectorLabelKey is the pod label key selected by test specs.
	selectorLabelKey = "app"
	// unknownKeyTemplate references a key that is neither a builtin nor a parameter.
	unknownKeyTemplate = "{{ .nope }}"
)

// validShimSpec is a spec exercising parameters, templated audience, env and files.
func validShimSpec() oidcshimv1alpha1.OIDCShimSpec {
	return oidcshimv1alpha1.OIDCShimSpec{
		Selection: oidcshimv1alpha1.SelectionSpec{
			PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{selectorLabelKey: "demo"}},
		},
		Parameters: []oidcshimv1alpha1.Parameter{
			{Name: "project", ValueFrom: oidcshimv1alpha1.ParameterSource{Static: ptr.To("demo")}},
			{Name: "pool", ValueFrom: oidcshimv1alpha1.ParameterSource{Template: "{{ .project }}-pool"}},
		},
		Audience: "//iam.googleapis.com/projects/{{ .project }}",
		Inject: oidcshimv1alpha1.InjectSpec{
			Env:   []oidcshimv1alpha1.EnvVar{{Name: envAudience, Value: "{{ .pool }}"}},
			Files: []oidcshimv1alpha1.FileSpec{{Path: testFilePath, Content: "{{ .tokenPath }}", Mode: "0600"}},
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
