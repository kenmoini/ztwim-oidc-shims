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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/kenmoini/ztwim-oidc-shims/test/utils"
)

// namespace where the project is deployed in
const namespace = "ztwim-oidc-shims-system"

// serviceAccountName created for the project
const serviceAccountName = "ztwim-oidc-shims-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "ztwim-oidc-shims-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "ztwim-oidc-shims-metrics-binding"

// webhookConfigName is the MutatingWebhookConfiguration deployed by config/default.
const webhookConfigName = "ztwim-oidc-shims-mutating-webhook-configuration"

// webhookServiceName is the service backing the pod mutating webhook.
const webhookServiceName = "ztwim-oidc-shims-webhook-service"

// Fixtures for the injection specs.
const (
	// shimNamespace is the throwaway namespace holding the OIDCShim and the workload pods.
	shimNamespace = "oidcshim-e2e"
	// kubeSystemNamespace is excluded by the webhook namespaceSelector, so it is the negative case.
	kubeSystemNamespace = "kube-system"
	// shimName matches metadata.name of config/samples/oidcshim_v1alpha1_oidcshim_gcp.yaml.
	shimName = "gcp"
	// clusterShimName matches metadata.name of
	// config/samples/oidcshim_v1alpha1_clusteroidcshim_azure.yaml. A cluster-scoped shim is
	// visible from every namespace, which is what makes the kube-system negative case meaningful.
	clusterShimName = "azure"

	appPodName         = "app"
	optOutPodName      = "app-opted-out"
	clusterShimPodName = "app-cluster-shim"
	negativePodName    = "app-not-enrolled"
	appContainerName   = "app"
	appImage           = "busybox:1.36"

	// Names the webhook gives to the artifacts it injects for the "gcp" shim.
	injectedPrefix       = "oidcshim-"
	initContainerName    = injectedPrefix + "init-" + shimName
	refreshContainerName = injectedPrefix + "refresh-" + shimName
	tokenVolumeName      = injectedPrefix + "token-" + shimName
	configVolumeName     = injectedPrefix + "config-" + shimName
	socketVolumeName     = "spiffe-workload-api"

	// Names the webhook gives to the artifacts it injects for the cluster-scoped "azure" shim.
	clusterInitContainerName = injectedPrefix + "init-" + clusterShimName

	// Metadata keys, mirroring api/v1alpha1/common_types.go.
	enrollmentAnnotation     = "oidcshim.kemo.dev/shim"
	injectAnnotation         = "oidcshim.kemo.dev/inject"
	statusAnnotation         = "oidcshim.kemo.dev/status"
	statusInjected           = "injected"
	shimLabel                = "oidcshim.kemo.dev/shim-" + shimName
	clusterShimLabel         = "oidcshim.kemo.dev/shim-" + clusterShimName
	shimLabelValueNamespaced = "namespaced"
	shimLabelValueCluster    = "cluster"

	// googleCredentialsEnv is the env var the GCP sample injects into app containers, and
	// googleCredentialsPath is the value it carries: the rendered external_account file,
	// which sits outside the read-only token mountPath.
	googleCredentialsEnv  = "GOOGLE_APPLICATION_CREDENTIALS"
	googleCredentialsPath = "/etc/oidcshim/" + shimName + "/key.json"
	// azureClientIDEnv is one of the env vars the Azure sample injects into app containers.
	azureClientIDEnv = "AZURE_CLIENT_ID"
)

// shimNamespaceAnnotations enrol the workload namespace into the "gcp" shim and supply the
// five iam.gke.io parameters config/samples/oidcshim_v1alpha1_oidcshim_gcp.yaml resolves.
var shimNamespaceAnnotations = []string{
	enrollmentAnnotation + "=" + shimName,
	"iam.gke.io/gcp-project-number=123456789012",
	"iam.gke.io/gcp-wid-pool=oidcshim-e2e-pool",
	"iam.gke.io/gcp-wid-provider=oidcshim-e2e-provider",
	"iam.gke.io/gcp-wid-pool-location=global",
	"iam.gke.io/gcp-service-account=oidcshim-e2e@example.iam.gserviceaccount.com",
}

// clusterShimPodAnnotations enrol a *Pod* into the cluster-scoped "azure" shim and supply the two
// azure.workload.identity parameters config/samples/oidcshim_v1alpha1_clusteroidcshim_azure.yaml
// requires. The exact same set is used for the positive control in oidcshim-e2e and for the
// kube-system negative case, so the namespace the webhook namespaceSelector inspects is the only
// difference between a pod that gets injected and one that does not.
var clusterShimPodAnnotations = map[string]string{
	enrollmentAnnotation:                clusterShimName,
	"azure.workload.identity/client-id": "00000000-0000-0000-0000-00000000c11d",
	"azure.workload.identity/tenant-id": "00000000-0000-0000-0000-0000000075e7",
}

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=ztwim-oidc-shims-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints", metricsServiceName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccount": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := getMetricsOutput()
			Expect(metricsOutput).To(ContainSubstring(
				"controller_runtime_reconcile_total",
			))
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})

	// Injection checks. No SPIRE is installed on the e2e cluster, so the injected pods never
	// become Ready (the csi.spiffe.io driver is absent and the workload API volume cannot be
	// mounted). Every assertion below is therefore on the *shape* of the admitted pod spec,
	// which is exactly what the mutating webhook is responsible for.
	Context("OIDCShim injection", Ordered, func() {
		BeforeAll(func() {
			By("creating the workload namespace")
			cmd := exec.Command("kubectl", "create", "ns", shimNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the workload namespace")

			By("enrolling the namespace into the gcp shim")
			annotateArgs := append([]string{"annotate", "--overwrite", "ns", shimNamespace},
				shimNamespaceAnnotations...)
			cmd = exec.Command("kubectl", annotateArgs...)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to annotate the workload namespace")

			By("applying the GCP OIDCShim sample")
			cmd = exec.Command("kubectl", "apply", "-n", shimNamespace,
				"-f", filepath.Join("config", "samples", "oidcshim_v1alpha1_oidcshim_gcp.yaml"))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply the GCP OIDCShim sample")

			// A cluster-scoped shim is visible from every namespace. Without it the kube-system
			// negative spec would prove nothing: the handler only lists namespaced OIDCShims from
			// the pod's own namespace, so a kube-system pod could not match the gcp shim even if
			// the webhook namespaceSelector were removed.
			By("applying the Azure ClusterOIDCShim sample")
			cmd = exec.Command("kubectl", "apply",
				"-f", filepath.Join("config", "samples", "oidcshim_v1alpha1_clusteroidcshim_azure.yaml"))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply the Azure ClusterOIDCShim sample")

			By("waiting for the mutating webhook to be served")
			Eventually(verifyWebhookServing).Should(Succeed())
		})

		AfterAll(func() {
			By("removing the Azure ClusterOIDCShim")
			cmd := exec.Command("kubectl", "delete", "clusteroidcshim", clusterShimName,
				"--ignore-not-found=true")
			_, _ = utils.Run(cmd)

			By("removing the workload namespace")
			cmd = exec.Command("kubectl", "delete", "ns", shimNamespace,
				"--ignore-not-found=true", "--wait=false")
			_, _ = utils.Run(cmd)
		})

		It("should report both shims as Ready", func() {
			verifyShimReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "oidcshim", shimName, "-n", shimNamespace,
					"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to read the OIDCShim status")
				g.Expect(output).To(Equal("True"), "OIDCShim is not Ready yet")
			}
			Eventually(verifyShimReady).Should(Succeed())

			verifyClusterShimReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "clusteroidcshim", clusterShimName,
					"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to read the ClusterOIDCShim status")
				g.Expect(output).To(Equal("True"), "ClusterOIDCShim is not Ready yet")
			}
			Eventually(verifyClusterShimReady).Should(Succeed())
		})

		It("should inject the gcp shim into an enrolled pod", func() {
			By("creating the application pod")
			_, err := applyPodManifest(podManifest(appPodName, shimNamespace, nil))
			Expect(err).NotTo(HaveOccurred(), "Failed to create the application pod")

			By("reading the admitted pod back from the API server")
			pod := getPod(appPodName, shimNamespace)

			By("verifying the spiffe-helper init container and native sidecar")
			initNames := containerNames(pod.Spec.InitContainers)
			Expect(initNames).To(ContainElements(initContainerName, refreshContainerName))

			oneShot := findContainer(pod.Spec.InitContainers, initContainerName)
			Expect(oneShot).NotTo(BeNil())
			Expect(oneShot.RestartPolicy).To(BeNil(), "the one-shot init container must not be a sidecar")

			refresh := findContainer(pod.Spec.InitContainers, refreshContainerName)
			Expect(refresh).NotTo(BeNil())
			Expect(refresh.RestartPolicy).NotTo(BeNil(), "the refresh container must be a native sidecar")
			Expect(*refresh.RestartPolicy).To(Equal(corev1.ContainerRestartPolicyAlways))

			By("verifying the injected volumes")
			Expect(volumeNames(pod.Spec.Volumes)).To(ContainElements(
				socketVolumeName, tokenVolumeName, configVolumeName))

			By("verifying the env injected into the app container")
			app := findContainer(pod.Spec.Containers, appContainerName)
			Expect(app).NotTo(BeNil())
			Expect(app.Env).To(ContainElement(corev1.EnvVar{
				Name: googleCredentialsEnv, Value: googleCredentialsPath}))

			By("verifying the rendered credential file is mounted outside the read-only token dir")
			Expect(app.VolumeMounts).To(ContainElement(SatisfyAll(
				HaveField("Name", configVolumeName),
				HaveField("MountPath", googleCredentialsPath),
				HaveField("ReadOnly", BeTrue()),
			)))

			By("verifying the metadata written by the webhook")
			Expect(pod.Annotations).To(HaveKeyWithValue(statusAnnotation, statusInjected))
			Expect(pod.Labels).To(HaveKeyWithValue(shimLabel, shimLabelValueNamespaced))
		})

		// Positive control for the negative spec below: the exact same pod-level annotations, in a
		// namespace the webhook does select, are injected from the cluster-scoped shim.
		It("should inject a cluster-scoped shim enrolled at pod level", func() {
			By("creating an azure-enrolled pod in the selected namespace")
			manifest := podManifest(clusterShimPodName, shimNamespace, clusterShimPodAnnotations)
			_, err := applyPodManifest(manifest)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the cluster-shim pod")

			By("verifying the azure shim was injected")
			pod := getPod(clusterShimPodName, shimNamespace)
			Expect(containerNames(pod.Spec.InitContainers)).To(ContainElement(clusterInitContainerName))
			Expect(pod.Annotations).To(HaveKeyWithValue(statusAnnotation, statusInjected))
			Expect(pod.Labels).To(HaveKeyWithValue(clusterShimLabel, shimLabelValueCluster))

			app := findContainer(pod.Spec.Containers, appContainerName)
			Expect(app).NotTo(BeNil())
			Expect(app.Env).To(ContainElement(HaveField("Name", azureClientIDEnv)))
		})

		It("should not inject into a pod in a namespace excluded by the webhook", func() {
			By("creating an identically annotated pod in kube-system")
			manifest := podManifest(negativePodName, kubeSystemNamespace, clusterShimPodAnnotations)
			_, err := applyPodManifest(manifest)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the pod in kube-system")
			DeferCleanup(func() {
				By("removing the kube-system pod")
				cmd := exec.Command("kubectl", "delete", "pod", negativePodName,
					"-n", kubeSystemNamespace, "--ignore-not-found=true", "--wait=false")
				_, _ = utils.Run(cmd)
			})

			By("verifying the pod was admitted untouched")
			pod := getPod(negativePodName, kubeSystemNamespace)
			Expect(containerNames(pod.Spec.InitContainers)).NotTo(ContainElement(HavePrefix(injectedPrefix)))
			Expect(volumeNames(pod.Spec.Volumes)).NotTo(ContainElement(HavePrefix(injectedPrefix)))
			Expect(volumeNames(pod.Spec.Volumes)).NotTo(ContainElement(socketVolumeName))
			Expect(pod.Annotations).NotTo(HaveKey(statusAnnotation))
			Expect(pod.Labels).NotTo(HaveKey(shimLabel))
			Expect(pod.Labels).NotTo(HaveKey(clusterShimLabel))

			app := findContainer(pod.Spec.Containers, appContainerName)
			Expect(app).NotTo(BeNil())
			Expect(app.Env).NotTo(ContainElement(HaveField("Name", azureClientIDEnv)))
		})

		It("should leave a pod that opted out of injection untouched", func() {
			By("creating an opted-out pod in the enrolled namespace")
			manifest := podManifest(optOutPodName, shimNamespace, map[string]string{
				injectAnnotation: "false",
			})
			_, err := applyPodManifest(manifest)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the opted-out pod")

			By("verifying the pod was admitted untouched")
			pod := getPod(optOutPodName, shimNamespace)
			Expect(pod.Spec.InitContainers).To(BeEmpty())
			Expect(volumeNames(pod.Spec.Volumes)).NotTo(ContainElement(HavePrefix(injectedPrefix)))
			Expect(volumeNames(pod.Spec.Volumes)).NotTo(ContainElement(socketVolumeName))
			Expect(pod.Annotations).NotTo(HaveKey(statusAnnotation))
			Expect(pod.Labels).NotTo(HaveKey(shimLabel))

			app := findContainer(pod.Spec.Containers, appContainerName)
			Expect(app).NotTo(BeNil())
			Expect(app.Env).NotTo(ContainElement(HaveField("Name", googleCredentialsEnv)))
		})
	})
})

// verifyWebhookServing asserts that the mutating webhook is actually reachable: cert-manager
// has injected the CA bundle, the webhook Service has ready endpoints and the manager reports
// readyz. Creating a pod before all three hold would fail on the Fail failurePolicy.
func verifyWebhookServing(g Gomega) {
	cmd := exec.Command("kubectl", "get", "mutatingwebhookconfiguration", webhookConfigName,
		"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred(), "Failed to read the MutatingWebhookConfiguration")
	g.Expect(output).NotTo(BeEmpty(), "the webhook caBundle has not been injected yet")

	cmd = exec.Command("kubectl", "get", "endpoints", webhookServiceName, "-n", namespace,
		"-o", "jsonpath={.subsets[*].addresses[*].ip}")
	output, err = utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred(), "Failed to read the webhook service endpoints")
	g.Expect(output).NotTo(BeEmpty(), "the webhook service has no ready endpoints yet")

	cmd = exec.Command("kubectl", "get", "pods", "-l", "control-plane=controller-manager",
		"-n", namespace,
		"-o", `jsonpath={.items[*].status.conditions[?(@.type=="Ready")].status}`)
	output, err = utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred(), "Failed to read the controller-manager readiness")
	g.Expect(output).To(Equal("True"), "the controller-manager is not reporting readyz yet")
}

// podManifest renders a minimal busybox pod with the given annotations.
func podManifest(name, ns string, annotations map[string]string) string {
	meta := ""
	if len(annotations) > 0 {
		keys := make([]string, 0, len(annotations))
		for k := range annotations {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		meta = "  annotations:\n"
		for _, k := range keys {
			meta += fmt.Sprintf("    %q: %q\n", k, annotations[k])
		}
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
%sspec:
  terminationGracePeriodSeconds: 1
  containers:
    - name: %s
      image: %s
      command: ["sleep", "3600"]
`, name, ns, meta, appContainerName, appImage)
}

// applyPodManifest pipes the rendered manifest into `kubectl apply -f -`.
func applyPodManifest(manifest string) (string, error) {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	return utils.Run(cmd)
}

// getPod reads a pod back from the API server and decodes it into a typed corev1.Pod so the
// specs can assert on real fields rather than on jsonpath strings.
func getPod(name, ns string) corev1.Pod {
	GinkgoHelper()
	cmd := exec.Command("kubectl", "get", "pod", name, "-n", ns, "-o", "json")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to read pod %s/%s", ns, name)

	var pod corev1.Pod
	Expect(json.Unmarshal([]byte(output), &pod)).To(Succeed(), "Failed to decode pod %s/%s", ns, name)
	return pod
}

func containerNames(containers []corev1.Container) []string {
	names := make([]string, 0, len(containers))
	for _, c := range containers {
		names = append(names, c.Name)
	}
	return names
}

func volumeNames(volumes []corev1.Volume) []string {
	names := make([]string, 0, len(volumes))
	for _, v := range volumes {
		names = append(names, v.Name)
	}
	return names
}

func findContainer(containers []corev1.Container, name string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	metricsOutput, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
