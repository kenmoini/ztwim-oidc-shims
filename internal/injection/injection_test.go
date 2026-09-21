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

package injection

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/kenmoini/ztwim-oidc-shims/api/v1alpha1"
	"github.com/kenmoini/ztwim-oidc-shims/internal/params"
)

var update = flag.Bool("update", false, "update golden files")

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

func testDefaults() Defaults {
	return Defaults{
		HelperImage:           "ghcr.io/spiffe/spiffe-helper:0.10.1",
		HelperImagePullPolicy: corev1.PullIfNotPresent,
		CSIDriver:             "csi.spiffe.io",
		SocketMountPath:       "/spiffe-workload-api",
		SocketFile:            "spire-agent.sock",
	}
}

// basePod is a pod with two app containers, one existing init container and one existing volume.
func basePod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checkout-7f9c",
			Namespace: "payments",
			Labels:    map[string]string{"app": "checkout"},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: "checkout",
			InitContainers: []corev1.Container{
				{Name: "setup", Image: "busybox:1.36"},
			},
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "ghcr.io/example/checkout:v1.4.2",
					Env:   []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "info"}},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "data", MountPath: "/var/lib/data"},
					},
				},
				{Name: "sidecar", Image: "ghcr.io/example/envoy:v1.29"},
			},
			Volumes: []corev1.Volume{
				{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
}

// gcpShim injects one file plus one env var, like GCP workload identity federation.
func gcpShim() *v1alpha1.OIDCShim {
	return &v1alpha1.OIDCShim{
		ObjectMeta: metav1.ObjectMeta{Name: "gcp", Namespace: "payments"},
		Spec: v1alpha1.OIDCShimSpec{
			Provider:       v1alpha1.ProviderGoogle,
			Audience:       "//iam.googleapis.com/projects/{{ .project }}/locations/global/workloadIdentityPools/{{ .pool }}/providers/{{ .provider }}",
			ExtraAudiences: []string{"https://{{ .podNamespace }}.svc.example.com"},
			Inject: v1alpha1.InjectSpec{
				Env: []v1alpha1.EnvVar{
					{Name: "GOOGLE_APPLICATION_CREDENTIALS", Value: "/etc/gcp/credential-configuration.json"},
				},
				Files: []v1alpha1.FileSpec{
					{
						Path: "/etc/gcp/credential-configuration.json",
						Mode: "0444",
						Content: `{"type":"external_account",` +
							`"audience":"//iam.googleapis.com/projects/{{ .project }}",` +
							`"subject_token_type":"urn:ietf:params:oauth:token-type:jwt",` +
							`"credential_source":{"file":"{{ .tokenPath }}"}}`,
					},
				},
			},
		},
	}
}

// awsShim is cluster scoped, injects env only and uses a custom token mountPath.
func awsShim() *v1alpha1.ClusterOIDCShim {
	return &v1alpha1.ClusterOIDCShim{
		ObjectMeta: metav1.ObjectMeta{Name: "aws"},
		Spec: v1alpha1.ClusterOIDCShimSpec{
			OIDCShimSpec: v1alpha1.OIDCShimSpec{
				Provider: v1alpha1.ProviderAWS,
				Audience: "sts.amazonaws.com",
				Token:    v1alpha1.TokenSpec{MountPath: "/var/run/secrets/aws-iam"},
				Inject: v1alpha1.InjectSpec{
					Env: []v1alpha1.EnvVar{
						{Name: "AWS_ROLE_ARN", Value: "arn:aws:iam::{{ .account }}:role/{{ .role }}"},
						{Name: "AWS_WEB_IDENTITY_TOKEN_FILE", Value: "{{ .tokenPath }}"},
						{Name: "AWS_REGION", Value: "us-east-1"},
					},
				},
			},
		},
	}
}

func gcpParams() map[string]string {
	return map[string]string{
		"project":  "482910375",
		"pool":     "prod-pool",
		"provider": "spire",
	}
}

func awsParams() map[string]string {
	return map[string]string{
		"account": "111122223333",
		"role":    "checkout-payments",
	}
}

// valuesFor assembles the params.Values a resolved shim would produce for pod.
func valuesFor(shim v1alpha1.Shim, pod *corev1.Pod, tok Token, extra map[string]string) params.Values {
	b := Builtins(shim, pod, pod.Namespace, tok)
	v := params.Values{
		params.KeyPodNamespace:       b.PodNamespace,
		params.KeyServiceAccountName: b.ServiceAccountName,
		params.KeyShimName:           b.ShimName,
		params.KeyTokenDir:           b.TokenDir,
		params.KeyTokenPath:          b.TokenPath,
	}
	for k, val := range extra {
		v[k] = val
	}
	return v
}

// planFor is the full BuildPlan pipeline for one shim against one pod.
func planFor(t *testing.T, shim v1alpha1.Shim, pod *corev1.Pod, extra map[string]string, d Defaults) *Plan {
	t.Helper()
	tok := ResolveToken(shim.GetName(), shim.ShimSpec().Token)
	plan, err := BuildPlan(shim, valuesFor(shim, pod, tok, extra), tok, d)
	if err != nil {
		t.Fatalf("BuildPlan(%s) error = %v", shim.GetName(), err)
	}
	return plan
}

// checkGolden applies plans to pod and compares the marshalled pod to testdata/<name>.golden.yaml.
func checkGolden(t *testing.T, name string, pod *corev1.Pod, plans []*Plan, wantWarnings []string) []*Plan {
	t.Helper()

	applied, warnings := Apply(pod, plans)
	if diff := cmp.Diff(wantWarnings, warnings); diff != "" {
		t.Errorf("warnings mismatch (-want +got):\n%s", diff)
	}

	got, err := yaml.Marshal(pod)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}

	golden := filepath.Join("testdata", name+".golden.yaml")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatalf("failed to update golden file: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("failed to read golden file: %v", err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("pod mismatch for %s (-want +got):\n%s", golden, diff)
	}
	return applied
}

// ---------------------------------------------------------------------------
// golden tests
// ---------------------------------------------------------------------------

func TestApplySingleGCP(t *testing.T) {
	pod := basePod()
	plan := planFor(t, gcpShim(), pod, gcpParams(), testDefaults())

	applied := checkGolden(t, "single-gcp", pod, []*Plan{plan}, nil)

	if len(applied) != 1 || applied[0].ShimName != "gcp" {
		t.Fatalf("applied = %v, want one plan for gcp", applied)
	}
	if got := pod.Annotations[v1alpha1.ShimsAnnotation]; got != "OIDCShim/payments/gcp" {
		t.Errorf("shims annotation = %q, want %q", got, "OIDCShim/payments/gcp")
	}
	if got := pod.Labels[v1alpha1.ShimLabelKey("gcp")]; got != v1alpha1.ShimLabelValueNamespaced {
		t.Errorf("shim label = %q, want %q", got, v1alpha1.ShimLabelValueNamespaced)
	}
}

func TestApplyMultiShim(t *testing.T) {
	pod := basePod()
	gcp := planFor(t, gcpShim(), pod, gcpParams(), testDefaults())
	aws := planFor(t, awsShim(), pod, awsParams(), testDefaults())

	checkGolden(t, "multi-shim", pod, []*Plan{gcp, aws}, nil)

	wantInits := []string{
		"oidcshim-init-gcp", "oidcshim-refresh-gcp",
		"oidcshim-init-aws", "oidcshim-refresh-aws",
		"setup",
	}
	gotInits := make([]string, 0, len(pod.Spec.InitContainers))
	for _, c := range pod.Spec.InitContainers {
		gotInits = append(gotInits, c.Name)
	}
	if diff := cmp.Diff(wantInits, gotInits); diff != "" {
		t.Errorf("init container order mismatch (-want +got):\n%s", diff)
	}

	// exactly one shared CSI volume
	csi := 0
	for _, v := range pod.Spec.Volumes {
		if v.Name == "spiffe-workload-api" {
			csi++
		}
	}
	if csi != 1 {
		t.Errorf("spiffe-workload-api volume count = %d, want 1", csi)
	}

	want := "OIDCShim/payments/gcp,ClusterOIDCShim/aws"
	if got := pod.Annotations[v1alpha1.ShimsAnnotation]; got != want {
		t.Errorf("shims annotation = %q, want %q", got, want)
	}
	if got := pod.Labels[v1alpha1.ShimLabelKey("aws")]; got != v1alpha1.ShimLabelValueCluster {
		t.Errorf("aws shim label = %q, want %q", got, v1alpha1.ShimLabelValueCluster)
	}
}

func TestApplyContainerFilterSpec(t *testing.T) {
	shim := gcpShim()
	shim.Spec.Inject.Containers = []string{"app"}

	pod := basePod()
	plan := planFor(t, shim, pod, gcpParams(), testDefaults())
	if diff := cmp.Diff([]string{"app"}, plan.Containers); diff != "" {
		t.Errorf("plan.Containers mismatch (-want +got):\n%s", diff)
	}

	checkGolden(t, "container-filter-spec", pod, []*Plan{plan}, nil)

	if n := len(pod.Spec.Containers[1].VolumeMounts); n != 0 {
		t.Errorf("sidecar volumeMounts = %d, want 0", n)
	}
	if n := len(pod.Spec.Containers[1].Env); n != 0 {
		t.Errorf("sidecar env = %d, want 0", n)
	}
}

func TestApplyContainerFilterAnnotation(t *testing.T) {
	shim := gcpShim()
	shim.Spec.Inject.Containers = []string{"app"}

	pod := basePod()
	pod.Annotations = map[string]string{v1alpha1.ContainersAnnotation: "sidecar"}
	plan := planFor(t, shim, pod, gcpParams(), testDefaults())

	checkGolden(t, "container-filter-annotation", pod, []*Plan{plan}, nil)

	if n := len(pod.Spec.Containers[0].VolumeMounts); n != 1 {
		t.Errorf("app volumeMounts = %d, want 1 (only the pre-existing data mount)", n)
	}
	if n := len(pod.Spec.Containers[1].Env); n != 1 {
		t.Errorf("sidecar env = %d, want 1", n)
	}
}

func TestApplyEnvCollision(t *testing.T) {
	pod := basePod()
	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env,
		corev1.EnvVar{Name: "GOOGLE_APPLICATION_CREDENTIALS", Value: "/legacy/creds.json"})

	plan := planFor(t, gcpShim(), pod, gcpParams(), testDefaults())

	wantWarnings := []string{
		`shim "gcp": container "app" already defines env var "GOOGLE_APPLICATION_CREDENTIALS"; leaving it untouched`,
	}
	checkGolden(t, "env-collision", pod, []*Plan{plan}, wantWarnings)

	if got := pod.Spec.Containers[0].Env[1].Value; got != "/legacy/creds.json" {
		t.Errorf("app GOOGLE_APPLICATION_CREDENTIALS = %q, want the pre-existing value", got)
	}
	if n := len(pod.Spec.Containers[1].Env); n != 1 {
		t.Errorf("sidecar env = %d, want 1 (injection not skipped there)", n)
	}
}

func TestApplyVolumeCollision(t *testing.T) {
	pod := basePod()
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name:         "oidcshim-token-gcp",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})

	plan := planFor(t, gcpShim(), pod, gcpParams(), testDefaults())

	wantWarnings := []string{
		`shim "gcp": skipped: volume "oidcshim-token-gcp" already exists on the pod`,
	}
	applied := checkGolden(t, "volume-collision", pod, []*Plan{plan}, wantWarnings)

	if len(applied) != 0 {
		t.Errorf("applied = %v, want none", applied)
	}
	if _, ok := pod.Annotations[v1alpha1.StatusAnnotation]; ok {
		t.Error("status annotation written although no plan applied")
	}
	if len(pod.Spec.InitContainers) != 1 {
		t.Errorf("init containers = %d, want the single pre-existing one", len(pod.Spec.InitContainers))
	}
}

func TestApplyAlreadyInjected(t *testing.T) {
	pod := basePod()
	pod.Annotations = map[string]string{v1alpha1.StatusAnnotation: v1alpha1.StatusInjected}

	plan := planFor(t, gcpShim(), pod, gcpParams(), testDefaults())

	wantWarnings := []string{
		`pod already carries oidcshim.kemo.dev/status=injected; skipping injection`,
	}
	applied := checkGolden(t, "already-injected", pod, []*Plan{plan}, wantWarnings)

	if len(applied) != 0 {
		t.Errorf("applied = %v, want none", applied)
	}
}

func TestApplyHelperOverrides(t *testing.T) {
	shim := gcpShim()
	shim.Spec.SPIFFE = &v1alpha1.SPIFFESpec{
		CSIDriver:       "csi.spiffe.example.com",
		SocketMountPath: "/run/spire/agent",
		SocketFile:      "api.sock",
	}
	shim.Spec.Helper = &v1alpha1.HelperSpec{
		Image:           "registry.example.com/spiffe-helper:custom",
		ImagePullPolicy: corev1.PullAlways,
		Resources: &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot: ptrTo(true),
			RunAsUser:    ptrTo(int64(65532)),
		},
	}

	pod := basePod()
	plan := planFor(t, shim, pod, gcpParams(), testDefaults())

	if plan.CSIDriver != "csi.spiffe.example.com" {
		t.Errorf("plan.CSIDriver = %q, want the spec override", plan.CSIDriver)
	}
	if !strings.Contains(plan.HelperConf, `agent_address = "/run/spire/agent/api.sock"`) {
		t.Errorf("helper.conf does not use the overridden socket path:\n%s", plan.HelperConf)
	}

	checkGolden(t, "helper-overrides", pod, []*Plan{plan}, nil)
}

// ---------------------------------------------------------------------------
// unit tests
// ---------------------------------------------------------------------------

func TestResolveToken(t *testing.T) {
	tests := []struct {
		name string
		spec v1alpha1.TokenSpec
		want Token
	}{
		{
			name: "defaults",
			spec: v1alpha1.TokenSpec{},
			want: Token{
				VolumeName: "oidcshim-token-gcp",
				MountPath:  "/var/run/secrets/oidcshim/gcp",
				FileName:   "token",
				FileMode:   "0644",
			},
		},
		{
			name: "all overridden",
			spec: v1alpha1.TokenSpec{
				VolumeName: "creds",
				MountPath:  "/var/run/creds",
				FileName:   "jwt",
				FileMode:   "0400",
			},
			want: Token{VolumeName: "creds", MountPath: "/var/run/creds", FileName: "jwt", FileMode: "0400"},
		},
		{
			name: "partial override keeps defaults",
			spec: v1alpha1.TokenSpec{MountPath: "/var/run/creds"},
			want: Token{
				VolumeName: "oidcshim-token-gcp",
				MountPath:  "/var/run/creds",
				FileName:   "token",
				FileMode:   "0644",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveToken("gcp", tt.spec)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("ResolveToken() mismatch (-want +got):\n%s", diff)
			}
		})
	}

	if got := (Token{MountPath: "/var/run/creds", FileName: "jwt"}).Path(); got != "/var/run/creds/jwt" {
		t.Errorf("Token.Path() = %q, want %q", got, "/var/run/creds/jwt")
	}
}

func TestBuiltins(t *testing.T) {
	shim := gcpShim()
	tok := ResolveToken("gcp", shim.Spec.Token)

	t.Run("from pod", func(t *testing.T) {
		got := Builtins(shim, basePod(), "payments", tok)
		want := params.Builtins{
			PodNamespace:       "payments",
			ServiceAccountName: "checkout",
			ShimName:           "gcp",
			TokenDir:           "/var/run/secrets/oidcshim/gcp",
			TokenPath:          "/var/run/secrets/oidcshim/gcp/token",
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("Builtins() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("service account falls back to default", func(t *testing.T) {
		pod := basePod()
		pod.Spec.ServiceAccountName = ""
		pod.Namespace = ""
		got := Builtins(shim, pod, "payments", tok)
		if got.ServiceAccountName != "default" {
			t.Errorf("ServiceAccountName = %q, want %q", got.ServiceAccountName, "default")
		}
		if got.PodNamespace != "payments" {
			t.Errorf("PodNamespace = %q, want the explicitly passed namespace", got.PodNamespace)
		}
	})
}

func TestBuildPlanTemplateError(t *testing.T) {
	shim := gcpShim()
	shim.Spec.Audience = "{{ .nope }}"

	pod := basePod()
	tok := ResolveToken("gcp", shim.Spec.Token)
	_, err := BuildPlan(shim, valuesFor(shim, pod, tok, gcpParams()), tok, testDefaults())
	if err == nil {
		t.Fatal("BuildPlan() error = nil, want a template error")
	}
	if !strings.Contains(err.Error(), "audience") {
		t.Errorf("error = %q, want it to mention the audience field", err)
	}
}

func TestBuildPlanFileTemplateError(t *testing.T) {
	shim := gcpShim()
	shim.Spec.Inject.Files[0].Content = "{{ .nope }}"

	pod := basePod()
	tok := ResolveToken("gcp", shim.Spec.Token)
	_, err := BuildPlan(shim, valuesFor(shim, pod, tok, gcpParams()), tok, testDefaults())
	if err == nil {
		t.Fatal("BuildPlan() error = nil, want a template error")
	}
	if !strings.Contains(err.Error(), "/etc/gcp/credential-configuration.json") {
		t.Errorf("error = %q, want it to mention the file path", err)
	}
}

func TestBuildPlanSizeCap(t *testing.T) {
	shim := gcpShim()
	pod := basePod()
	tok := ResolveToken("gcp", shim.Spec.Token)
	values := valuesFor(shim, pod, tok, gcpParams())

	d := testDefaults()
	d.MaxRenderedBytes = 64
	if _, err := BuildPlan(shim, values, tok, d); err == nil {
		t.Fatal("BuildPlan() error = nil, want a size cap error")
	} else if !strings.Contains(err.Error(), "64") {
		t.Errorf("error = %q, want it to mention the cap", err)
	}

	d.MaxRenderedBytes = 1 << 20
	if _, err := BuildPlan(shim, values, tok, d); err != nil {
		t.Fatalf("BuildPlan() with a generous cap error = %v", err)
	}
}

func TestApplyUnknownContainerName(t *testing.T) {
	shim := gcpShim()
	shim.Spec.Inject.Containers = []string{"app", "ghost"}

	pod := basePod()
	plan := planFor(t, shim, pod, gcpParams(), testDefaults())

	applied, warnings := Apply(pod, []*Plan{plan})
	want := []string{`shim "gcp": container "ghost" not found on pod`}
	if diff := cmp.Diff(want, warnings); diff != "" {
		t.Errorf("warnings mismatch (-want +got):\n%s", diff)
	}
	if len(applied) != 1 {
		t.Fatalf("applied = %d plans, want 1", len(applied))
	}
	if len(pod.Spec.Containers[0].Env) != 2 {
		t.Errorf("app env = %d, want 2", len(pod.Spec.Containers[0].Env))
	}
}

func TestApplyNoContainerMatches(t *testing.T) {
	shim := gcpShim()
	shim.Spec.Inject.Containers = []string{"ghost"}

	pod := basePod()
	plan := planFor(t, shim, pod, gcpParams(), testDefaults())

	applied, warnings := Apply(pod, []*Plan{plan})
	want := []string{
		`shim "gcp": container "ghost" not found on pod`,
		`shim "gcp": skipped: none of the requested containers exist on the pod: ghost`,
	}
	if diff := cmp.Diff(want, warnings); diff != "" {
		t.Errorf("warnings mismatch (-want +got):\n%s", diff)
	}
	if len(applied) != 0 {
		t.Errorf("applied = %d plans, want 0", len(applied))
	}
	if len(pod.Spec.Volumes) != 1 {
		t.Errorf("volumes = %d, want the pod left untouched", len(pod.Spec.Volumes))
	}
}

func TestApplyInitContainerCollision(t *testing.T) {
	pod := basePod()
	pod.Spec.InitContainers = append(pod.Spec.InitContainers,
		corev1.Container{Name: "oidcshim-refresh-gcp", Image: "someone-elses:v1"})

	plan := planFor(t, gcpShim(), pod, gcpParams(), testDefaults())

	applied, warnings := Apply(pod, []*Plan{plan})
	want := []string{`shim "gcp": skipped: init container "oidcshim-refresh-gcp" already exists on the pod`}
	if diff := cmp.Diff(want, warnings); diff != "" {
		t.Errorf("warnings mismatch (-want +got):\n%s", diff)
	}
	if len(applied) != 0 {
		t.Errorf("applied = %d plans, want 0", len(applied))
	}
}

func TestApplyMountPathCollision(t *testing.T) {
	pod := basePod()
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts,
		corev1.VolumeMount{Name: "data", MountPath: "/etc/gcp/credential-configuration.json"})

	plan := planFor(t, gcpShim(), pod, gcpParams(), testDefaults())

	applied, warnings := Apply(pod, []*Plan{plan})
	want := []string{
		`shim "gcp": skipped: container "app" already mounts "/etc/gcp/credential-configuration.json"`,
	}
	if diff := cmp.Diff(want, warnings); diff != "" {
		t.Errorf("warnings mismatch (-want +got):\n%s", diff)
	}
	if len(applied) != 0 {
		t.Errorf("applied = %d plans, want 0", len(applied))
	}
}

func TestApplyCSIDriverCollision(t *testing.T) {
	pod := basePod()
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name:         "spiffe-workload-api",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})

	plan := planFor(t, gcpShim(), pod, gcpParams(), testDefaults())

	_, warnings := Apply(pod, []*Plan{plan})
	want := []string{`shim "gcp": skipped: volume "spiffe-workload-api" already exists but is not a CSI volume`}
	if diff := cmp.Diff(want, warnings); diff != "" {
		t.Errorf("warnings mismatch (-want +got):\n%s", diff)
	}

	pod = basePod()
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: "spiffe-workload-api",
		VolumeSource: corev1.VolumeSource{
			CSI: &corev1.CSIVolumeSource{Driver: "csi.other.io"},
		},
	})
	plan = planFor(t, gcpShim(), pod, gcpParams(), testDefaults())
	_, warnings = Apply(pod, []*Plan{plan})
	want = []string{
		`shim "gcp": skipped: volume "spiffe-workload-api" already exists with CSI driver "csi.other.io", want "csi.spiffe.io"`,
	}
	if diff := cmp.Diff(want, warnings); diff != "" {
		t.Errorf("warnings mismatch (-want +got):\n%s", diff)
	}
}

func TestBuildPlanEnvOrderPreserved(t *testing.T) {
	shim := awsShim()
	pod := basePod()
	plan := planFor(t, shim, pod, awsParams(), testDefaults())

	want := []corev1.EnvVar{
		{Name: "AWS_ROLE_ARN", Value: "arn:aws:iam::111122223333:role/checkout-payments"},
		{Name: "AWS_WEB_IDENTITY_TOKEN_FILE", Value: "/var/run/secrets/aws-iam/token"},
		{Name: "AWS_REGION", Value: "us-east-1"},
	}
	if diff := cmp.Diff(want, plan.Env); diff != "" {
		t.Errorf("plan.Env mismatch (-want +got):\n%s", diff)
	}

	Apply(pod, []*Plan{plan})
	if diff := cmp.Diff(want, pod.Spec.Containers[1].Env); diff != "" {
		t.Errorf("sidecar env mismatch (-want +got):\n%s", diff)
	}
}

func TestBuildPlanRendersAudiences(t *testing.T) {
	shim := gcpShim()
	pod := basePod()
	plan := planFor(t, shim, pod, gcpParams(), testDefaults())

	wantAudience := "//iam.googleapis.com/projects/482910375/locations/global/workloadIdentityPools/prod-pool/providers/spire"
	if !strings.Contains(plan.HelperConf, `jwt_audience = "`+wantAudience+`"`) {
		t.Errorf("helper.conf audience not rendered:\n%s", plan.HelperConf)
	}
	if !strings.Contains(plan.HelperConf, `"https://payments.svc.example.com"`) {
		t.Errorf("helper.conf extra audience not rendered:\n%s", plan.HelperConf)
	}
	if plan.ShimKey != "OIDCShim/payments/gcp" || plan.ClusterScoped {
		t.Errorf("ShimKey = %q ClusterScoped = %v", plan.ShimKey, plan.ClusterScoped)
	}
	if len(plan.Files) != 1 || plan.Files[0].Mode != "0444" {
		t.Fatalf("plan.Files = %+v, want one file with mode 0444", plan.Files)
	}
	if !strings.Contains(plan.Files[0].Content, `"file":"/var/run/secrets/oidcshim/gcp/token"`) {
		t.Errorf("file content not rendered:\n%s", plan.Files[0].Content)
	}
}

func TestBuildPlanDefaultFileMode(t *testing.T) {
	shim := gcpShim()
	shim.Spec.Inject.Files[0].Mode = ""

	pod := basePod()
	plan := planFor(t, shim, pod, gcpParams(), testDefaults())
	if plan.Files[0].Mode != "0644" {
		t.Errorf("file mode = %q, want %q", plan.Files[0].Mode, "0644")
	}
}

func TestBuildPlanDoesNotAliasShimOverrides(t *testing.T) {
	shim := gcpShim()
	shim.Spec.Helper = &v1alpha1.HelperSpec{
		Resources: &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")},
		},
		SecurityContext: &corev1.SecurityContext{RunAsUser: ptrTo(int64(65532))},
	}

	pod := basePod()
	plan := planFor(t, shim, pod, gcpParams(), testDefaults())
	Apply(pod, []*Plan{plan})

	// Mutating the injected containers must not reach back into the shim resource,
	// which normally comes from a shared informer cache.
	pod.Spec.InitContainers[0].SecurityContext.RunAsUser = ptrTo(int64(0))
	pod.Spec.InitContainers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("4")

	if got := *shim.Spec.Helper.SecurityContext.RunAsUser; got != 65532 {
		t.Errorf("shim securityContext.runAsUser = %d, want it untouched", got)
	}
	if got := shim.Spec.Helper.Resources.Requests[corev1.ResourceCPU]; got.String() != "10m" {
		t.Errorf("shim resources.requests.cpu = %s, want it untouched", got.String())
	}
	if got := *pod.Spec.InitContainers[1].SecurityContext.RunAsUser; got != 65532 {
		t.Errorf("sidecar securityContext.runAsUser = %d, want it independent of the init container", got)
	}
}

func TestBuildPlanDuplicateFilePaths(t *testing.T) {
	shim := gcpShim()
	shim.Spec.Inject.Files = append(shim.Spec.Inject.Files, v1alpha1.FileSpec{
		Path:    "/etc/gcp/credential-configuration.json",
		Content: "second file at the same path",
	})

	pod := basePod()
	tok := ResolveToken("gcp", shim.Spec.Token)
	_, err := BuildPlan(shim, valuesFor(shim, pod, tok, gcpParams()), tok, testDefaults())
	if err == nil {
		t.Fatal("BuildPlan() error = nil, want a duplicate path error")
	}
	if !strings.Contains(err.Error(), "/etc/gcp/credential-configuration.json") {
		t.Errorf("error = %q, want it to name the duplicated path", err)
	}
}

func TestBuildPlanFilePathCollidesWithTokenMountPath(t *testing.T) {
	shim := gcpShim()
	shim.Spec.Inject.Files[0].Path = "/var/run/secrets/oidcshim/gcp"

	pod := basePod()
	tok := ResolveToken("gcp", shim.Spec.Token)
	_, err := BuildPlan(shim, valuesFor(shim, pod, tok, gcpParams()), tok, testDefaults())
	if err == nil {
		t.Fatal("BuildPlan() error = nil, want a token mountPath collision error")
	}
	if !strings.Contains(err.Error(), "/var/run/secrets/oidcshim/gcp") {
		t.Errorf("error = %q, want it to name the colliding path", err)
	}
}
