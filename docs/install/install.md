# Install skua

This page installs skua on a cluster with its Helm chart. Finish [prerequisites.md](prerequisites.md) first: agent-sandbox, the gVisor RuntimeClass, LiteLLM, the OIDC client and, for `forge: github` profiles, the GitHub App. Every key named here is described in [configuration.md](configuration.md). For a GitOps install with Flux, read [flux.md](flux.md) after this page.

The steps are written from the chart, the kind e2e tests and upstream docs. They are not verified on a real cluster.

## 1. Pick a release

Each release `v<version>` on <https://github.com/Devcon4/skua/releases> has three assets:

| Asset | Contents |
| --- | --- |
| `skua-crds.yaml` | The `AgentProfile` and `AgentRun` CRDs. |
| `skua-<version>.tgz` | The Helm chart, also pushed to `oci://ghcr.io/devcon4/charts/skua`. |
| `digests.txt` | The digest of each pushed artifact. |

`digests.txt` has one line per artifact:

```text
ghcr.io/devcon4/skua:<version>@sha256:<digest>
ghcr.io/devcon4/skua-agent-generic:<version>@sha256:<digest>
ghcr.io/devcon4/charts/skua:<version>@sha256:<digest>
```

```sh
gh release download v<version> --repo Devcon4/skua --pattern digests.txt
```

The released chart has the `skua` image digest in its default values, so the operator, `skua-api` and the runner init container pull `ghcr.io/devcon4/skua` by digest without any value set. The `skua-agent-generic` digest goes into the first profile in step 7. Flux users pin the chart digest as shown in [flux.md](flux.md).

Install the CRDs, the chart and the images of the same release. `skua-api` refuses a runner whose version is outside `config.runner.supportedVersions`, which the chart sets to the chart's own minor version.

## 2. Apply the CRDs

The chart contains no CRDs, so `helm upgrade` never changes them. Apply them before the first install and before every upgrade, with server-side apply. Use one of these two commands:

```sh
kubectl apply --server-side -f https://github.com/Devcon4/skua/releases/download/v<version>/skua-crds.yaml
```

```sh
kubectl apply --server-side -k 'https://github.com/Devcon4/skua//config/crd?ref=v<version>'
```

Check that both CRDs exist:

```sh
kubectl get crd agentprofiles.skua.dev agentruns.skua.dev
```

## 3. Create the Secrets

skua reads two Secrets that the installer owns. The chart creates neither and never writes to them.

**LiteLLM operator key.** [prerequisites.md](prerequisites.md) creates the LiteLLM `internal_user` and its key. Store that key in a namespace of its own. The default is `skua-system`; it must differ from the run namespace (`skua`) and the sandbox namespace (`skua-sandboxes`), or the operator fails at startup. The chart does not create this namespace, and it must exist before the install because the chart puts Role `skua-operator-litellm-key` in it.

```sh
kubectl create namespace skua-system
kubectl -n skua-system create secret generic skua-litellm-operator --from-file=apiKey=<file holding the key>
```

Delete the file afterwards. The Secret name and key go to `config.gateway.operatorKeySecret.name` and `config.gateway.operatorKeySecret.key`. The operator gets one `get` on this one Secret.

**GitHub App Secret.** skua reads an App Secret that already exists, by namespace and name, in Flux's format. Its keys default to Flux's names: `githubAppID`, `githubAppInstallationID` and `githubAppPrivateKey`. The App ID and installation ID must be the numeric ids, and the private key a PEM RSA key. When no such Secret exists yet, Flux's CLI writes one:

```sh
flux create secret githubapp skua-github-app --namespace flux-system --app-id=<app id> --app-installation-id=<installation id> --app-private-key=<path to the .pem file> --export > skua-github-app.yaml
```

Encrypt the file with SOPS or replace it with an External Secrets object before you commit it anywhere. The chart creates Role `skua-github-app-secret` in the Secret's namespace with one `get` on that one Secret. The App key stays in that namespace. Runs get one-hour tokens scoped to their repo. Without GitHub, leave out `config.github` and use only `forge: none` profiles.

## 4. Write the values

Start from [examples/values.yaml](examples/values.yaml) and replace every example value. The file sets:

- `operator.resources` and `api.resources`: requests and limits for the two control-plane pods.
- `ingress`: exposes `skua-api` through ingress-nginx with TLS and the SSE settings (step 6).
- `config.oidc.issuerURL`: the issuer from the OIDC prerequisite. `config.oidc.audience`: the omp extension's client id, which the issuer puts in the token audience.
- `config.gateway.baseURL`: LiteLLM's in-cluster URL. `config.gateway.operatorKeySecret`: the Secret from step 3. `config.gateway.egress`: LiteLLM's namespace, pod labels and port, which sandbox pods may reach.
- `config.github.appSecret`: the App Secret from step 3. Leave out the whole `config.github` block when no profile uses `forge: github`.
- `config.network.podCIDRs`, `config.network.serviceCIDRs`, `config.network.nodeCIDRs`: the cluster's ranges from the network prerequisite. Sandbox pods reach the internet but none of these ranges.
- `config.defaults.runtimeClassName`: the RuntimeClass for profiles that name none, `gvisor` here and by default.
- `config.retention.afterFetch` and `config.retention.max`: how long a finished `AgentRun` and its result stay after the dev fetched it, and at most.

The chart fills `config.runNamespace`, `config.runner.image`, `config.runner.supportedVersions`, `config.api.runnerURL` and `config.api.egress` from the release. Leave them unset.

Check the values against the chart schema before installing:

```sh
helm template skua oci://ghcr.io/devcon4/charts/skua --version <version> --namespace skua -f values.yaml > /dev/null
```

A schema error names the key. Some checks run only in the binary, for example that `config.retention.afterFetch` is not longer than `config.retention.max`; those show up as a startup error in the pod log (step 8).

## 5. Install the chart

```sh
helm upgrade --install skua oci://ghcr.io/devcon4/charts/skua --version <version> --namespace skua --create-namespace -f values.yaml
```

The chart installs:

- Deployments `skua-operator` and `skua-api`, one replica each, in the release namespace. Neither mounts a volume; both read the config from env `SKUA_CONFIG`, which comes from ConfigMap `skua-config`.
- Service `skua-api`, and the Ingress or HTTPRoute when enabled.
- Namespace `skua-sandboxes` with the `restricted` Pod Security labels, unless `sandboxNamespace.create` is `false`.
- ServiceAccounts, ClusterRoles, the `system:auth-delegator` binding for `skua-api`, and Roles in the run, sandbox, agent-sandbox, operator key and App Secret namespaces.

The install needs cluster-admin because of the ClusterRoles and the auth-delegator binding.

## 6. Expose skua-api

Devs' omp sessions reach `skua-api` over HTTPS. Turn on one of:

- `ingress`: set `ingress.enabled`, `ingress.className`, `ingress.host` and `ingress.tls.secretName` (a TLS Secret in the release namespace, for example from cert-manager).
- `httpRoute`: set `httpRoute.enabled`, `httpRoute.parentRefs` (a Gateway listener with TLS) and `httpRoute.hostnames`.

Run events reach omp as Server-Sent Events with a heartbeat every 15 s. The proxy must not buffer responses and must keep a connection open far longer than 15 s without data. For ingress-nginx, [examples/values.yaml](examples/values.yaml) sets the two annotations from [prerequisites.md](prerequisites.md):

```yaml
nginx.ingress.kubernetes.io/proxy-buffering: "off"
nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
```

For a Gateway API implementation, set the equivalent timeout on the Gateway or with `httpRoute.annotations`.

Devs need three values for `/skua login`: the `skua-api` URL, the issuer URL and the client id. [extension/README.md](../../extension/README.md) covers the dev side.

## 7. Create the first profile

An `AgentProfile` is cluster-scoped and names the agent image, its models, its runtime class, its warm pool and the limits of every run. [examples/agentprofile-generic.yaml](examples/agentprofile-generic.yaml) uses the release's generic image. Before applying it, replace:

- `<version>` and `<generic-image-digest>` with the `skua-agent-generic` line of `digests.txt`. Profiles pin images by digest.
- `<gateway-model-name>` with model group names that LiteLLM serves and prices. A model without an input price leaves the profile not ready.
- `forge: github` with `forge: none` when `config.github` is not set.

```sh
kubectl apply -f agentprofile-generic.yaml
```

The operator renders a `SandboxTemplate` and a `SandboxWarmPool` named `skua-generic` in the sandbox namespace. `warmPool.replicas` idle pods run all the time; [prerequisites.md](prerequisites.md) covers their cost and the node disk they need.

## 8. Check the install

```sh
kubectl -n skua rollout status deployment/skua-operator
kubectl -n skua rollout status deployment/skua-api
kubectl get agentprofiles
kubectl get sandboxwarmpools -n skua-sandboxes
```

`kubectl get agentprofiles` shows a `READY` column from each profile's `Ready` condition. `kubectl describe agentprofile <name>` shows the reason and message:

| Reason | Meaning |
| --- | --- |
| `Ready` | The gateway serves and prices every model, and the warm pool is starting or ready. |
| `GatewayUnavailable` | The operator could not read model info from LiteLLM. Check `config.gateway.baseURL` and the operator key Secret. |
| `ModelUnknown` | LiteLLM has no model group of a listed name. |
| `ModelUnpriced` | LiteLLM has no input price for a listed model, so run budgets cannot be enforced. |
| `PoolNotStarting` | The warm pool has had no ready pod for 5 minutes since its template last changed. Check the pods in the sandbox namespace, the image digest and the RuntimeClass. |
| `AgentSandboxUnsupported` | agent-sandbox is missing, lacks its extensions, or runs a version outside `v1.0.9 <= v < v1.1.0`. The message names what the operator found. |

The operator reads the agent-sandbox CRDs and the controller Deployments in `config.agentSandbox.namespace` at startup and every 5 minutes. Outside the supported range it marks every profile `AgentSandboxUnsupported` and keeps running; a fixed agent-sandbox install turns the profiles ready again within a few minutes.

When a control-plane pod does not start, its log names every invalid config key:

```sh
kubectl -n skua logs deployment/skua-operator
kubectl -n skua logs deployment/skua-api
```

## 9. Upgrade

Upgrade in this order:

1. agent-sandbox, when the new skua release supports a different agent-sandbox version: its CRDs server-side first, then its chart, as in [prerequisites.md](prerequisites.md). A Helm-only upgrade leaves agent-sandbox's CRDs at the old version.
2. skua's CRDs of the new release, server-side, as in step 2.
3. `helm upgrade` with the new `--version`, as in step 5.

Any change to `config` changes the `checksum/config` annotation on both Deployments, so `helm upgrade` restarts both pods. `skua-api` holds live run events in memory; a restart makes connected omp sessions resume their streams, and finished runs keep their results.

## 10. Uninstall

Delete the runs first while the operator still runs. Each `AgentRun` carries the `skua.dev/cleanup` finalizer, which the operator removes after it revokes the run's LiteLLM key and releases its sandbox.

```sh
kubectl -n skua delete agentruns --all
kubectl delete agentprofiles --all
helm uninstall skua --namespace skua
```

`helm uninstall` keeps the sandbox namespace (`helm.sh/resource-policy: keep`), the CRDs, the operator key Secret and the App Secret. Delete the sandbox namespace by hand when it is empty. Deleting the CRDs deletes every remaining `AgentRun` and its result:

```sh
kubectl delete crd agentruns.skua.dev agentprofiles.skua.dev
```
