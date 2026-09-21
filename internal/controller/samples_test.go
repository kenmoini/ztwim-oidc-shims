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
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// Every file under config/samples must be accepted by the CRD schema (including CEL
// rules and defaults) served by the API server.
var _ = Describe("Samples", func() {
	samples, err := filepath.Glob(filepath.Join("..", "..", "config", "samples", "oidcshim_v1alpha1_*.yaml"))
	Expect(err).NotTo(HaveOccurred())
	Expect(samples).NotTo(BeEmpty())

	for _, path := range samples {
		It("accepts "+filepath.Base(path), func() {
			ctx := context.Background()
			raw, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())

			obj := &unstructured.Unstructured{}
			Expect(yaml.Unmarshal(raw, obj)).To(Succeed())
			if obj.GetKind() == "OIDCShim" {
				obj.SetNamespace(testNamespace)
			}

			Expect(k8sClient.Create(ctx, obj)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
			})

			Expect(obj.GetGeneration()).To(BeNumerically(">=", 1))
		})
	}
})
