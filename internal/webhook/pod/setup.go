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
	"fmt"
	"net/http"
	"sync/atomic"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
	"github.com/kenmoini/ztwim-oidc-shims/internal/config"
)

// Path is the webhook endpoint path, matching the +kubebuilder:webhook marker.
const Path = "/mutate-v1-pod"

// SetupWithManager registers the pod mutating webhook, an informer warm-up runnable and the
// readiness checks ("webhook" = webhook server started, "informers" = warm-up finished).
func SetupWithManager(mgr ctrl.Manager, opts config.Options) error {
	mgr.GetWebhookServer().Register(Path, &webhook.Admission{Handler: &Handler{
		Reader:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Decoder:   admission.NewDecoder(mgr.GetScheme()),
		Options:   opts,
	}})

	w := NewWarmer(mgr.GetCache(),
		&corev1.Namespace{},
		&corev1.ServiceAccount{},
		&v1alpha1.OIDCShim{},
		&v1alpha1.ClusterOIDCShim{},
	)
	if err := mgr.Add(w); err != nil {
		return fmt.Errorf("add informer warm-up: %w", err)
	}
	if err := mgr.AddReadyzCheck("informers", w.Check); err != nil {
		return fmt.Errorf("add informers ready check: %w", err)
	}
	if err := mgr.AddReadyzCheck("webhook", mgr.GetWebhookServer().StartedChecker()); err != nil {
		return fmt.Errorf("add webhook ready check: %w", err)
	}

	return nil
}

// Warmer creates the informers the handler reads from as soon as the cache has started, so the
// first admission request does not pay the list/watch cost against the 10s webhook timeout.
// It runs on every replica (NeedLeaderElection returns false) and doubles as a readiness checker.
type Warmer struct {
	cache   cache.Cache
	objects []client.Object
	ready   atomic.Bool
}

// NewWarmer returns a Warmer that starts an informer for each of objects.
func NewWarmer(c cache.Cache, objects ...client.Object) *Warmer {
	return &Warmer{cache: c, objects: objects}
}

// Start implements manager.Runnable. The manager only starts non-leader-election runnables
// once the caches have synced, so GetInformer returns as soon as each informer exists.
// It returns immediately instead of blocking until ctx is done: there is nothing to run.
func (w *Warmer) Start(ctx context.Context) error {
	for _, obj := range w.objects {
		if _, err := w.cache.GetInformer(ctx, obj); err != nil {
			return fmt.Errorf("warm up informer for %T: %w", obj, err)
		}
	}
	w.ready.Store(true)
	return nil
}

// NeedLeaderElection implements manager.LeaderElectionRunnable: every replica serves
// admission requests, so every replica must warm its own informers.
func (w *Warmer) NeedLeaderElection() bool { return false }

// Check implements healthz.Checker: the replica is only ready once the informers exist.
func (w *Warmer) Check(_ *http.Request) error {
	if !w.ready.Load() {
		return errors.New("informers not synced")
	}
	return nil
}
