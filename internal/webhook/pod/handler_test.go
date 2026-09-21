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

package pod

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
	"github.com/kenmoini/ztwim-oidc-shims/internal/config"
)

const (
	testNamespace     = "app"
	otherNamespace    = "other"
	sharedNamespace   = "shared"
	testShim          = "s"
	testClusterShim   = "c"
	testPodName       = "workload"
	testContainerName = "main"
	testAudience      = "https://example.test/aud"
	testParamName     = "project"

	// operatorNamespace stands in for the namespace the manager itself runs in
	// (config.Options.OperatorNamespace, from POD_NAMESPACE).
	operatorNamespace = "ztwim-oidc-shims-system"
	// prefixExcludedNamespace is excluded by the default "kube-" prefix.
	prefixExcludedNamespace = "kube-system"
	// podServiceAccount is the ServiceAccount newPod names.
	podServiceAccount = "workload-sa"
)

// testScheme returns a scheme with the core and operator types registered.
func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(v1alpha1.AddToScheme(s))
	return s
}

// newPod returns a minimal admittable pod, optionally adjusted by opts.
func newPod(opts ...func(*corev1.Pod)) *corev1.Pod {
	p := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPodName,
			Namespace: testNamespace,
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: podServiceAccount,
			Containers:         []corev1.Container{{Name: testContainerName, Image: "busybox"}},
		},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// withAnnotation sets a pod annotation.
func withAnnotation(key, value string) func(*corev1.Pod) {
	return func(p *corev1.Pod) {
		if p.Annotations == nil {
			p.Annotations = map[string]string{}
		}
		p.Annotations[key] = value
	}
}

// inNamespace moves the pod into namespace.
func inNamespace(namespace string) func(*corev1.Pod) {
	return func(p *corev1.Pod) { p.Namespace = namespace }
}

// withoutServiceAccount clears spec.serviceAccountName, so the pod runs as "default".
func withoutServiceAccount() func(*corev1.Pod) {
	return func(p *corev1.Pod) { p.Spec.ServiceAccountName = "" }
}

// withGenerateName turns the pod into the shape the API server sends at CREATE for a
// controller-owned pod: no name and no namespace yet, only a generateName prefix.
func withGenerateName(prefix string) func(*corev1.Pod) {
	return func(p *corev1.Pod) {
		p.Name = ""
		p.Namespace = ""
		p.GenerateName = prefix
	}
}

// namespaceObj returns a Namespace enrolling the given shim names (empty enrolls nothing).
func namespaceObj(name string, enroll ...string) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if len(enroll) > 0 {
		ns.Annotations = map[string]string{v1alpha1.EnrollmentKey: strings.Join(enroll, ",")}
	}
	return ns
}

// serviceAccountObj returns the ServiceAccount newPod names.
func serviceAccountObj(namespace string) *corev1.ServiceAccount {
	return serviceAccountNamed(namespace, podServiceAccount)
}

// serviceAccountNamed returns a ServiceAccount enrolling the given shim names.
func serviceAccountNamed(namespace, name string, enroll ...string) *corev1.ServiceAccount {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if len(enroll) > 0 {
		sa.Annotations = map[string]string{v1alpha1.EnrollmentKey: strings.Join(enroll, ",")}
	}
	return sa
}

// namespacedShim returns the OIDCShim used by the fixtures, in namespace.
func namespacedShim(namespace string) *v1alpha1.OIDCShim {
	return &v1alpha1.OIDCShim{
		ObjectMeta: metav1.ObjectMeta{Name: testShim, Namespace: namespace},
		Spec:       shimSpec(),
	}
}

// injectableFixture seeds a cluster in which a pod of namespace IS mutated: the namespace
// enrolls testShim and the shim lives in the same namespace. Short-circuit tests build on it
// so that removing the short-circuit under test makes them fail.
func injectableFixture(t *testing.T, namespace string) client.WithWatch {
	t.Helper()
	return fakeReader(t,
		namespaceObj(namespace, testShim),
		serviceAccountObj(namespace),
		namespacedShim(namespace),
	)
}

// assertInjected fails unless resp patched the pod with testShim's containers.
func assertInjected(t *testing.T, resp admission.Response) {
	t.Helper()
	if !resp.Allowed {
		t.Fatalf("expected allowed, got %+v", resp.Result)
	}
	patches := patchJSON(t, resp)
	if !strings.Contains(patches, `"path":"/spec/initContainers"`) {
		t.Fatalf("expected an add for /spec/initContainers, got %s", patches)
	}
	for _, name := range []string{"oidcshim-init-" + testShim, "oidcshim-refresh-" + testShim} {
		if !strings.Contains(patches, name) {
			t.Errorf("expected init container %q in the patch, got %s", name, patches)
		}
	}
}

// shimSpec returns a minimal valid shim spec injecting one env var.
func shimSpec(params ...v1alpha1.Parameter) v1alpha1.OIDCShimSpec {
	return v1alpha1.OIDCShimSpec{
		Selection:  v1alpha1.SelectionSpec{},
		Parameters: params,
		Audience:   testAudience,
		Inject: v1alpha1.InjectSpec{
			Env: []v1alpha1.EnvVar{{Name: "OIDC_TOKEN_FILE", Value: "{{ .tokenPath }}"}},
		},
	}
}

// newRequest builds an admission request carrying pod.
func newRequest(t *testing.T, op admissionv1.Operation, namespace string, pod *corev1.Pod) admission.Request {
	t.Helper()
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatalf("marshal pod: %v", err)
	}
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: op,
			Namespace: namespace,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

// newHandler builds a Handler over the supplied readers.
func newHandler(t *testing.T, reader, apiReader client.Reader, opts config.Options) *Handler {
	t.Helper()
	return &Handler{
		Reader:    reader,
		APIReader: apiReader,
		Decoder:   admission.NewDecoder(testScheme(t)),
		Options:   opts,
	}
}

// fakeReader builds a fake client seeded with objs.
func fakeReader(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
}

// patchJSON renders the response patch list so tests can assert on it.
func patchJSON(t *testing.T, resp admission.Response) string {
	t.Helper()
	raw, err := json.Marshal(resp.Patches)
	if err != nil {
		t.Fatalf("marshal patches: %v", err)
	}
	return string(raw)
}

func TestHandleShortCircuits(t *testing.T) {
	opts := config.Defaults()
	opts.OperatorNamespace = operatorNamespace

	tests := []struct {
		name       string
		namespace  string
		op         admissionv1.Operation
		pod        *corev1.Pod
		wantReason string
	}{
		{
			name:       "update is not mutated",
			namespace:  testNamespace,
			op:         admissionv1.Update,
			pod:        newPod(),
			wantReason: "only CREATE is mutated",
		},
		{
			name:       "pod opted out",
			namespace:  testNamespace,
			op:         admissionv1.Create,
			pod:        newPod(withAnnotation(v1alpha1.InjectAnnotation, "false")),
			wantReason: "opted out",
		},
		{
			name:       "pod already injected",
			namespace:  testNamespace,
			op:         admissionv1.Create,
			pod:        newPod(withAnnotation(v1alpha1.StatusAnnotation, v1alpha1.StatusInjected)),
			wantReason: "already injected",
		},
		{
			name:       "namespace excluded by prefix",
			namespace:  prefixExcludedNamespace,
			op:         admissionv1.Create,
			pod:        newPod(inNamespace(prefixExcludedNamespace)),
			wantReason: "namespace excluded",
		},
		{
			name:       "namespace excluded by operator namespace",
			namespace:  operatorNamespace,
			op:         admissionv1.Create,
			pod:        newPod(inNamespace(operatorNamespace)),
			wantReason: "namespace excluded",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// injectableFixture is a cluster in which this pod WOULD be mutated, so a subtest
			// only passes because its short-circuit fired - not because nothing matched. The
			// reason string pins down which short-circuit that was.
			reader := injectableFixture(t, tc.namespace)
			h := newHandler(t, reader, reader, opts)

			resp := h.Handle(context.Background(), newRequest(t, tc.op, tc.namespace, tc.pod))

			if !resp.Allowed {
				t.Fatalf("expected allowed, got %+v", resp.Result)
			}
			if len(resp.Patches) != 0 {
				t.Fatalf("expected no patches, got %s", patchJSON(t, resp))
			}
			if resp.Result == nil {
				t.Fatalf("expected a result carrying reason %q, got none", tc.wantReason)
			}
			if resp.Result.Message != tc.wantReason {
				t.Errorf("expected reason %q, got %q", tc.wantReason, resp.Result.Message)
			}
		})
	}
}

// TestHandleShortCircuitFixturesAreMutableControls guards TestHandleShortCircuits: it proves
// that every fixture it uses really is one the webhook mutates once the short-circuit no longer
// applies, so those subtests cannot pass vacuously.
func TestHandleShortCircuitFixturesAreMutableControls(t *testing.T) {
	// Without any exclusion configured, the excluded-namespace fixtures are mutated too.
	permissive := config.Defaults()
	permissive.ExcludedNamespacePrefixes = nil
	permissive.ZTWIMNamespace = ""
	permissive.OperatorNamespace = ""

	for _, namespace := range []string{testNamespace, prefixExcludedNamespace, operatorNamespace} {
		t.Run(namespace, func(t *testing.T) {
			reader := injectableFixture(t, namespace)
			h := newHandler(t, reader, reader, permissive)

			resp := h.Handle(context.Background(),
				newRequest(t, admissionv1.Create, namespace, newPod(inNamespace(namespace))))

			assertInjected(t, resp)
		})
	}
}

func TestHandleAllowsWhenNoShimExists(t *testing.T) {
	reader := fakeReader(t, namespaceObj(testNamespace), serviceAccountObj(testNamespace))
	h := newHandler(t, reader, reader, config.Defaults())

	resp := h.Handle(context.Background(), newRequest(t, admissionv1.Create, testNamespace, newPod()))

	if !resp.Allowed {
		t.Fatalf("expected allowed, got %+v", resp.Result)
	}
	if len(resp.Patches) != 0 {
		t.Fatalf("expected no patches, got %s", patchJSON(t, resp))
	}
	if resp.Result == nil || resp.Result.Message != "no shim applied" {
		t.Fatalf("expected reason %q, got %+v", "no shim applied", resp.Result)
	}
}

func TestHandleFallsBackToTheDefaultServiceAccount(t *testing.T) {
	// The enrollment lives ONLY on the ServiceAccount named "default" and the pod names no
	// ServiceAccount, so the pod is mutated only if the handler looked "default" up.
	reader := fakeReader(t,
		namespaceObj(testNamespace),
		serviceAccountNamed(testNamespace, defaultServiceAccountName, testShim),
		namespacedShim(testNamespace),
	)
	h := newHandler(t, reader, reader, config.Defaults())

	resp := h.Handle(context.Background(),
		newRequest(t, admissionv1.Create, testNamespace, newPod(withoutServiceAccount())))

	assertInjected(t, resp)
}

func TestHandleInjectsPodWithGenerateNameOnly(t *testing.T) {
	// At CREATE a controller-owned pod carries neither a name nor a namespace: the namespace
	// comes from the admission request alone.
	reader := injectableFixture(t, testNamespace)
	h := newHandler(t, reader, reader, config.Defaults())

	pod := newPod(withGenerateName("workload-"))
	if pod.Name != "" || pod.Namespace != "" {
		t.Fatalf("fixture must have neither name nor namespace, got %q/%q", pod.Namespace, pod.Name)
	}

	resp := h.Handle(context.Background(), newRequest(t, admissionv1.Create, testNamespace, pod))

	assertInjected(t, resp)
}

func TestHandleAppliesNamespacedShim(t *testing.T) {
	reader := injectableFixture(t, testNamespace)
	h := newHandler(t, reader, reader, config.Defaults())

	resp := h.Handle(context.Background(), newRequest(t, admissionv1.Create, testNamespace, newPod()))

	assertInjected(t, resp)

	patches := patchJSON(t, resp)
	if !strings.Contains(patches, v1alpha1.StatusAnnotation) || !strings.Contains(patches, v1alpha1.StatusInjected) {
		t.Errorf("expected the status annotation in the patch, got %s", patches)
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("expected no warnings, got %v", resp.Warnings)
	}
}

func TestHandleSkipsShimWithMissingRequiredParameter(t *testing.T) {
	shim := &v1alpha1.OIDCShim{
		ObjectMeta: metav1.ObjectMeta{Name: testShim, Namespace: testNamespace},
		Spec: shimSpec(v1alpha1.Parameter{
			Name:      testParamName,
			ValueFrom: v1alpha1.ParameterSource{Annotation: "example.test/project"},
			Required:  true,
		}),
	}
	reader := fakeReader(t,
		namespaceObj(testNamespace, testShim),
		serviceAccountObj(testNamespace),
		shim,
	)
	h := newHandler(t, reader, reader, config.Defaults())

	resp := h.Handle(context.Background(), newRequest(t, admissionv1.Create, testNamespace, newPod()))

	if !resp.Allowed {
		t.Fatalf("expected allowed, got %+v", resp.Result)
	}
	if len(resp.Patches) != 0 {
		t.Fatalf("expected no patches, got %s", patchJSON(t, resp))
	}
	if len(resp.Warnings) != 1 {
		t.Fatalf("expected exactly one warning, got %v", resp.Warnings)
	}
	if !strings.Contains(resp.Warnings[0], shim.ShimKey()) {
		t.Errorf("expected warning to name %q, got %q", shim.ShimKey(), resp.Warnings[0])
	}
}

func TestHandleClusterShimResolvesConfigMapFromConfigMapNamespace(t *testing.T) {
	shim := &v1alpha1.ClusterOIDCShim{
		ObjectMeta: metav1.ObjectMeta{Name: testClusterShim},
		Spec: v1alpha1.ClusterOIDCShimSpec{
			OIDCShimSpec: v1alpha1.OIDCShimSpec{
				Selection: v1alpha1.SelectionSpec{},
				Parameters: []v1alpha1.Parameter{{
					Name: testParamName,
					ValueFrom: v1alpha1.ParameterSource{
						ConfigMapKeyRef: &v1alpha1.ConfigMapKeySelector{Name: "shim-config", Key: testParamName},
					},
					Required: true,
				}},
				Audience: "https://example.test/{{ .project }}",
			},
			ConfigMapNamespace: sharedNamespace,
		},
	}
	reader := fakeReader(t,
		namespaceObj(testNamespace, testClusterShim),
		serviceAccountObj(testNamespace),
		shim,
	)
	// The ConfigMap lives only in the uncached APIReader, in the configMapNamespace.
	apiReader := fakeReader(t, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "shim-config", Namespace: sharedNamespace},
		Data:       map[string]string{testParamName: "proj-12345"},
	})
	h := newHandler(t, reader, apiReader, config.Defaults())

	resp := h.Handle(context.Background(), newRequest(t, admissionv1.Create, testNamespace, newPod()))

	if !resp.Allowed {
		t.Fatalf("expected allowed, got %+v", resp.Result)
	}
	if len(resp.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", resp.Warnings)
	}
	patches := patchJSON(t, resp)
	if !strings.Contains(patches, "proj-12345") {
		t.Errorf("expected the ConfigMap value in the rendered audience, got %s", patches)
	}
	if !strings.Contains(patches, "oidcshim-init-"+testClusterShim) {
		t.Errorf("expected the cluster shim init container in the patch, got %s", patches)
	}
}

func TestHandleIgnoresShimInAnotherNamespace(t *testing.T) {
	shim := &v1alpha1.OIDCShim{
		ObjectMeta: metav1.ObjectMeta{Name: testShim, Namespace: otherNamespace},
		Spec:       shimSpec(),
	}
	reader := fakeReader(t,
		namespaceObj(testNamespace, testShim),
		serviceAccountObj(testNamespace),
		namespaceObj(otherNamespace),
		shim,
	)
	h := newHandler(t, reader, reader, config.Defaults())

	resp := h.Handle(context.Background(), newRequest(t, admissionv1.Create, testNamespace, newPod()))

	if !resp.Allowed {
		t.Fatalf("expected allowed, got %+v", resp.Result)
	}
	if len(resp.Patches) != 0 {
		t.Fatalf("expected no patches, got %s", patchJSON(t, resp))
	}
}

func TestHandleErrorsOnAPIReadFailure(t *testing.T) {
	boom := apierrors.NewInternalError(errors.New("etcd is on fire"))
	reader := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object,
				_ ...client.GetOption) error {
				return boom
			},
		}).
		Build()
	h := newHandler(t, reader, reader, config.Defaults())

	resp := h.Handle(context.Background(), newRequest(t, admissionv1.Create, testNamespace, newPod()))

	if resp.Allowed {
		t.Fatal("expected the request to be denied")
	}
	if resp.Result == nil || resp.Result.Code != http.StatusInternalServerError {
		t.Fatalf("expected a 500 response, got %+v", resp.Result)
	}
}

func TestHandleErrorsOnUndecodablePod(t *testing.T) {
	reader := fakeReader(t)
	h := newHandler(t, reader, reader, config.Defaults())

	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: admissionv1.Create,
		Namespace: testNamespace,
		Object:    runtime.RawExtension{Raw: []byte("not json")},
	}}

	resp := h.Handle(context.Background(), req)

	if resp.Allowed {
		t.Fatal("expected the request to be denied")
	}
	if resp.Result == nil || resp.Result.Code != http.StatusBadRequest {
		t.Fatalf("expected a 400 response, got %+v", resp.Result)
	}
}
