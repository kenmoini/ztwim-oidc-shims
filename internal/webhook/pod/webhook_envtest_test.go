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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
)

// The shim under test is a GCP-like OIDCShim named "gcp": one required parameter read from
// an annotation, one templated parameter, one env var and one rendered file.
const (
	gcpShim  = "gcp"
	saName   = "app"
	appName  = "app"
	appImage = "busybox"

	projectNumberAnnotation = "iam.gke.io/gcp-project-number"
	projectNumber           = "123"

	tokenDir    = "/var/run/secrets/oidcshim/" + gcpShim
	keyFilePath = tokenDir + "/key.json"

	socketVolume      = "spiffe-workload-api"
	gcpTokenVolume    = "oidcshim-token-" + gcpShim
	gcpConfigVolume   = "oidcshim-config-" + gcpShim
	gcpInitContainer  = "oidcshim-init-" + gcpShim
	gcpRefreshInit    = "oidcshim-refresh-" + gcpShim
	helperConfSubPath = "helper.conf"
	fileSubPath       = "files/0"

	googleCredsEnv = "GOOGLE_APPLICATION_CREDENTIALS"
	clusterEnvName = "OIDC_TOKEN_FILE"
)

// cacheTimeout bounds how long a fixture may take to reach the manager's informers. It is
// only ever used for fixtures: every assertion about an admitted pod is a direct Get,
// because admission is synchronous with the create that triggered it.
const cacheTimeout = 20 * time.Second

var _ = Describe("Pod mutating webhook", func() {
	Context("when the namespace is enrolled into a namespaced shim", func() {
		It("injects a pod shape the API server accepts", func() {
			ns := newTestNamespace(map[string]string{
				v1alpha1.EnrollmentKey:  gcpShim,
				projectNumberAnnotation: projectNumber,
			})
			createGCPShim(ns)

			pod := createPod(ns)

			By("adding the one-shot init container and the native sidecar, in that order")
			Expect(pod.Spec.InitContainers).To(HaveLen(2))
			Expect(pod.Spec.InitContainers[0].Name).To(Equal(gcpInitContainer))
			Expect(pod.Spec.InitContainers[0].RestartPolicy).To(BeNil())
			Expect(pod.Spec.InitContainers[1].Name).To(Equal(gcpRefreshInit))
			Expect(pod.Spec.InitContainers[1].RestartPolicy).NotTo(BeNil())
			Expect(*pod.Spec.InitContainers[1].RestartPolicy).To(Equal(corev1.ContainerRestartPolicyAlways))

			By("adding the SPIRE agent socket, token and config volumes")
			socket := volumeNamed(pod, socketVolume)
			Expect(socket).NotTo(BeNil())
			Expect(socket.CSI).NotTo(BeNil())
			Expect(socket.CSI.Driver).To(Equal("csi.spiffe.io"))
			Expect(socket.CSI.ReadOnly).NotTo(BeNil())
			Expect(*socket.CSI.ReadOnly).To(BeTrue())

			token := volumeNamed(pod, gcpTokenVolume)
			Expect(token).NotTo(BeNil())
			Expect(token.EmptyDir).NotTo(BeNil())
			Expect(token.EmptyDir.Medium).To(Equal(corev1.StorageMediumMemory))

			conf := volumeNamed(pod, gcpConfigVolume)
			Expect(conf).NotTo(BeNil())
			Expect(conf.DownwardAPI).NotTo(BeNil())
			Expect(conf.DownwardAPI.Items).To(HaveLen(2))
			Expect(conf.DownwardAPI.Items[0].Path).To(Equal(helperConfSubPath))
			Expect(conf.DownwardAPI.Items[0].FieldRef).NotTo(BeNil())
			Expect(conf.DownwardAPI.Items[0].FieldRef.FieldPath).To(Equal(
				fmt.Sprintf("metadata.annotations['%s']", v1alpha1.HelperConfAnnotation(gcpShim))))
			Expect(conf.DownwardAPI.Items[1].Path).To(Equal(fileSubPath))
			Expect(conf.DownwardAPI.Items[1].FieldRef).NotTo(BeNil())
			Expect(conf.DownwardAPI.Items[1].FieldRef.FieldPath).To(Equal(
				fmt.Sprintf("metadata.annotations['%s%s-0']", v1alpha1.FileAnnotationPrefix, gcpShim)))

			By("mounting the token and the rendered file into the app container")
			app := containerNamed(pod, appName)
			Expect(app).NotTo(BeNil())

			tokenMount := mountNamed(app, gcpTokenVolume)
			Expect(tokenMount).NotTo(BeNil())
			Expect(tokenMount.MountPath).To(Equal(tokenDir))
			Expect(tokenMount.ReadOnly).To(BeTrue())

			fileMount := mountNamed(app, gcpConfigVolume)
			Expect(fileMount).NotTo(BeNil())
			Expect(fileMount.MountPath).To(Equal(keyFilePath))
			Expect(fileMount.SubPath).To(Equal(fileSubPath))
			Expect(fileMount.ReadOnly).To(BeTrue())

			Expect(app.Env).To(ContainElement(corev1.EnvVar{Name: googleCredsEnv, Value: keyFilePath}))

			By("recording the injection in the pod metadata")
			Expect(pod.Annotations).To(HaveKeyWithValue(v1alpha1.StatusAnnotation, v1alpha1.StatusInjected))
			Expect(pod.Annotations[v1alpha1.ShimsAnnotation]).To(ContainSubstring("OIDCShim/" + ns + "/" + gcpShim))
			Expect(pod.Annotations[v1alpha1.HelperConfAnnotation(gcpShim)]).To(ContainSubstring("jwt_audience"))
			Expect(pod.Labels).To(HaveKeyWithValue(v1alpha1.ShimLabelKey(gcpShim), v1alpha1.ShimLabelValueNamespaced))
		})
	})

	Context("when the pod opts out", func() {
		It("leaves the pod untouched", func() {
			ns := newTestNamespace(map[string]string{
				v1alpha1.EnrollmentKey:  gcpShim,
				projectNumberAnnotation: projectNumber,
			})
			createGCPShim(ns)

			pod := createPod(ns, withPodAnnotation(v1alpha1.InjectAnnotation, "false"))

			expectNotInjected(pod)
		})
	})

	Context("when the namespace is not enrolled and the shim sets no selectors", func() {
		It("leaves the pod untouched", func() {
			ns := newTestNamespace(map[string]string{projectNumberAnnotation: projectNumber})
			createGCPShim(ns)

			pod := createPod(ns)

			expectNotInjected(pod)
		})
	})

	Context("when a required parameter has no value", func() {
		It("still creates the pod, untouched", func() {
			// Enrolled, but nothing carries iam.gke.io/gcp-project-number, so the shim is
			// skipped with a warning. Warnings are not observable through a client, so the
			// assertion is only that the pod was created and not mutated.
			ns := newTestNamespace(map[string]string{v1alpha1.EnrollmentKey: gcpShim})
			createGCPShim(ns)

			pod := createPod(ns)

			expectNotInjected(pod)
		})
	})

	Context("when a namespaced and a cluster shim both match", func() {
		It("applies both, namespaced first", func() {
			clusterShim := "generic-" + rand.String(6)
			ns := newTestNamespace(map[string]string{
				v1alpha1.EnrollmentKey:  gcpShim + "," + clusterShim,
				projectNumberAnnotation: projectNumber,
			})
			createGCPShim(ns)
			createClusterOIDCShim(clusterShim)

			pod := createPod(ns)

			Expect(initContainerNames(pod)).To(Equal([]string{
				gcpInitContainer,
				gcpRefreshInit,
				"oidcshim-init-" + clusterShim,
				"oidcshim-refresh-" + clusterShim,
			}))

			Expect(pod.Labels).To(HaveKeyWithValue(v1alpha1.ShimLabelKey(gcpShim), v1alpha1.ShimLabelValueNamespaced))
			Expect(pod.Labels).To(HaveKeyWithValue(v1alpha1.ShimLabelKey(clusterShim), v1alpha1.ShimLabelValueCluster))

			Expect(pod.Annotations[v1alpha1.ShimsAnnotation]).To(Equal(
				"OIDCShim/" + ns + "/" + gcpShim + ",ClusterOIDCShim/" + clusterShim))

			app := containerNamed(pod, appName)
			Expect(app).NotTo(BeNil())
			Expect(app.Env).To(ContainElement(corev1.EnvVar{
				Name:  clusterEnvName,
				Value: "/var/run/secrets/oidcshim/" + clusterShim + "/token",
			}))

			By("reusing the single SPIRE agent socket volume")
			Expect(volumeNames(pod)).To(ContainElements(
				socketVolume, gcpTokenVolume, gcpConfigVolume,
				"oidcshim-token-"+clusterShim, "oidcshim-config-"+clusterShim))
		})
	})

	Context("when the pod already carries a volume the shim would add", func() {
		It("skips the shim and creates the pod unchanged", func() {
			ns := newTestNamespace(map[string]string{
				v1alpha1.EnrollmentKey:  gcpShim,
				projectNumberAnnotation: projectNumber,
			})
			createGCPShim(ns)

			pod := createPod(ns, func(p *corev1.Pod) {
				p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{
					Name:         gcpTokenVolume,
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				})
			})

			Expect(pod.Spec.InitContainers).To(BeEmpty())
			Expect(pod.Annotations).NotTo(HaveKey(v1alpha1.StatusAnnotation))
			Expect(pod.Labels).NotTo(HaveKey(v1alpha1.ShimLabelKey(gcpShim)))
			Expect(volumeNamed(pod, gcpConfigVolume)).To(BeNil())
			Expect(volumeNamed(pod, socketVolume)).To(BeNil())

			By("leaving the pre-existing volume as the pod declared it")
			existing := volumeNamed(pod, gcpTokenVolume)
			Expect(existing).NotTo(BeNil())
			Expect(existing.EmptyDir).NotTo(BeNil())
			Expect(existing.EmptyDir.Medium).To(BeEmpty())
		})
	})
})

// gcpShimSpec is the GCP-like shim used by every spec: a required project number read from
// an annotation, a templated audience, one env var and one rendered credentials file.
func gcpShimSpec() v1alpha1.OIDCShimSpec {
	return v1alpha1.OIDCShimSpec{
		Provider:  v1alpha1.ProviderGoogle,
		Selection: v1alpha1.SelectionSpec{},
		Parameters: []v1alpha1.Parameter{
			{
				Name:      "projectNumber",
				ValueFrom: v1alpha1.ParameterSource{Annotation: projectNumberAnnotation},
				Required:  true,
			},
			{
				Name: "audience",
				ValueFrom: v1alpha1.ParameterSource{
					Template: "https://iam.googleapis.com/projects/{{ .projectNumber }}" +
						"/locations/global/workloadIdentityPools/pool/providers/provider",
				},
			},
		},
		Audience: "{{ .audience }}",
		Inject: v1alpha1.InjectSpec{
			Env: []v1alpha1.EnvVar{{Name: googleCredsEnv, Value: "{{ .tokenDir }}/key.json"}},
			Files: []v1alpha1.FileSpec{{
				Path:    keyFilePath,
				Content: `{"type":"external_account","audience":"{{ .audience }}","token_file":"{{ .tokenPath }}"}`,
				Mode:    "0644",
			}},
		},
	}
}

// clusterShimSpec is a minimal cluster-scoped shim with no required parameters.
func clusterShimSpec() v1alpha1.ClusterOIDCShimSpec {
	return v1alpha1.ClusterOIDCShimSpec{
		OIDCShimSpec: v1alpha1.OIDCShimSpec{
			Provider:  v1alpha1.ProviderGeneric,
			Selection: v1alpha1.SelectionSpec{},
			Audience:  "https://example.test/aud",
			Inject: v1alpha1.InjectSpec{
				Env: []v1alpha1.EnvVar{{Name: clusterEnvName, Value: "{{ .tokenPath }}"}},
			},
		},
	}
}

// newTestNamespace creates a uniquely named Namespace carrying annotations plus the "app"
// ServiceAccount every test pod runs as, waits for both to reach the manager's informers
// and registers their deletion.
func newTestNamespace(annotations map[string]string) string {
	GinkgoHelper()

	name := "oidcshim-e2e-" + rand.String(8)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations}}
	Expect(envClient.Create(envCtx, ns)).To(Succeed())
	DeferCleanup(func() {
		Expect(client.IgnoreNotFound(envClient.Delete(envCtx, ns))).To(Succeed())
	})

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: name, Name: saName}}
	Expect(envClient.Create(envCtx, sa)).To(Succeed())

	waitCached(ns)
	waitCached(sa)
	return name
}

// createGCPShim creates the GCP-like namespaced shim in namespace and waits for the
// handler's informers to have observed it.
func createGCPShim(namespace string) {
	GinkgoHelper()

	shim := &v1alpha1.OIDCShim{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: gcpShim},
		Spec:       gcpShimSpec(),
	}
	Expect(envClient.Create(envCtx, shim)).To(Succeed())
	waitCached(shim)
}

// createClusterOIDCShim creates a cluster-scoped shim, waits for the handler to be able to
// see it and deletes it afterwards: unlike the namespaced fixtures it outlives its namespace.
func createClusterOIDCShim(name string) {
	GinkgoHelper()

	shim := &v1alpha1.ClusterOIDCShim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       clusterShimSpec(),
	}
	Expect(envClient.Create(envCtx, shim)).To(Succeed())
	DeferCleanup(func() {
		Expect(client.IgnoreNotFound(envClient.Delete(envCtx, shim))).To(Succeed())
	})
	waitCached(shim)
}

// createPod creates a pod with generateName "app-" and returns the object the API server
// persisted, i.e. the pod after every admission plugin and webhook has run.
func createPod(namespace string, opts ...func(*corev1.Pod)) *corev1.Pod {
	GinkgoHelper()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, GenerateName: "app-"},
		Spec: corev1.PodSpec{
			ServiceAccountName: saName,
			Containers:         []corev1.Container{{Name: appName, Image: appImage}},
		},
	}
	for _, o := range opts {
		o(pod)
	}
	Expect(envClient.Create(envCtx, pod)).To(Succeed())

	persisted := &corev1.Pod{}
	Expect(envClient.Get(envCtx, client.ObjectKeyFromObject(pod), persisted)).To(Succeed())
	return persisted
}

// withPodAnnotation sets an annotation on the pod before it is created.
func withPodAnnotation(key, value string) func(*corev1.Pod) {
	return func(p *corev1.Pod) {
		if p.Annotations == nil {
			p.Annotations = map[string]string{}
		}
		p.Annotations[key] = value
	}
}

// waitCached blocks until the manager's cache has observed obj. Only fixtures use it: the
// handler reads them through informers, which lag the creating request by a little.
func waitCached(obj client.Object) {
	GinkgoHelper()

	key := client.ObjectKeyFromObject(obj)
	into, ok := obj.DeepCopyObject().(client.Object)
	Expect(ok).To(BeTrue())
	Eventually(func() error {
		return envCachedReader.Get(envCtx, key, into)
	}).WithTimeout(cacheTimeout).WithPolling(50 * time.Millisecond).Should(Succeed())
}

// expectNotInjected asserts that nothing this webhook writes is present on the pod.
func expectNotInjected(pod *corev1.Pod) {
	GinkgoHelper()

	Expect(pod.Spec.InitContainers).To(BeEmpty())
	Expect(pod.Annotations).NotTo(HaveKey(v1alpha1.StatusAnnotation))
	Expect(pod.Annotations).NotTo(HaveKey(v1alpha1.ShimsAnnotation))
	Expect(pod.Annotations).NotTo(HaveKey(v1alpha1.HelperConfAnnotation(gcpShim)))
	Expect(pod.Labels).NotTo(HaveKey(v1alpha1.ShimLabelKey(gcpShim)))
	Expect(volumeNames(pod)).NotTo(ContainElement(SatisfyAny(
		Equal(socketVolume), Equal(gcpTokenVolume), Equal(gcpConfigVolume))))

	app := containerNamed(pod, appName)
	Expect(app).NotTo(BeNil())
	Expect(app.Env).To(BeEmpty())
	Expect(mountNamed(app, gcpTokenVolume)).To(BeNil())
	Expect(mountNamed(app, gcpConfigVolume)).To(BeNil())
}

func volumeNamed(pod *corev1.Pod, name string) *corev1.Volume {
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == name {
			return &pod.Spec.Volumes[i]
		}
	}
	return nil
}

func volumeNames(pod *corev1.Pod) []string {
	names := make([]string, 0, len(pod.Spec.Volumes))
	for i := range pod.Spec.Volumes {
		names = append(names, pod.Spec.Volumes[i].Name)
	}
	return names
}

func containerNamed(pod *corev1.Pod, name string) *corev1.Container {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return &pod.Spec.Containers[i]
		}
	}
	return nil
}

func initContainerNames(pod *corev1.Pod) []string {
	names := make([]string, 0, len(pod.Spec.InitContainers))
	for i := range pod.Spec.InitContainers {
		names = append(names, pod.Spec.InitContainers[i].Name)
	}
	return names
}

// mountNamed returns the first volumeMount of c backed by the named volume.
func mountNamed(c *corev1.Container, volume string) *corev1.VolumeMount {
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].Name == volume {
			return &c.VolumeMounts[i]
		}
	}
	return nil
}
