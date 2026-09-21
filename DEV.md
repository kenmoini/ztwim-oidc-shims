# Development

## Prerequisites

- [Go](https://go.dev/doc/install) Version v1.26.0+
- Docker/Podman
- [Kubectl/oc](https://github.com/lmcclint/ocp-version-manager)
- Access to a Kubernetes/OpenShift cluster
- [Operator SDK installed](https://sdk.operatorframework.io/docs/installation/)

## How this project was scaffolded

```bash
# Initialize Git Repo
git init
echo "# OIDC Shims for Zero Trust Workload Identity Manager" > README.md
git commit -m "first commit"
git branch -M main
git remote add origin git@github.com:kenmoini/ztwim-oidc-shims.git
git push -u origin main

# Initialize the Operator
operator-sdk init --domain kemo.dev --repo github.com/kenmoini/ztwim-oidc-shims

# Create APIs
# NOTE: these were originally scaffolded with --group openshift. The group was renamed to
# "oidcshim" during implementation, so the real API group is oidcshim.kemo.dev, not
# openshift.kemo.dev. See PROJECT and api/v1alpha1/groupversion_info.go.
# OIDCShim
operator-sdk create api --group oidcshim --version v1alpha1 --kind OIDCShim --namespaced=true --resource --controller
# ClusterOIDCShim
operator-sdk create api --group oidcshim --version v1alpha1 --kind ClusterOIDCShim --namespaced=false --resource --controller
```

The `api/v1alpha1` types were then rewritten by hand; the CRDs under `config/crd/bases/` are generated from them with `make manifests`.

## API examples

Worked `OIDCShim` / `ClusterOIDCShim` examples live in [`config/samples/`](config/samples/) and are the canonical reference for the v1alpha1 spec:

| File | Kind | Covers |
| --- | --- | --- |
| `oidcshim_v1alpha1_oidcshim_gcp.yaml` | `OIDCShim` | GCP Workload Identity Federation: annotation parameters, a `template` parameter building the STS audience, a rendered `external_account` credential file. |
| `oidcshim_v1alpha1_oidcshim_aws.yaml` | `OIDCShim` | AWS `AssumeRoleWithWebIdentity`: static audience, defaulted parameters, EKS-compatible token path and env vars. |
| `oidcshim_v1alpha1_clusteroidcshim_azure.yaml` | `ClusterOIDCShim` | Azure Workload Identity: cluster-scoped shim, required parameters, custom token mount path and file name. |
| `oidcshim_v1alpha1_clusteroidcshim_generic.yaml` | `ClusterOIDCShim` | Selector-based (non-enrolment) matching, `configMapKeyRef` parameters with `spec.configMapNamespace`, `spec.inject.containers`. |

`internal/controller/samples_test.go` parses these files, so a sample that drifts from the API fails `make test`.

## Package layout

| Package | Responsibility |
| --- | --- |
| `api/v1alpha1` | `OIDCShim`, `ClusterOIDCShim`, the shared `OIDCShimSpec`/`OIDCShimStatus`, the `Shim` interface both kinds implement, and **every annotation and label key** the operator reads or writes (`common_types.go`). |
| `internal/config` | Manager-level `Options`: the spiffe-helper image and pull policy, CSI driver, socket location, namespace exclusions, render size cap. Flags (`BindFlags`), env fallbacks (`ApplyEnv`) and `Validate`. |
| `internal/spiffehelper` | Renders the `spiffe-helper` HCL config. Stdlib only — no Kubernetes at all. |
| `internal/params` | Resolves a shim's ordered `spec.parameters` into template values (Pod > ServiceAccount > Namespace lookup, defaults, `required`), and the `text/template` engine (`missingkey=error`, no FuncMap). ConfigMap access is a caller-supplied callback. |
| `internal/selection` | Decides whether a pod is matched by a shim (enrolment annotation/label, or label selectors), the `oidcshim.kemo.dev/inject: "false"` opt-out, and namespace exclusions. |
| `internal/injection` | Turns a matched shim plus resolved values into a fully rendered `Plan` (`plan.go`), checks for collisions (`collisions.go`) and applies plans to a `corev1.Pod` (`mutate.go`). Owns the injected container, volume and mount names. |
| `internal/webhook/pod` | The mutating admission webhook. The **only** place the above packages meet the API server: it reads the Namespace/ServiceAccount/shims, calls selection → params → injection, and builds the admission response. |
| `internal/controller` | The `OIDCShim`/`ClusterOIDCShim` reconcilers: validate the spec, write the `Ready` condition (`Valid` / `InvalidSpec`) and `observedGeneration`, and count `status.matchedPods` from the per-shim pod label. |

### The pure-package rule

`internal/config`, `internal/spiffehelper`, `internal/params`, `internal/selection` and `internal/injection` are **pure**: they may import only the Go standard library, `k8s.io/api`, `k8s.io/apimachinery` and `api/v1alpha1` (plus each other). No `client-go`, no `controller-runtime`, no API server access, no clocks or randomness. Everything that talks to a cluster lives in `internal/webhook/pod` and `internal/controller`.

This is what makes the interesting logic testable as plain table tests with no envtest, and it is why `params` takes a `ConfigMapGetter` callback instead of a client. Please keep it that way; adding a `controller-runtime` import to one of those packages is a design change, not a convenience.

## Everyday commands

```bash
make manifests generate   # regenerate CRDs, RBAC, webhook manifests and DeepCopy funcs
make fmt vet              # gofmt + go vet (both are dependencies of build/test anyway)
make lint                 # golangci-lint run
make lint-fix             # golangci-lint run --fix
make test                 # manifests + generate + fmt + vet + go test ./... (excluding e2e), writes cover.out
make build                # go build -o bin/manager cmd/main.go
make run                  # run the manager against your current kubecontext
```

Tool binaries (`kustomize`, `controller-gen`, `setup-envtest`, `golangci-lint`) are downloaded into `./bin/` on demand by the Makefile — do not install them globally.

`make manifests` regenerates `config/crd/bases/*.yaml`, `config/rbac/role.yaml` and `config/webhook/manifests.yaml` from the kubebuilder markers in the Go source. If you change a `+kubebuilder:` marker (especially the `+kubebuilder:webhook:` block in `internal/webhook/pod/handler.go`) you must re-run it and commit the result.

### envtest

`internal/controller` and `internal/webhook/pod` use envtest, which starts a real `kube-apiserver` and `etcd` from `./bin/`. **They bind localhost ports**, so they will fail behind a sandbox or firewall that blocks loopback listeners, and the webhook suite additionally serves TLS on a local port for the API server to call back into. If `make test` hangs or fails with connection errors, check that first. `make setup-envtest` downloads the binaries on their own.

### Golden files

`internal/injection` and `internal/spiffehelper` assert against golden files in their `testdata/` directories. After a deliberate change to the injected pod shape or the rendered helper config, regenerate and **read the diff before committing** — a golden file is the spec of what lands in a user's pod:

```bash
go test ./internal/injection/... -update
go test ./internal/spiffehelper/... -update
git diff internal/injection/testdata internal/spiffehelper/testdata
```

## Building and pushing images

```bash
make docker-build docker-push IMG=quay.io/kenmoini/ztwim-oidc-shims:0.0.1
```

`IMG` defaults to `$(IMAGE_TAG_BASE):$(VERSION)`, i.e. `quay.io/kenmoini/ztwim-oidc-shims:0.0.1`. `CONTAINER_TOOL` defaults to `podman`; set `CONTAINER_TOOL=docker` if you prefer. For a multi-arch image use `make docker-buildx IMG=...`.

## Deploy overlays

| Overlay | For | Serving certificate | spiffe-helper image |
| --- | --- | --- | --- |
| `config/default` | Kubernetes | cert-manager `Issuer` + `Certificate` (`config/certmanager`), CA injected via `cert-manager.io/inject-ca-from` | `ghcr.io/spiffe/spiffe-helper:0.10.0` |
| `config/openshift` | OpenShift | service-ca: `service.beta.openshift.io/serving-cert-secret-name` on the webhook Service, `service.beta.openshift.io/inject-cabundle` on the `MutatingWebhookConfiguration` | `registry.redhat.io/zero-trust-workload-identity-manager/spiffe-helper-rhel9:1.1` via `RELATED_IMAGE_SPIFFE_HELPER` |

```bash
make deploy    IMG=...   # or: make build-installer           -> dist/install.yaml
make deploy-openshift IMG=...  # or: make build-installer-openshift -> dist/install-openshift.yaml
make undeploy / make undeploy-openshift
```

`config/openshift` is a thin overlay on `../default`: it deletes the cert-manager `Issuer` and `Certificate` with `$patch: delete`, swaps the CA-injection annotation, and replaces the `RELATED_IMAGE_SPIFFE_HELPER` env value. That last patch is a JSON6902 patch addressing `containers/0/env/1` by index — **if you reorder the containers or env vars in `config/manager/manager.yaml`, fix the index** and re-check:

```bash
./bin/kustomize build config/default   > /dev/null
./bin/kustomize build config/openshift > /dev/null
./bin/kustomize build config/openshift | grep -c cert-manager   # must be 0
```

Always install through `config/default` or `config/openshift`. The webhook's `namespaceSelector` is a kustomize patch in `config/webhook/kustomization.yaml`, not part of the generated `config/webhook/manifests.yaml`, so applying that generated file directly gives you a fail-closed webhook with no namespace exclusions.

`dist/` is generated output and is gitignored.

## On-cluster verification checklist

Unit tests and envtest cannot cover the parts that only a real cluster exercises. Before calling a change done on OpenShift, confirm:

1. **The Red Hat spiffe-helper image accepts our flags and config.** The injected containers run `spiffe-helper -config /etc/oidcshim/helper.conf` (plus `-daemon-mode=false` for the init container) against a config with `agent_address`, `cert_dir`, `jwt_svids = [{...}]` and `jwt_svid_file_mode`. Flag spelling and the HCL schema have both changed between upstream spiffe-helper releases; check `kubectl logs <pod> -c oidcshim-init-<shim>` on the Red Hat build specifically, not just upstream `ghcr.io/spiffe/spiffe-helper`.
2. **The `restricted-v2` SCC admits the injected pod.** The helper containers request `runAsNonRoot`, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `drop: ["ALL"]` and `seccompProfile: RuntimeDefault`, and the pod gains a `csi.spiffe.io` CSI volume plus an in-memory `emptyDir`. Confirm the pod is admitted under `restricted-v2` (`oc get pod <pod> -o jsonpath='{.metadata.annotations.openshift\.io/scc}'`) with no elevated SCC and no `readOnlyRootFilesystem` conflict with the helper's own scratch needs.
3. **service-ca actually injects the caBundle.** After `make deploy-openshift`, `oc get mutatingwebhookconfiguration ztwim-oidc-shims-mutating-webhook-configuration -o jsonpath='{.webhooks[0].clientConfig.caBundle}'` must be non-empty, and the `webhook-server-cert` Secret must exist in `ztwim-oidc-shims-system`. An empty caBundle means every pod CREATE in non-excluded namespaces will fail (`failurePolicy: Fail`).
4. **Fail-closed behaviour when the operator is down.** Scale the manager Deployment to 0 and confirm that (a) pod creation in an ordinary namespace is *rejected* — that is the intended design — and (b) pod creation still works in `kube-system`, in `openshift.io/run-level` 0/1 namespaces and in `ztwim-oidc-shims-system` itself, so the operator can be restarted. Then scale back to 2 and confirm the PodDisruptionBudget keeps one replica through a rollout (`oc rollout restart deployment/ztwim-oidc-shims-controller-manager -n ztwim-oidc-shims-system`).
5. **End-to-end token exchange.** Follow the GCP (or AWS/Azure) walkthrough in [README.md](README.md) and confirm the provider actually hands back a credential — that is the only test that covers the issuer, the OIDC discovery endpoint reachability and the audience all at once.
