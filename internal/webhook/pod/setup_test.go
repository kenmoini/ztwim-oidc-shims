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
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
)

func TestPathMatchesWebhookMarker(t *testing.T) {
	if Path != "/mutate-v1-pod" {
		t.Fatalf("Path must match the +kubebuilder:webhook marker, got %q", Path)
	}
}

func TestWarmerBecomesReadyAfterStart(t *testing.T) {
	informers := &informertest.FakeInformers{Scheme: testScheme(t)}
	w := NewWarmer(informers,
		&corev1.Namespace{},
		&corev1.ServiceAccount{},
		&v1alpha1.OIDCShim{},
		&v1alpha1.ClusterOIDCShim{},
	)

	if w.NeedLeaderElection() {
		t.Error("the warm-up must run on every replica, not only the leader")
	}
	if err := w.Check(nil); err == nil {
		t.Error("expected Check to fail before Start")
	}

	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := w.Check(nil); err != nil {
		t.Errorf("expected Check to succeed after Start, got %v", err)
	}
	if len(informers.InformersByGVK) != 4 {
		t.Errorf("expected 4 informers to be created, got %d", len(informers.InformersByGVK))
	}
}

func TestWarmerStaysUnreadyWhenAnInformerFails(t *testing.T) {
	informers := &informertest.FakeInformers{Scheme: testScheme(t), Error: errors.New("no watch for you")}
	w := NewWarmer(informers, &corev1.Namespace{})

	if err := w.Start(context.Background()); err == nil {
		t.Fatal("expected Start to fail")
	}
	if err := w.Check(nil); err == nil {
		t.Error("expected Check to keep failing after a failed Start")
	}
}
