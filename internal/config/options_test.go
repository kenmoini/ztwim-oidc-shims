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

package config

import (
	"flag"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestDefaults(t *testing.T) {
	o := Defaults()

	if o.SpiffeHelperImage != DefaultSpiffeHelperImage {
		t.Errorf("SpiffeHelperImage = %q, want %q", o.SpiffeHelperImage, DefaultSpiffeHelperImage)
	}
	if o.SpiffeHelperImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("SpiffeHelperImagePullPolicy = %q, want %q", o.SpiffeHelperImagePullPolicy, corev1.PullIfNotPresent)
	}
	if o.SpiffeCSIDriver != DefaultSpiffeCSIDriver {
		t.Errorf("SpiffeCSIDriver = %q, want %q", o.SpiffeCSIDriver, DefaultSpiffeCSIDriver)
	}
	if o.SpiffeSocketMountPath != DefaultSpiffeSocketMountPath {
		t.Errorf("SpiffeSocketMountPath = %q, want %q", o.SpiffeSocketMountPath, DefaultSpiffeSocketMountPath)
	}
	if o.SpiffeSocketFile != DefaultSpiffeSocketFile {
		t.Errorf("SpiffeSocketFile = %q, want %q", o.SpiffeSocketFile, DefaultSpiffeSocketFile)
	}
	if len(o.ExcludedNamespaces) != 0 {
		t.Errorf("ExcludedNamespaces = %v, want empty", o.ExcludedNamespaces)
	}
	if !reflect.DeepEqual(o.ExcludedNamespacePrefixes, []string{"kube-"}) {
		t.Errorf("ExcludedNamespacePrefixes = %v, want [kube-]", o.ExcludedNamespacePrefixes)
	}
	if o.ZTWIMNamespace != DefaultZTWIMNamespace {
		t.Errorf("ZTWIMNamespace = %q, want %q", o.ZTWIMNamespace, DefaultZTWIMNamespace)
	}
	if o.OperatorNamespace != "" {
		t.Errorf("OperatorNamespace = %q, want empty", o.OperatorNamespace)
	}
	if o.MaxRenderedBytes != DefaultMaxRenderedBytes {
		t.Errorf("MaxRenderedBytes = %d, want %d", o.MaxRenderedBytes, DefaultMaxRenderedBytes)
	}
}

func fakeGetenv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func TestApplyEnv_SpiffeHelperImageTakesPrecedenceOverRelatedImage(t *testing.T) {
	o := Defaults()
	o.ApplyEnv(fakeGetenv(map[string]string{
		EnvSpiffeHelperImage:        "example.com/spiffe-helper:1.2.3",
		EnvRelatedImageSpiffeHelper: "example.com/related-spiffe-helper:9.9.9",
	}))

	if got, want := o.SpiffeHelperImage, "example.com/spiffe-helper:1.2.3"; got != want {
		t.Errorf("SpiffeHelperImage = %q, want %q", got, want)
	}
}

func TestApplyEnv_RelatedImageUsedWhenSpiffeHelperImageUnset(t *testing.T) {
	o := Defaults()
	o.ApplyEnv(fakeGetenv(map[string]string{
		EnvRelatedImageSpiffeHelper: "example.com/related-spiffe-helper:9.9.9",
	}))

	if got, want := o.SpiffeHelperImage, "example.com/related-spiffe-helper:9.9.9"; got != want {
		t.Errorf("SpiffeHelperImage = %q, want %q", got, want)
	}
}

func TestApplyEnv_NeitherImageEnvSetKeepsDefault(t *testing.T) {
	o := Defaults()
	o.ApplyEnv(fakeGetenv(map[string]string{}))

	if got, want := o.SpiffeHelperImage, DefaultSpiffeHelperImage; got != want {
		t.Errorf("SpiffeHelperImage = %q, want %q", got, want)
	}
}

func TestApplyEnv_PodNamespaceSetsOperatorNamespace(t *testing.T) {
	o := Defaults()
	o.ApplyEnv(fakeGetenv(map[string]string{
		EnvPodNamespace: "my-operator-ns",
	}))

	if got, want := o.OperatorNamespace, "my-operator-ns"; got != want {
		t.Errorf("OperatorNamespace = %q, want %q", got, want)
	}
}

func TestApplyEnv_PodNamespaceUnsetLeavesEmpty(t *testing.T) {
	o := Defaults()
	o.ApplyEnv(fakeGetenv(map[string]string{}))

	if o.OperatorNamespace != "" {
		t.Errorf("OperatorNamespace = %q, want empty", o.OperatorNamespace)
	}
}

func TestBindFlags_ParsesEveryFlag(t *testing.T) {
	o := Defaults()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	o.BindFlags(fs)

	args := []string{
		"--spiffe-helper-image=example.com/helper:2.0.0",
		"--spiffe-helper-image-pull-policy=Always",
		"--spiffe-csi-driver=custom.csi.example.com",
		"--spiffe-socket-mount-path=/custom/mount",
		"--spiffe-socket-file=custom.sock",
		"--excluded-namespaces=ns-one, ns-two ,, ns-three",
		"--excluded-namespace-prefixes=kube-, openshift- ,",
		"--ztwim-namespace=custom-ztwim-ns",
		"--max-rendered-bytes=12345",
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("fs.Parse() error = %v", err)
	}

	if got, want := o.SpiffeHelperImage, "example.com/helper:2.0.0"; got != want {
		t.Errorf("SpiffeHelperImage = %q, want %q", got, want)
	}
	if got, want := o.SpiffeHelperImagePullPolicy, corev1.PullAlways; got != want {
		t.Errorf("SpiffeHelperImagePullPolicy = %q, want %q", got, want)
	}
	if got, want := o.SpiffeCSIDriver, "custom.csi.example.com"; got != want {
		t.Errorf("SpiffeCSIDriver = %q, want %q", got, want)
	}
	if got, want := o.SpiffeSocketMountPath, "/custom/mount"; got != want {
		t.Errorf("SpiffeSocketMountPath = %q, want %q", got, want)
	}
	if got, want := o.SpiffeSocketFile, "custom.sock"; got != want {
		t.Errorf("SpiffeSocketFile = %q, want %q", got, want)
	}
	if want := []string{"ns-one", "ns-two", "ns-three"}; !reflect.DeepEqual(o.ExcludedNamespaces, want) {
		t.Errorf("ExcludedNamespaces = %v, want %v", o.ExcludedNamespaces, want)
	}
	if want := []string{"kube-", "openshift-"}; !reflect.DeepEqual(o.ExcludedNamespacePrefixes, want) {
		t.Errorf("ExcludedNamespacePrefixes = %v, want %v", o.ExcludedNamespacePrefixes, want)
	}
	if got, want := o.ZTWIMNamespace, "custom-ztwim-ns"; got != want {
		t.Errorf("ZTWIMNamespace = %q, want %q", got, want)
	}
	if got, want := o.MaxRenderedBytes, 12345; got != want {
		t.Errorf("MaxRenderedBytes = %d, want %d", got, want)
	}
}

func TestBindFlags_FlagOverridesEnv(t *testing.T) {
	o := Defaults()
	o.ApplyEnv(fakeGetenv(map[string]string{
		EnvSpiffeHelperImage: "example.com/env-image:1.0.0",
	}))

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	o.BindFlags(fs)

	if err := fs.Parse([]string{"--spiffe-helper-image=example.com/flag-image:2.0.0"}); err != nil {
		t.Fatalf("fs.Parse() error = %v", err)
	}

	if got, want := o.SpiffeHelperImage, "example.com/flag-image:2.0.0"; got != want {
		t.Errorf("SpiffeHelperImage = %q, want %q (flag should win over env)", got, want)
	}
}

func TestBindFlags_DefaultsComeFromCurrentOptionValues(t *testing.T) {
	o := Defaults()
	o.ApplyEnv(fakeGetenv(map[string]string{
		EnvSpiffeHelperImage: "example.com/env-image:1.0.0",
	}))

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	o.BindFlags(fs)

	if err := fs.Parse(nil); err != nil {
		t.Fatalf("fs.Parse() error = %v", err)
	}

	if got, want := o.SpiffeHelperImage, "example.com/env-image:1.0.0"; got != want {
		t.Errorf("SpiffeHelperImage = %q, want %q (unset flag should keep env-derived default)", got, want)
	}
}

func TestValidate_ValidDefaultsPasses(t *testing.T) {
	o := Defaults()
	if err := o.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

func TestValidate_EmptyImage(t *testing.T) {
	o := Defaults()
	o.SpiffeHelperImage = ""
	if err := o.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for empty image")
	}
}

func TestValidate_InvalidPullPolicy(t *testing.T) {
	o := Defaults()
	o.SpiffeHelperImagePullPolicy = "Sometimes"
	if err := o.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for invalid pull policy")
	}
}

func TestValidate_ValidPullPolicies(t *testing.T) {
	for _, p := range []corev1.PullPolicy{corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever} {
		o := Defaults()
		o.SpiffeHelperImagePullPolicy = p
		if err := o.Validate(); err != nil {
			t.Errorf("Validate() with pull policy %q error = %v, want nil", p, err)
		}
	}
}

func TestValidate_EmptyCSIDriver(t *testing.T) {
	o := Defaults()
	o.SpiffeCSIDriver = ""
	if err := o.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for empty CSI driver")
	}
}

func TestValidate_NonAbsoluteSocketMountPath(t *testing.T) {
	o := Defaults()
	o.SpiffeSocketMountPath = "relative/path"
	if err := o.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for non-absolute socket mount path")
	}
}

func TestValidate_EmptySocketFile(t *testing.T) {
	o := Defaults()
	o.SpiffeSocketFile = ""
	if err := o.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for empty socket file")
	}
}

func TestValidate_SocketFileContainsSlash(t *testing.T) {
	o := Defaults()
	o.SpiffeSocketFile = "sub/dir.sock"
	if err := o.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for socket file containing '/'")
	}
}

func TestValidate_NonPositiveMaxRenderedBytes(t *testing.T) {
	for _, v := range []int{0, -1} {
		o := Defaults()
		o.MaxRenderedBytes = v
		if err := o.Validate(); err == nil {
			t.Errorf("Validate() with MaxRenderedBytes=%d error = nil, want error", v)
		}
	}
}

func TestValidate_MultipleErrorsJoined(t *testing.T) {
	o := Defaults()
	o.SpiffeHelperImage = ""
	o.SpiffeCSIDriver = ""
	err := o.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "image") {
		t.Errorf("Validate() error = %q, want it to mention image", msg)
	}
}

func TestSocketPath(t *testing.T) {
	o := Defaults()
	o.SpiffeSocketMountPath = "/spiffe-workload-api"
	o.SpiffeSocketFile = "spire-agent.sock"

	if got, want := o.SocketPath(), "/spiffe-workload-api/spire-agent.sock"; got != want {
		t.Errorf("SocketPath() = %q, want %q", got, want)
	}
}

func TestExcludedNamespaceNames_DedupSortAndIncludesOperatorAndZTWIM(t *testing.T) {
	o := Defaults()
	o.ExcludedNamespaces = []string{"zeta", "alpha", "zero-trust-workload-identity-manager"}
	o.OperatorNamespace = "operator-ns"
	o.ZTWIMNamespace = "zero-trust-workload-identity-manager"

	got := o.ExcludedNamespaceNames()
	want := []string{"alpha", "operator-ns", "zero-trust-workload-identity-manager", "zeta"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExcludedNamespaceNames() = %v, want %v", got, want)
	}
}

func TestExcludedNamespaceNames_EmptyOperatorNamespaceOmitted(t *testing.T) {
	o := Defaults()
	o.ExcludedNamespaces = []string{"foo"}
	o.OperatorNamespace = ""
	o.ZTWIMNamespace = DefaultZTWIMNamespace

	got := o.ExcludedNamespaceNames()
	want := []string{"foo", DefaultZTWIMNamespace}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExcludedNamespaceNames() = %v, want %v", got, want)
	}
}
