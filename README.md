# OIDC Shims for Zero Trust Workload Identity Manager

The [Zero Trust Workload Identity Manager Operator for OpenShift](https://github.com/openshift/zero-trust-workload-identity-manager/tree/main) (ZTWIM) provides SPIFFE/SPIRE integration so workloads can reach resources at federated OIDC providers such as AWS IAM, Azure Entra ID and Google Cloud Workload Identity Federation. Doing that by hand means adding an init container that mints the first JWT-SVID before the app starts, a sidecar that keeps refreshing it, a volume to hold it, a rendered credential file and a handful of environment variables — to every workload, in every namespace. This operator does it for you: you declare an `OIDCShim` (namespaced) or `ClusterOIDCShim` (cluster-scoped), enrol a Namespace, ServiceAccount or Pod with a single annotation or label, and a mutating admission webhook injects the whole thing at pod creation, Service Mesh sidecar style.

For each shim that matches a pod, the webhook adds:

- an **init container** `oidcshim-init-<shim>` running `spiffe-helper -config /etc/oidcshim/helper.conf -daemon-mode=false`, so the token exists before the app container starts;
- a **native sidecar** `oidcshim-refresh-<shim>` (an init container with `restartPolicy: Always`) running the same config in daemon mode, which keeps the JWT-SVID fresh for the life of the pod;
- a **CSI volume** `spiffe-workload-api` (driver `csi.spiffe.io`, read-only) carrying the SPIRE agent Workload API socket, mounted only into the helper containers — added once no matter how many shims apply;
- an **in-memory `emptyDir`** `oidcshim-token-<shim>` (default) mounted read-only into your app containers at `/var/run/secrets/oidcshim/<shim>`, holding the JWT-SVID;
- a **downward-API volume** `oidcshim-config-<shim>` that materialises the rendered `spiffe-helper` config and any `spec.inject.files` from pod annotations — no ConfigMaps or Secrets are created;
- the **environment variables** from `spec.inject.env`, rendered per pod, appended to each targeted app container;
- pod **annotations and labels** recording what was done (see [Annotation and label reference](#annotation-and-label-reference)).

The helper containers run with `runAsNonRoot`, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, all capabilities dropped and `seccompProfile: RuntimeDefault`, which is what the `restricted-v2` SCC and the `restricted` Pod Security Standard require. Your application containers are not restarted, re-imaged or otherwise altered beyond the mounts and environment variables above.

## Prerequisites

- **ZTWIM installed and running**, with a SPIRE server and SPIRE agents healthy on every node the workloads land on.
- **The SPIFFE CSI driver** (`csi.spiffe.io`) deployed, so pods can mount the agent Workload API socket. This is what ZTWIM installs; if you run it under a different driver name, set `spec.spiffe.csiDriver` on the shim or `--spiffe-csi-driver` on the manager.
- **A `ClusterSPIFFEID` that covers the workload's ServiceAccount.** Without one the SPIRE agent will not attest the pod and `spiffe-helper` will never obtain a JWT-SVID — the init container will block pod startup until it does.
- **`SpireServer.spec.jwtIssuer` set to a reachable, publicly resolvable HTTPS issuer URL**, and the matching **OIDC discovery endpoint** exposed (ZTWIM's `spire-spiffe-oidc-discovery-provider`, usually behind a Route). The cloud provider fetches `<issuer>/.well-known/openid-configuration` and `<issuer>/keys` to validate the JWT-SVID, so the endpoint must be reachable *from the provider*, not just from the cluster.
- **A trust relationship configured on the cloud side** that names that issuer — see the per-provider sections below.
- On Kubernetes, **cert-manager** for the webhook serving certificate. On OpenShift, the built-in **service-ca** operator is used instead; no cert-manager needed.

> **JWT-SVID lifetime.** SPIRE issues JWT-SVIDs with a default TTL of **5 minutes**, and `spiffe-helper` rewrites the token when roughly half the TTL has elapsed. Applications must re-read the token file rather than caching its contents — every AWS, Azure and Google SDK listed below already does. If your provider requires a longer-lived assertion, raise the JWT TTL on the SPIRE server rather than working around the refresh.

## Install

Both paths install the CRDs, the manager Deployment (2 replicas plus a PodDisruptionBudget), RBAC, the webhook Service and the `MutatingWebhookConfiguration`.

> Always install through `config/default` or `config/openshift`. The webhook's `namespaceSelector` — the thing that keeps this operator from wedging `kube-system` — is applied as a kustomize patch in `config/webhook`, so it is **not** present in `config/webhook/manifests.yaml`. Applying that generated file on its own produces a fail-closed webhook with no namespace exclusions.

### Kubernetes (cert-manager)

```bash
# cert-manager must already be installed in the cluster.
make deploy IMG=quay.io/kenmoini/ztwim-oidc-shims:0.0.1
```

Or render the manifests without applying them:

```bash
make build-installer IMG=quay.io/kenmoini/ztwim-oidc-shims:0.0.1   # writes dist/install.yaml
```

### OpenShift (service-ca)

The `config/openshift` overlay drops the cert-manager `Issuer` and `Certificate`, annotates the webhook Service with `service.beta.openshift.io/serving-cert-secret-name: webhook-server-cert`, annotates the `MutatingWebhookConfiguration` with `service.beta.openshift.io/inject-cabundle: "true"`, and points `RELATED_IMAGE_SPIFFE_HELPER` at `registry.redhat.io/zero-trust-workload-identity-manager/spiffe-helper-rhel9:1.1`.

```bash
make deploy-openshift IMG=quay.io/kenmoini/ztwim-oidc-shims:0.0.1
make build-installer-openshift                                     # writes dist/install-openshift.yaml
```

Remove either with `make undeploy` / `make undeploy-openshift`.

## Walkthrough: Google Cloud Workload Identity Federation

`config/samples/oidcshim_v1alpha1_oidcshim_gcp.yaml` is a complete namespaced shim for GCP. It resolves the pool and provider from annotations, builds the STS audience from them with a `template` parameter, writes the JWT-SVID to `/var/run/secrets/oidcshim/gcp/token`, renders an `external_account` credential file next to it and points `GOOGLE_APPLICATION_CREDENTIALS` at it.

**1. On the Google side**, create a workload identity pool and an OIDC provider whose issuer URI is your `SpireServer.spec.jwtIssuer`, with the allowed audience set to the pool provider's full resource name (the default audience Google expects). Grant the pool principal `roles/iam.workloadIdentityUser` on the service account you want to impersonate.

**2. Create the shim** in the workload's namespace:

```bash
kubectl apply -n my-app -f config/samples/oidcshim_v1alpha1_oidcshim_gcp.yaml
kubectl get oidcshim -n my-app
# NAME   PROVIDER   READY   PODS   AGE
# gcp    google     True    0      5s
```

`READY=True` means the spec validated. `READY=False` with reason `InvalidSpec` means the webhook will skip the shim entirely and emit an admission warning; `kubectl describe oidcshim gcp -n my-app` shows why.

**3. Enrol the namespace** and supply the parameters. Enrolment and parameters may live on the Namespace, the ServiceAccount or the Pod; higher-precedence objects win (see [Parameter precedence](#parameter-precedence)).

```bash
kubectl annotate namespace my-app \
  oidcshim.kemo.dev/shim=gcp \
  iam.gke.io/gcp-project-number=123456789012 \
  iam.gke.io/gcp-wid-pool=my-pool \
  iam.gke.io/gcp-wid-provider=my-provider \
  iam.gke.io/gcp-service-account=app@my-project.iam.gserviceaccount.com
```

`iam.gke.io/gcp-wid-pool-location` is optional and defaults to `global`.

**4. Create a pod** (or roll an existing Deployment — injection only happens at pod CREATE, so existing pods are untouched until they are recreated):

```bash
kubectl rollout restart deployment/my-app -n my-app
```

**5. Check the injected shape:**

```bash
kubectl get pod -n my-app -l app=my-app -o yaml | less
```

You should see, on a pod named e.g. `my-app-7c9f-abcde`:

```yaml
metadata:
  annotations:
    oidcshim.kemo.dev/status: injected
    oidcshim.kemo.dev/shims: OIDCShim/my-app/gcp
    oidcshim.kemo.dev/helper-conf-gcp: |
      agent_address = "/spiffe-workload-api/spire-agent.sock"
      cert_dir = "/var/run/secrets/oidcshim/gcp"
      jwt_svids = [{jwt_audience = "https://iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/my-pool/providers/my-provider", jwt_extra_audiences = [], jwt_svid_file_name = "token"}]
      jwt_svid_file_mode = 0644
    oidcshim.kemo.dev/file-gcp-0: |
      { "universe_domain": "googleapis.com", "type": "external_account", ... }
  labels:
    oidcshim.kemo.dev/shim-gcp: namespaced
spec:
  initContainers:
  - name: oidcshim-init-gcp        # runs to completion before the app starts
    args: ["-config", "/etc/oidcshim/helper.conf", "-daemon-mode=false"]
  - name: oidcshim-refresh-gcp     # restartPolicy: Always -> native sidecar
    args: ["-config", "/etc/oidcshim/helper.conf"]
  containers:
  - name: my-app
    env:
    - name: GOOGLE_APPLICATION_CREDENTIALS
      value: /var/run/secrets/oidcshim/gcp/key.json
    volumeMounts:
    - name: oidcshim-token-gcp
      mountPath: /var/run/secrets/oidcshim/gcp
      readOnly: true
    - name: oidcshim-config-gcp
      mountPath: /var/run/secrets/oidcshim/gcp/key.json
      subPath: files/0
      readOnly: true
  volumes:
  - name: spiffe-workload-api       # csi: csi.spiffe.io, readOnly
  - name: oidcshim-token-gcp        # emptyDir, medium: Memory
  - name: oidcshim-config-gcp       # downwardAPI over the annotations above
```

**6. Smoke test** from inside the app container — the Google SDK and `gcloud` both honour `GOOGLE_APPLICATION_CREDENTIALS`:

```bash
kubectl exec -n my-app deploy/my-app -c my-app -- sh -c \
  'cat $GOOGLE_APPLICATION_CREDENTIALS && gcloud auth print-access-token'
```

A printed access token means SPIRE attested the pod, the JWT-SVID carried the right audience, and Google's STS accepted it. If it fails, check the `oidcshim-refresh-gcp` container logs first (`kubectl logs -n my-app <pod> -c oidcshim-refresh-gcp`), then whether the discovery endpoint is reachable from outside the cluster.

## AWS

Sample: `config/samples/oidcshim_v1alpha1_oidcshim_aws.yaml` (namespaced `OIDCShim`).

The shim requests the audience `sts.amazonaws.com` and writes the token to `/var/run/secrets/eks.amazonaws.com/serviceaccount/token`, the same path the EKS Pod Identity Webhook uses, exporting `AWS_ROLE_ARN`, `AWS_WEB_IDENTITY_TOKEN_FILE`, `AWS_ROLE_SESSION_NAME`, `AWS_REGION` and `AWS_DEFAULT_REGION`. Every AWS SDK picks those up and calls `AssumeRoleWithWebIdentity` on its own — no application changes.

On the AWS side, create an **IAM OIDC identity provider** whose provider URL is your `SpireServer.spec.jwtIssuer` and whose **client ID (audience) list contains `sts.amazonaws.com`**, then attach a role whose trust policy federates to that provider and conditions on the `sub` claim (the workload's SPIFFE ID). Enrol with `oidcshim.kemo.dev/shim: aws` and set `eks.amazonaws.com/role-arn` (required) and optionally `eks.amazonaws.com/region` (defaults to `us-east-1`).

## Azure

Sample: `config/samples/oidcshim_v1alpha1_clusteroidcshim_azure.yaml` (cluster-scoped `ClusterOIDCShim`).

The shim requests the audience `api://AzureADTokenExchange`, writes the token to `/var/run/secrets/azure/tokens/azure-identity-token` and exports `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_AUTHORITY_HOST` and `AZURE_FEDERATED_TOKEN_FILE`, which is exactly what `WorkloadIdentityCredential` in the Azure SDKs reads.

On the Azure side, add a **federated credential** to the app registration with **issuer** = your `SpireServer.spec.jwtIssuer`, **subject** = the workload's **SPIFFE ID** (e.g. `spiffe://example.org/ns/my-app/sa/my-sa`, whatever your `ClusterSPIFFEID` mints) and **audience** = `api://AzureADTokenExchange`. Enrol with `oidcshim.kemo.dev/shim: azure` and set `azure.workload.identity/client-id` and `azure.workload.identity/tenant-id` (both required); `azure.workload.identity/authority-host` defaults to `https://login.microsoftonline.com/`.

## Generic OIDC

`config/samples/oidcshim_v1alpha1_clusteroidcshim_generic.yaml` shows the pieces that are not provider-specific: matching by label selector instead of enrolment, sourcing parameters from a `ConfigMap` in a fixed namespace (`spec.configMapNamespace`, a `ClusterOIDCShim`-only field), a templated audience, and restricting injection to one container with `spec.inject.containers`.

## Annotation and label reference

All keys live under the `oidcshim.kemo.dev` prefix. "You set" keys are inputs; "operator sets" keys are written by the webhook onto the mutated pod and should be treated as read-only.

| Key | Kind | Set on | Who sets it | Meaning |
| --- | --- | --- | --- | --- |
| `oidcshim.kemo.dev/shim` | annotation or label | Namespace, ServiceAccount, Pod | you | Enrols the object into the named shim(s). The **annotation** takes a comma-separated list (`gcp,aws`); a **label** value cannot contain commas, so the label form carries exactly one shim name. Which kinds are consulted is controlled by `spec.selection.enrollment`. |
| `oidcshim.kemo.dev/inject` | annotation | Pod | you | `"false"` (case-insensitive) opts the pod out of injection entirely, before any shim is considered. Any other value is ignored. |
| `oidcshim.kemo.dev/containers` | annotation | Pod | you | Comma-separated list of app container names to inject into. **Overrides `spec.inject.containers`** for every shim applied to that pod. An empty or whitespace-only value falls back to the spec; a name that does not exist on the pod produces a warning, and if *none* of the requested names exist the shim is skipped. |
| `oidcshim.kemo.dev/webhook` | label | Namespace | you | `disabled` excludes the namespace via the webhook's `namespaceSelector`. The operator's own namespace carries it so the operator can always be (re)started. |
| `oidcshim.kemo.dev/status` | annotation | Pod | operator | Set to `injected` once the pod has been mutated. A pod that already carries it is left alone. |
| `oidcshim.kemo.dev/shims` | annotation | Pod | operator | Comma-separated list of the shim keys applied, e.g. `OIDCShim/my-app/gcp,ClusterOIDCShim/azure`. |
| `oidcshim.kemo.dev/helper-conf-<shim>` | annotation | Pod | operator | The rendered `spiffe-helper` HCL config for that shim. The downward-API volume projects it to `/etc/oidcshim/helper.conf`. |
| `oidcshim.kemo.dev/file-<shim>-<i>` | annotation | Pod | operator | The rendered content of `spec.inject.files[i]` for that shim, projected to `files/<i>` in the config volume and mounted at the file's `path`. |
| `oidcshim.kemo.dev/shim-<name>` | label | Pod | operator | `namespaced` for an `OIDCShim`, `cluster` for a `ClusterOIDCShim`. Used by the controller to count `status.matchedPods` and handy for `kubectl get pods -l oidcshim.kemo.dev/shim-gcp`. |

Provider-specific annotations (`iam.gke.io/*`, `eks.amazonaws.com/*`, `azure.workload.identity/*`) are not owned by this operator — they are just the keys the sample shims happen to read, and you can point `spec.parameters[].valueFrom.annotation` at anything you like.

## Parameters

`spec.parameters` is an ordered list. Each entry names a template variable and a single source: `static`, `annotation`, `label`, `configMapKeyRef` or `template`. A `template` parameter may reference any parameter defined **before** it, plus the built-ins.

### Parameter precedence

For `annotation` and `label` sources, the key is looked up on the **Pod**, then its **ServiceAccount**, then its **Namespace** — first object where the key is *present* wins, **even if its value is empty**. Setting an annotation to `""` on a Pod therefore deliberately masks the Namespace's value rather than falling through to it.

If no object carries the key:

1. `default` is used if set (including an empty-string default);
2. otherwise, if `required: true`, the whole shim is **skipped for that pod** with an admission warning — the pod is still created;
3. otherwise the value is the empty string.

A `template` parameter that renders to an **empty string counts as "no value"**, so `default` and `required` apply to it exactly as they do to a missing annotation.

### Template built-ins

Every shim's templates (parameter templates, `spec.audience`, `spec.extraAudiences`, `spec.inject.env[].value`, `spec.inject.files[].content`) are Go `text/template` with `missingkey=error` and no functions — referencing an undefined key is an error that skips the shim rather than rendering `<no value>`. Alongside the resolved parameters, these are always available:

| Built-in | Value |
| --- | --- |
| `.podNamespace` | The namespace the pod is being created in. |
| `.serviceAccountName` | `spec.serviceAccountName` of the pod, or `default` when unset. |
| `.shimName` | `metadata.name` of the shim. |
| `.tokenDir` | Resolved token directory, default `/var/run/secrets/oidcshim/<shim>`. |
| `.tokenPath` | `<tokenDir>/<fileName>`, default `/var/run/secrets/oidcshim/<shim>/token`. |

Parameters may not reuse a built-in name.

## Manager flags and environment variables

Flags are registered on the manager binary (`internal/config`); environment variables are read first, so a flag always wins over an env var. Set flags via `spec.template.spec.containers[0].args` on the Deployment.

| Flag | Env var | Default | Purpose |
| --- | --- | --- | --- |
| `--spiffe-helper-image` | `SPIFFE_HELPER_IMAGE`, then `RELATED_IMAGE_SPIFFE_HELPER` | `ghcr.io/spiffe/spiffe-helper:0.10.0` (the OpenShift overlay sets `RELATED_IMAGE_SPIFFE_HELPER` to the Red Hat build) | Image for both injected helper containers. Overridable per shim with `spec.helper.image`. |
| `--spiffe-helper-image-pull-policy` | — | `IfNotPresent` | One of `Always`, `IfNotPresent`, `Never`. Per shim: `spec.helper.imagePullPolicy`. |
| `--spiffe-csi-driver` | — | `csi.spiffe.io` | CSI driver providing the Workload API socket. Per shim: `spec.spiffe.csiDriver`. |
| `--spiffe-socket-mount-path` | — | `/spiffe-workload-api` | Where the socket volume is mounted **in the helper containers**. Per shim: `spec.spiffe.socketMountPath`. |
| `--spiffe-socket-file` | — | `spire-agent.sock` | Socket file name inside that path. Per shim: `spec.spiffe.socketFile`. |
| `--ztwim-namespace` | — | `zero-trust-workload-identity-manager` | Always excluded from injection. |
| `--excluded-namespaces` | — | *(empty)* | Extra exact namespace names to exclude, comma-separated. The operator's own namespace (`POD_NAMESPACE`) and `--ztwim-namespace` are always excluded on top of this. |
| `--excluded-namespace-prefixes` | — | `kube-` | Comma-separated namespace name prefixes to exclude. |
| `--max-rendered-bytes` | — | `65536` | Cap on the rendered helper config plus all rendered files for one shim. Pod annotations are the transport, so this keeps a shim from producing an over-large pod object. |
| — | `POD_NAMESPACE` | *(from the downward API)* | The operator's own namespace; excluded from injection. |

Standard kubebuilder flags also apply: `--leader-elect`, `--health-probe-bind-address`, `--metrics-bind-address`, `--metrics-secure`, `--webhook-cert-path`, `--webhook-cert-name`, `--webhook-cert-key`, `--enable-http2`, plus the zap logging flags (`--zap-log-level`, `--zap-devel`, …).

## Failure policy and safety

The webhook is registered with **`failurePolicy: Fail`**, `sideEffects: None`, `matchPolicy: Equivalent`, `timeoutSeconds: 10`, `reinvocationPolicy: IfNeeded`, and fires only on pod **CREATE**. Fail-closed is deliberate: silently creating a workload without its credentials is worse than not creating it. To keep that from turning into a cluster outage:

- the manager runs **2 replicas** behind a **PodDisruptionBudget** (`minAvailable: 1`), so a rollout or node drain never empties the webhook endpoint;
- the `MutatingWebhookConfiguration` carries a **`namespaceSelector`** that skips `kube-system`, `kube-public`, `kube-node-lease`, any namespace labelled `openshift.io/run-level` `"0"` or `"1"`, and any namespace labelled `oidcshim.kemo.dev/webhook: disabled` — including the operator's own namespace, so it can always restart itself;
- the manager additionally refuses to touch namespaces matching **`--excluded-namespace-prefixes`** (default `kube-`), `--excluded-namespaces`, its own namespace and the ZTWIM namespace, as a second line of defence in case the `namespaceSelector` patch was lost.

Beyond that, nothing about a *single* misconfigured shim blocks pod creation. A shim that fails validation (`Ready=False`, reason `InvalidSpec`), fails selector compilation, is missing a required parameter or fails to render is skipped with an **admission warning** and the pod is admitted without it. The same is true at apply time:

- an **environment variable the container already defines** is left untouched, with a warning — the shim still applies;
- a **volume, volume mount or init-container name collision** skips **that whole shim** with a warning, because half-injecting it would leave the app pointing at a token nobody refreshes;
- an existing `spiffe-workload-api` volume is reused only if it is a CSI volume with the same driver; otherwise the shim is skipped.

Only a genuine API failure (the namespace or ServiceAccount cannot be read, the shim list cannot be fetched) fails the admission request.

Warnings surface in `kubectl` output on creation and in the API server audit log; the manager also logs one line per admitted pod at verbosity 1 (`--zap-log-level=debug`).

## Known limitations

- **JWT-SVID only.** X.509-SVID delivery (certificate/key pairs, `cert_file_name`/`key_file_name`) is not implemented. Every shim is a JWT-SVID shim.
- **Injection happens at pod CREATE only.** Existing pods are never mutated. After creating or changing a shim you must recreate the pods (`kubectl rollout restart`) for it to take effect — **the operator does not restart pods**, and it does not update or remove injected content from pods that are already running.
- **Changing a shim does not re-render running pods.** The rendered helper config and files live in the pod's own annotations, so they are frozen at admission time.
- **Secrets cannot be a parameter source.** Only `static`, `annotation`, `label`, `configMapKeyRef` and `template` are supported. Rendered content is stored in pod annotations, which are readable by anyone who can read the pod, so Secret-sourced values would leak.
- **No provider presets.** `spec.provider` is an informational hint only; it does not preconfigure audiences, paths or environment variables. Everything comes from the shim spec — start from `config/samples/`.
- **Label enrolment is single-valued.** Kubernetes label values cannot contain commas, so multi-shim enrolment requires the `oidcshim.kemo.dev/shim` *annotation*.
- **No conversion or validating webhook.** Spec validation is CRD-level (OpenAPI + CEL) plus the controller's `Ready` condition; an invalid shim is rejected at pod admission time, not at `kubectl apply` time.

## Development

See [DEV.md](DEV.md).
