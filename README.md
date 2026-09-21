# OIDC Shims for Zero Trust Workload Identity Manager

The [Zero Trust Workload Identity Manager Operator for OpenShift](https://github.com/openshift/zero-trust-workload-identity-manager/tree/main) provides SPIFFE/SPIRE integration for workloads to access resources from other federated OIDC providers such as AWS IAM, Azure EntraID, and Google Cloud Workload Identity Federation.

However, the tokens generated from the OIDC STS exchanges need to be refreshed regularly, which at first can consist of an initContainer to generate the Token and credentials before the workload consumer starts, and a sidecar to keep the token refreshed.  This can be difficult to manage for multiple applications at scale.

This Kubernetes Operator aims to solve for that with a minimal impact on your application.

By defining an **OIDCShim** or **ClusterOIDCShim**, you can simply annotate or label resources and have the needed token exchange and refresh functions automated for you, similar to how Service Mesh sidecar injection works.
