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
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
	"github.com/kenmoini/ztwim-oidc-shims/internal/config"
)

// These specs run against a real API server (envtest) with the committed
// MutatingWebhookConfiguration from config/webhook installed, so they prove the API server
// accepts the shape this webhook injects. The fake-client tests in handler_test.go stay:
// they cover the branches that are cheaper to exercise without an API server.

var (
	// envCtx is cancelled in AfterSuite, which stops the manager.
	envCtx    context.Context
	envCancel context.CancelFunc

	testEnv *envtest.Environment
	envCfg  *rest.Config

	// envScheme carries the core types plus v1alpha1.
	envScheme *runtime.Scheme

	// envClient talks straight to the API server (no cache): every assertion reads the
	// object the API server actually persisted.
	envClient client.Client

	// envCachedReader is the manager's cached client, the same one the handler reads
	// Namespaces, ServiceAccounts and shims through. Specs wait on it before creating a
	// pod so that the informers have observed their fixtures.
	envCachedReader client.Reader
)

func TestPodWebhook(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Pod Webhook Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	envCtx, envCancel = context.WithCancel(context.Background())

	envScheme = runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(envScheme))
	utilruntime.Must(v1alpha1.AddToScheme(envScheme))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{webhookManifestDir()},
		},
	}
	if dir := firstFoundEnvTestBinaryDir(); dir != "" {
		testEnv.BinaryAssetsDirectory = dir
	}

	var err error
	envCfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(envCfg).NotTo(BeNil())

	envClient, err = client.New(envCfg, client.Options{Scheme: envScheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(envClient).NotTo(BeNil())

	By("starting the manager with the pod webhook installed")
	opts := testEnv.WebhookInstallOptions
	mgr, err := ctrl.NewManager(envCfg, ctrl.Options{
		Scheme: envScheme,
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    opts.LocalServingHost,
			Port:    opts.LocalServingPort,
			CertDir: opts.LocalServingCertDir,
		}),
		LeaderElection:         false,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	Expect(err).NotTo(HaveOccurred())

	Expect(SetupWithManager(mgr, config.Defaults())).To(Succeed())
	envCachedReader = mgr.GetClient()

	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(envCtx)).To(Succeed(), "manager exited with an error")
	}()

	By("waiting for the webhook server to serve TLS")
	addr := fmt.Sprintf("%s:%d", opts.LocalServingHost, opts.LocalServingPort)
	Eventually(func() error {
		conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}) // #nosec G402 -- test-only CA
		if err != nil {
			return err
		}
		return conn.Close()
	}).WithTimeout(30 * time.Second).WithPolling(100 * time.Millisecond).Should(Succeed())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	if envCancel != nil {
		envCancel()
	}
	if testEnv != nil {
		Expect(testEnv.Stop()).To(Succeed())
	}
})

// webhookManifestDir returns a directory holding a copy of the committed
// config/webhook/manifests.yaml, and nothing else.
//
// envtest parses every .yaml file of a Paths directory and fails on any document that is
// not a Kubernetes object, so config/webhook cannot be used directly: it also holds
// namespace_selector_patch.yaml, a kustomize JSON-patch list. Only manifests.yaml is
// copied, so the webhook this suite installs is still the generated one, byte for byte.
func webhookManifestDir() string {
	GinkgoHelper()

	src := filepath.Join("..", "..", "..", "config", "webhook", "manifests.yaml")
	manifest, err := os.ReadFile(src) // #nosec G304 -- fixed, repo-relative test input
	Expect(err).NotTo(HaveOccurred())

	dir, err := os.MkdirTemp("", "oidcshim-webhook-manifests-")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		Expect(os.RemoveAll(dir)).To(Succeed())
	})

	Expect(os.WriteFile(filepath.Join(dir, "manifests.yaml"), manifest, 0o600)).To(Succeed())
	return dir
}

// firstFoundEnvTestBinaryDir locates the first binary directory under bin/k8s so the suite
// also runs from an IDE, where KUBEBUILDER_ASSETS is not set. Run 'make setup-envtest' first.
func firstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
