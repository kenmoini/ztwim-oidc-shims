# Development

## Prerequisites

- [Go](https://go.dev/doc/install) Version v1.26.0+
- Docker/Podman
- [Kubectl/oc](https://github.com/lmcclint/ocp-version-manager)
- Access to a Kubernetes/OpenShift cluster
- [Operator SDK installed](https://sdk.operatorframework.io/docs/installation/)

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
# OIDCShim
operator-sdk create api --group openshift --version v1alpha1 --kind OIDCShim --namespaced=true --resource --controller
# ClusterOIDCShim
operator-sdk create api --group openshift --version v1alpha1 --kind ClusterOIDCShim --namespaced=false --resource --controller
```

```yaml
# ClusterOIDCShim is simply a Cluster-scoped version of this
apiVersion: openshift.kemo.dev/v1alpha1
kind: OIDCShim
metadata:
  name: abc
  namespace: def
spec:
  # Manual Injection of this OIDCShim's configuration if the Namespace/ServiceAccount/Pod is annotated/labeled
  enrollmentSelectors:
    - target: Namespace
      annotations:
        iam.gke.io/gcp-ztwim-shim: abc
      labels:
        iam.gke.io/gcp-ztwim-shim: abc
    - target: ServiceAccount
      annotations:
        iam.gke.io/gcp-ztwim-shim: abc
      labels:
        iam.gke.io/gcp-ztwim-shim: abc
    - target: Pod
      annotations:
        iam.gke.io/gcp-ztwim-shim: abc
      labels:
        iam.gke.io/gcp-ztwim-shim: abc
  # Automatic Injection based on what's labeled in the environment
  automaticSelectors:
    - target: Namespace
      matchExpressions:
        - key: kubernetes.io/metadata.name
          operator: In
          values:
          - openshift-logging
          - openshift-workload-availability
      matchLabels:
        kubernetes.io/metadata.name: openshift-workload-availability
    - target: ServiceAccount 
      matchExpressions:
        - key: kubernetes.io/metadata.name
          operator: In
          values:
          - gcp-dash
    - target: Pod
      matchExpressions:
        - key: app.kubernetes.io/name: gcp-dash
          operator: In
          values:
          - gcp-dash
  # federatedProvider defines parameters needed for ZTWIM Shims
  federatedProvider:
    provider: google # google, aws, azure, generic
    parameterSources:
      - name: audience
        source: Static # Inspects Namespace, then ServiceAccount, then Pod. Otherwise can be Static/Secret/ConfigMap
        target: 'https://iam.googleapis.com/projects/{{ .projectNumber }}/locations/{{ .workloadIdentityPoolLocation }}/workloadIdentityPools/{{ .workloadIdentityPool }}/providers/{{ .workloadName }}'
      - name: googleServiceAccount
        source: Default # Inspects Namespace, then ServiceAccount, then Pod. Otherwise can be Static/Secret/ConfigMap
        path: annotations["iam.gke.io/gcp-service-account"]
        target: MySecret # When not Default/Namespace/Pod
      - name: projectName
        source: Default # Inspects Namespace, then ServiceAccount, then Pod. Otherwise can be Static/Secret/ConfigMap
        path: annotations["iam.gke.io/gcp-project-name"]
        target: MySecret # When not Default/Namespace/Pod
      - name: projectNumber
        source: Default # Inspects Namespace, then ServiceAccount, then Pod. Otherwise can be Static/Secret/ConfigMap
        path: annotations["iam.gke.io/gcp-project-number"]
        target: MySecret # When not Default/Namespace/Pod
      - name: workloadIdentityPool
        source: Default # Inspects Namespace, then ServiceAccount, then Pod. Otherwise can be Static/Secret/ConfigMap
        path: annotations["iam.gke.io/gcp-wid-pool"]
        target: MySecret # When not Default/Namespace/Pod
      - name: workloadName
        source: Default # Inspects Namespace, then ServiceAccount, then Pod. Otherwise can be Static/Secret/ConfigMap
        path: annotations["iam.gke.io/gcp-workload-name"]
        target: MySecret # When not Default/Namespace/Pod

      - name: workloadIdentityPoolLocation # default value if unset is "global"
        source: Default # Inspects Namespace, then ServiceAccount, then Pod. Otherwise can be Static/Secret/ConfigMap
        path: annotations["iam.gke.io/gcp-wid-pool-location"]
        target: MySecret # When not Default/Namespace/Pod
    exchangeConfiguration:
      exchangeType: token # token or x509
      exchangeVolumeName: spiffe-shim # name of the volume created
      tokenFilePath: /var/run/secrets/gcp/token # path to stored token
      keyFileEnvName: GOOGLE_APPLICATION_CREDENTIALS # If a Environmental Variable needs to be set for the path
      keyFilePath: /var/run/secrets/gcp/key.json
      keyFileFormat: |
        {
          "universe_domain": "googleapis.com",
          "type": "external_account",
          "audience": "//iam.googleapis.com/projects/{{ .projectNumber }}/locations/{{ .workloadIdentityPoolLocation }}/workloadIdentityPools/{{ .workloadIdentityPool }}/providers/{{ .workloadName }}",
          "subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
          "token_url": "https://sts.googleapis.com/v1/token",
          "credential_source": {
            "file": "{{ .tokenFilePath }}",
            "format": { "type": "text" }
          },
          "service_account_impersonation_url": "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/{{ .googleServiceAccount }}:generateAccessToken"
        }

  # Defaults for OpenShift ZTWIM
  spiffeEndpointSocket: unix:///run/spire/sockets/spire-agent.sock
  spiffeVolumes:
    - name: spiffe-workload-api
      csi:
        driver: csi.spifee.io
        readOnly: true
  spiffeVolumeMounts:
    - name: spiffe-workload-api
      readOnly: true
      mountPath: /run/spire/sockets
```