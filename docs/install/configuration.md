# Configuration reference

This page lists every value of the skua Helm chart. Values outside `config` shape the Kubernetes objects the chart renders. Values under `config` are skua's own configuration.

## How the configuration reaches skua

The chart renders `config` as YAML into ConfigMap `skua-config`, key `config.yaml`, in the release namespace. Both control-plane Deployments, `skua-operator` and `skua-api`, read it from env `SKUA_CONFIG` through a `configMapKeyRef`. Neither Deployment mounts a volume.

Each Deployment's pod template carries a `checksum/config` annotation with the hash of the rendered config, so any change to `config` rolls both pods on `helm upgrade`.

## Invalid values

An invalid value fails in one of two places:

- **Install.** Helm checks the values against the chart's `values.schema.json` on `helm install`, `helm upgrade` and `helm template`, and fails with an error that names the key. The schema rejects unknown keys at every level except the free-form maps (`resources`, `nodeSelector`, `affinity`, `podAnnotations`, the `annotations` maps and every `podLabels`), missing required keys, wrong types, malformed CIDRs and durations, and out-of-range ports.
- **Startup.** The `skua` binary parses `SKUA_CONFIG` strictly at startup, fills defaults and validates the rest. On an error it logs `config:` followed by every invalid key and exits, so the pod restarts until the config is fixed. `kubectl -n skua logs deployment/skua-operator` shows the message. The checks that only the binary makes are listed in the tables below.

## Image and pods

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `image` | object | | The `skua` image for both control-plane Deployments and, unless `config.runner.image` is set, the runner init container in every sandbox pod. |
| `image.repository` | string | `ghcr.io/devcon4/skua` | Image repository. Required; an empty string fails the schema. |
| `image.tag` | string | `""` | Image tag. Empty means the chart's `appVersion`. Ignored when `image.digest` is set. |
| `image.digest` | string | the release's digest | `sha256:` and 64 lowercase hex characters. When set, the image is `<repository>@<digest>` and the tag is not used. Released charts carry the digest of the release's image. Any other format fails the schema. |
| `image.pullPolicy` | string | `IfNotPresent` | `Always`, `IfNotPresent` or `Never`. Anything else fails the schema. |
| `imagePullSecrets` | list of `{name}` | `[]` | Pull Secrets for the two control-plane Deployments. Sandbox pods get no pull Secret, so `config.runner.image` and profile images must be pullable without one. |
| `operator` | object | | Pod settings of `skua-operator`. |
| `operator.resources` | object | `{}` | Container requests and limits. |
| `operator.nodeSelector` | map | `{}` | Pod node selector. |
| `operator.tolerations` | list | `[]` | Pod tolerations. |
| `operator.affinity` | object | `{}` | Pod affinity. |
| `operator.podAnnotations` | map | `{}` | Extra pod annotations. |
| `api` | object | | Pod and Service settings of `skua-api`. The Deployment always has one replica and the `Recreate` strategy, because live run events live in one process. |
| `api.resources` | object | `{}` | Container requests and limits. |
| `api.nodeSelector` | map | `{}` | Pod node selector. |
| `api.tolerations` | list | `[]` | Pod tolerations. |
| `api.affinity` | object | `{}` | Pod affinity. |
| `api.podAnnotations` | map | `{}` | Extra pod annotations. |
| `api.service` | object | | Service `skua-api`. Its port is named `http` and targets container port 8080. |
| `api.service.type` | string | `ClusterIP` | `ClusterIP`, `NodePort` or `LoadBalancer`. Anything else fails the schema. |
| `api.service.port` | integer | `80` | Service port, 1 to 65535. |
| `api.service.nodePort` | integer or null | `null` | Fixed node port, used only when the type is not `ClusterIP`. `null` lets Kubernetes pick one. |
| `sandboxNamespace` | object | | Whether the chart owns the sandbox namespace. |
| `sandboxNamespace.create` | boolean | `true` | Creates `config.sandboxNamespace` with the `restricted` Pod Security labels and `helm.sh/resource-policy: keep`, so `helm uninstall` leaves it. With `false`, the namespace must exist before the install. |

## Exposure

`skua-api` serves devs' omp sessions and must be reachable from their machines over HTTPS. Use `ingress` or `httpRoute`. Both send every path to Service `skua-api` port `http`. The proxy must not buffer responses and needs a read timeout far above the 15 s SSE heartbeat; [prerequisites.md](prerequisites.md) gives the settings.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `ingress` | object | | Ingress `skua-api` in the release namespace. |
| `ingress.enabled` | boolean | `false` | Renders the Ingress. |
| `ingress.className` | string | `""` | `ingressClassName`. Empty leaves it unset, so the cluster default class applies. |
| `ingress.annotations` | map | `{}` | Ingress annotations, for example ingress-nginx's `proxy-buffering` and `proxy-read-timeout`. |
| `ingress.host` | string | `""` | Host rule and TLS host. Empty matches every host. |
| `ingress.tls` | object | | TLS settings. |
| `ingress.tls.secretName` | string | `""` | TLS Secret in the release namespace. Empty renders no `tls` block. |
| `httpRoute` | object | | `gateway.networking.k8s.io/v1` HTTPRoute `skua-api` in the release namespace. |
| `httpRoute.enabled` | boolean | `false` | Renders the HTTPRoute. The Gateway API CRDs must be installed. |
| `httpRoute.annotations` | map | `{}` | HTTPRoute annotations. |
| `httpRoute.parentRefs` | list | `[]` | Gateway listeners to attach to. With an empty list the route attaches to nothing. |
| `httpRoute.hostnames` | list of strings | `[]` | Hostnames of the route. |

## config

`config` is required. Its keys are exactly the keys of skua's configuration; the schema rejects any other key and the binary does too.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `config.runNamespace` | string | the release namespace | Namespace of `AgentRun` objects and their per-run Secrets. The chart puts the operator's and `skua-api`'s Roles there. Another value must name an existing namespace. Must be a DNS label. |
| `config.sandboxNamespace` | string | `skua-sandboxes` | Namespace of the `SandboxTemplate`, `SandboxWarmPool` and `SandboxClaim` objects and of every sandbox pod. Must be a DNS label. |

### config.oidc

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `config.oidc` | object | | The OIDC issuer devs log in with. Required. |
| `config.oidc.issuerURL` | string | none, required | Issuer URL. `skua-api` fetches the issuer's discovery document and keys from it, so it must resolve to the same name from devs' machines and from inside the cluster. Missing or empty fails the schema. |
| `config.oidc.audience` | string | none, required | The omp extension's OIDC client id. The issuer puts it in the token audience, and `skua-api` rejects tokens without it. Devs pass the same client id to `/skua login`. Missing or empty fails the schema. |

### config.gateway

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `config.gateway` | object | | The LiteLLM gateway. Required. |
| `config.gateway.baseURL` | string | none, required | LiteLLM's base URL as the operator and sandbox pods reach it, for example `http://litellm.litellm.svc:4000`. The operator mints a per-run key there; agents send inference there. |
| `config.gateway.operatorKeySecret` | object | none, required | The Secret holding the key of the LiteLLM `internal_user` that the operator mints run keys with. The installer creates it; see [install.md](install.md). |
| `config.gateway.operatorKeySecret.namespace` | string | `skua-system` | Namespace of that Secret. The chart puts Role `skua-operator-litellm-key` there with one `get` on the Secret, so the namespace must exist before the install. Startup fails when it equals `config.runNamespace` or `config.sandboxNamespace`. |
| `config.gateway.operatorKeySecret.name` | string | none, required | Secret name. |
| `config.gateway.operatorKeySecret.key` | string | none, required | Key in the Secret's data that holds the LiteLLM key. |
| `config.gateway.egress` | object | none, required | LiteLLM's pods, which sandbox pods may reach. Has `config.gateway.egress.namespace` (required), `config.gateway.egress.podLabels` (required) and `config.gateway.egress.port` (required, 1 to 65535). The sandbox NetworkPolicy allows TCP to that port on pods with those labels in that namespace. |

### config.api

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `config.api` | object | filled by the chart | How sandbox pods reach `skua-api`. |
| `config.api.runnerURL` | string | `http://skua-api.<release namespace>.svc` | URL the runner in each sandbox pod uses for `skua-api`. |
| `config.api.egress` | object | `{namespace: <release namespace>, podLabels: {app.kubernetes.io/name: skua-api}, port: 8080}` | `skua-api`'s pods, which sandbox pods may reach. Same shape as `config.gateway.egress`; `podLabels` is optional. A wrong value cuts runners off from `skua-api`, so every run fails to start. |

### config.github

Leave out the whole block when every profile uses `forge: none`. With the block set, the operator mints one-hour tokens scoped to a run's repo from the App Secret, and the chart creates Role `skua-github-app-secret` in the Secret's namespace with one `get` on that Secret.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `config.github` | object | absent | The GitHub App skua uses. |
| `config.github.appSecret` | object | none, required in the block | The App Secret, which already exists. Its default key names are Flux's, so a Secret from `flux create secret githubapp` works as is. |
| `config.github.appSecret.namespace` | string | none, required | Namespace of the App Secret, for example `flux-system`. Startup fails when it equals `config.runNamespace` or `config.sandboxNamespace`. |
| `config.github.appSecret.name` | string | none, required | Name of the App Secret. |
| `config.github.appSecret.appIDKey` | string | `githubAppID` | Key holding the numeric App ID. |
| `config.github.appSecret.installationIDKey` | string | `githubAppInstallationID` | Key holding the numeric installation ID. |
| `config.github.appSecret.privateKeyKey` | string | `githubAppPrivateKey` | Key holding the App's PEM RSA private key. |
| `config.github.apiURL` | string | `https://api.github.com` | GitHub REST API URL. For GitHub Enterprise Server, `https://<host>/api/v3`. |
| `config.github.gitURL` | string | `https://github.com` | Git host URL that run repos are cloned from and pushed to. |

A missing key or a non-numeric id in the Secret is not a config error. It fails the token mint for each `forge: github` run, and the error names the Secret and the key.

### config.network

The operator gives every sandbox pod a NetworkPolicy. Ingress is denied. Egress is allowed to the internet except the cluster's own ranges and link-local addresses, to cluster DNS, to `config.gateway.egress`, to `config.api.egress` and to each entry of `config.network.extraEgress`. [prerequisites.md](prerequisites.md) explains how to find the ranges.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `config.network` | object | | Cluster ranges and extra egress. Required. |
| `config.network.podCIDRs` | list of CIDRs | none, required | Pod ranges. At least one; IPv4 or IPv6. A malformed CIDR fails the schema. |
| `config.network.serviceCIDRs` | list of CIDRs | none, required | Service ranges. Same rules. |
| `config.network.nodeCIDRs` | list of CIDRs | none, required | Node ranges. Same rules. |
| `config.network.extraEgress` | list | `[]` | More in-cluster targets sandbox pods may reach, each with `namespace` (required), `port` (required) and optional `podLabels`. An entry without `podLabels` allows every pod in that namespace. |

IPv6 egress is opened only when at least one IPv6 range is listed, so a dual-stack cluster must list its IPv6 ranges too.

### config.defaults

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `config.defaults` | object | | Defaults for profiles. |
| `config.defaults.runtimeClassName` | string | `gvisor` | RuntimeClass of sandbox pods whose profile sets no `runtimeClassName`. A RuntimeClass that does not exist keeps the warm pool from starting, and the profile reports `PoolNotStarting`. |

### config.retention

Both values are Go durations such as `24h` or `90m`. A malformed or negative duration fails the schema.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `config.retention` | object | | How long finished runs stay. |
| `config.retention.afterFetch` | duration | `24h` | A finished `AgentRun` is deleted this long after its owner first fetched the result. Startup fails when it is longer than `config.retention.max`. |
| `config.retention.max` | duration | `168h` | A finished `AgentRun` is deleted at most this long after it finished, fetched or not. |

### config.runner

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `config.runner` | object | filled by the chart | The runner injected into every sandbox pod. |
| `config.runner.image` | string | the chart's `skua` image | Image of the init container that copies the runner into each sandbox pod. |
| `config.runner.supportedVersions` | string | `>=<major>.<minor>.0-0 <<major>.<minor+1>.0` from the chart's `appVersion` | Semver constraint on runner versions. `skua-api` refuses a runner outside it, and the run fails with `RunnerUnsupported`. A string that is not a semver constraint fails at startup. |

### config.agentSandbox

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `config.agentSandbox` | object | | Where agent-sandbox runs. |
| `config.agentSandbox.namespace` | string | `agent-sandbox-system` | Namespace of the agent-sandbox controller. The operator reads its Deployments there to check the version range, and the chart gives it `get` and `list` on Deployments there. A wrong namespace makes every profile report `AgentSandboxUnsupported`. |

## Namespaces

| Namespace | Default | Holds | Created by |
| --- | --- | --- | --- |
| Release namespace | `skua` | `skua-operator`, `skua-api`, ConfigMap `skua-config`, Service `skua-api` | `helm install --create-namespace` |
| `config.runNamespace` | the release namespace | `AgentRun` objects and per-run Secrets | the installer when it differs from the release namespace |
| `config.sandboxNamespace` | `skua-sandboxes` | sandbox templates, warm pools, claims, sandbox pods, ServiceAccount `skua-runner` | the chart, unless `sandboxNamespace.create` is `false` |
| `config.gateway.operatorKeySecret.namespace` | `skua-system` | the LiteLLM operator key Secret | the installer |
| `config.github.appSecret.namespace` | none | the GitHub App Secret | the installer or Flux |
| `config.agentSandbox.namespace` | `agent-sandbox-system` | the agent-sandbox controller | the agent-sandbox install |
