# Prerequisites

This page is written from skua's kind tests and from upstream documentation. Nobody has
verified it on a real cluster. Where a number comes from a kind run, the page says so.

skua brings none of the components below. The installer provides each one and passes its
location to skua through the chart's `config` values. [configuration.md](configuration.md)
lists every key; [install.md](install.md) covers the install itself.

## Kubernetes 1.29 or newer

skua's CRDs and agent-sandbox's CRDs use CEL validation rules (`x-kubernetes-validations`),
which are GA from Kubernetes 1.29. agent-sandbox also recommends 1.29 or newer.

CI runs the e2e suite on `kindest/node:v1.29.14` and on the default node image of kind
v0.33.0 (Kubernetes 1.37).

agent-sandbox's Gateway API access path needs Kubernetes 1.31. skua does not use it.

## A CNI that enforces NetworkPolicy

The operator gives every profile's sandbox pods one NetworkPolicy. It:

- denies all ingress;
- allows DNS to pods labelled `k8s-app: kube-dns` in `kube-system`, on UDP and TCP port 53;
- allows LiteLLM (`config.gateway.egress`), `skua-api` (`config.api.egress`) and every
  `config.network.extraEgress` target;
- allows every other IPv4 address except the cluster's pod, service and node CIDRs and
  `169.254.0.0/16` (link-local, including cloud metadata endpoints).

An IPv6 egress rule is rendered only when the CIDR lists hold IPv6 ranges. Without them,
sandbox pods get no IPv6 egress. Check that your DNS pods carry the `k8s-app: kube-dns`
label, or sandbox pods cannot resolve names.

A CNI that ignores NetworkPolicy leaves sandbox pods free to reach every Service and pod in
the cluster. To see which CNI runs, list the DaemonSets and Deployments in `kube-system`:

```sh
kubectl -n kube-system get daemonsets,deployments
```

| What you see | NetworkPolicy |
| --- | --- |
| `calico-node`, `cilium`, `antrea-agent`, `kube-router`, `canal` | enforced |
| `kube-flannel-ds` alone | not enforced; add a policy engine or change CNI |
| `aws-node` | enforced only with the VPC CNI's network policy agent turned on |
| `kindnet` | enforced by recent releases through `kube-network-policies` (skua's kind tests rely on it) |

For any other CNI, check its documentation.

On Cilium, set `socketLB.hostNamespaceOnly=true`. Without it, gVisor pods cannot reach
ClusterIP Services, which includes DNS, LiteLLM and `skua-api`.

Sandbox pods can reach the internet; the policy blocks only the cluster's own ranges. Agents
may read from the web, and they should change nothing outside their sandbox except through
git. skua does not filter hostnames. Plain NetworkPolicies only add allows, so a hostname
allowlist needs your CNI's own policy type, such as Cilium `toFQDNs`.

Collect three sets of ranges. Each list needs at least one CIDR; the config rejects an empty
list.

| Range | Config key | Where to find it |
| --- | --- | --- |
| Pod CIDRs | `config.network.podCIDRs` | The CNI's cluster-wide pool: `kubectl get nodes -o jsonpath='{.items[*].spec.podCIDRs}'` shows per-node slices under node IPAM; Calico keeps pools in `ippools.crd.projectcalico.org`; Cilium cluster-pool mode keeps it in ConfigMap `kube-system/cilium-config` key `cluster-pool-ipv4-cidr`. |
| Service CIDRs | `config.network.serviceCIDRs` | `kubectl get servicecidrs` where the ServiceCIDR API is served (GA in 1.33), or the API server's `--service-cluster-ip-range` flag. |
| Node CIDRs | `config.network.nodeCIDRs` | The subnets holding every node's `InternalIP` (`kubectl get nodes -o wide`), control-plane nodes included. |

Use the cluster-wide ranges, not one node's slice. A range missing from these lists is
reachable from every sandbox pod.

## agent-sandbox v1.0.9 with extensions

skua runs every sandbox through agent-sandbox, core and extensions. It needs all four CRDs
(`sandboxes`, `sandboxtemplates`, `sandboxwarmpools`, `sandboxclaims`) and the controller.
agent-sandbox publishes no Helm repository. Install its CRDs first, then its chart from the
tagged source with the CRDs skipped:

```sh
git clone --depth 1 --branch v1.0.9 https://github.com/kubernetes-sigs/agent-sandbox.git
cd agent-sandbox
git rev-parse HEAD   # b1277676f8bf0e82ea299928997ecf5d696a92f1
kubectl apply --server-side --force-conflicts -f helm/crds/
kubectl wait --for=condition=Established --timeout=60s -f helm/crds/
helm upgrade --install agent-sandbox ./helm -n agent-sandbox-system --create-namespace --skip-crds \
  --set namespace.create=false --set image.tag=v1.0.9 --set controller.extensions=true --wait
```

The chart templates its own Namespace, which conflicts with `--create-namespace`, so
`namespace.create=false` is required.

The release manifest is the other path. It holds the four CRDs and the controller:

```sh
kubectl apply --server-side \
  -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v1.0.9/sandbox-with-extensions.yaml
```

Helm never upgrades the CRDs in a chart's `crds/` directory. Every agent-sandbox upgrade
repeats the CRD step before `helm upgrade`. `make check-agent-sandbox-upgrade` tests that
order in kind, from v1.0.8 to v1.0.9.

skua supports agent-sandbox `v1.0.9 <= v < v1.1.0`. At startup and every five minutes, the
operator reads the four CRDs and the controller Deployments in `config.agentSandbox.namespace`
(default `agent-sandbox-system`). It takes the version from the controller image tag. When a
CRD is missing or the version is outside the range, every `AgentProfile` gets `Ready=False`
with reason `AgentSandboxUnsupported` and a message naming what was found. The operator keeps
running and does not crash-loop. A controller image without a semver tag, or a read error,
leaves profiles as they are and logs a warning. If agent-sandbox runs in another namespace,
set `config.agentSandbox.namespace`; the chart grants the operator `get` and `list` on
Deployments there.

Flux users follow [flux.md](flux.md).

## gVisor RuntimeClass

Sandbox pods run under RuntimeClass `gvisor` unless a profile names another
(`config.defaults.runtimeClassName`, default `gvisor`). The class uses the containerd
handler `runsc`.

Install gVisor on every node that runs sandbox pods, from the release tarball. The tarball
holds `runsc`, `containerd-shim-runsc-v1` and the directory `gvisor-bin/`. Since release
`20261005.0`, `runsc` does not start without the sidecar binaries in `gvisor-bin/`, so copy all three.
skua's kind tests use release `20261102.0`:

```sh
version=20261102.0
base=https://storage.googleapis.com/gvisor/releases/release/$version/x86_64
curl -fsSLO "$base/gvisor.tar.bz2" -fsSLO "$base/gvisor.tar.bz2.sha512"
sha512sum -c gvisor.tar.bz2.sha512
tar xjf gvisor.tar.bz2
install -m 0755 runsc containerd-shim-runsc-v1 /usr/local/bin/
cp -r gvisor-bin /usr/local/bin/
```

Register the handler in containerd (config version 2, as kind uses it) and point it at a
`runsc` config file:

```toml
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runsc]
  runtime_type = "io.containerd.runsc.v1"
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runsc.options]
  TypeUrl = "io.containerd.runsc.v1.options"
  ConfigPath = "/etc/containerd/runsc.toml"
```

A containerd config at version 3 puts the same runtime under
`plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runsc`. gVisor's
[containerd guide](https://gvisor.dev/docs/user_guide/containerd/quick_start/) covers both.

`/etc/containerd/runsc.toml` picks the platform:

```toml
[runsc_config]
  platform = "systrap"
```

`systrap` needs no hardware support. `kvm` needs `/dev/kvm` on the node, which means bare
metal or nested virtualization. Restart containerd after the change.

Then create the RuntimeClass. [examples/runtimeclass-gvisor.yaml](examples/runtimeclass-gvisor.yaml)
is a starting point:

- `scheduling.nodeSelector` keeps sandbox pods on nodes that have `runsc`. Label those nodes
  with the same key (`skua.dev/gvisor: "true"` in the example), or drop the selector if every
  node has gVisor.
- `overhead.podFixed` is added to every sandbox pod's requests for scheduling and quota. It
  accounts for the gVisor sandbox itself. An idle gVisor warm pod used 27 MiB of cgroup
  memory in kind; the example's 64Mi and 100m are a guess, not a measurement.

gVisor's cost against runc on clone, install, build and test work has not been measured on
real nodes. Installers who need numbers measure their own workloads under both runtimes. A
profile can name a different `runtimeClassName`.

Kata Containers is opt-in: create a Kata RuntimeClass and name it in a profile's
`runtimeClassName`. Kata needs `/dev/kvm` on the node.

## Node ephemeral storage and warm pools

Each sandbox pod's workspace at `/work` is an `emptyDir` sized by the profile's
`workspace.size` (default `10Gi`). The operator sets that size as the `emptyDir` size limit
and as the agent container's `ephemeral-storage` request and limit. Size agent node disk as:

```text
sum over profiles of (warmPool.replicas + expected concurrent runs) * workspace.size
```

Leave room on top for images and the kubelet's own reserve.

Under gVisor, workspace writes count as ephemeral storage, not as memory. The kubelet evicts
the pod when the workspace passes its limit, and the run fails. This was measured in kind.
Each pod also has a 64Mi memory-backed volume for skua's runner, which counts toward pod
memory.

A profile with `workspace.storageClassName` gets a generic ephemeral volume of that class
instead: a ReadWriteOnce PVC that requests `workspace.size` and is deleted with the pod. The
operator then sets no `ephemeral-storage` request. Whether the size holds depends on the
storage class; `local-path` enforces no size.

A warm pool keeps `warmPool.replicas` idle pods per profile (default `1`). Its idle cost is
the replica count times the pod's requests plus `overhead.podFixed`. `warmPool.replicas: 0`
is allowed; every run then starts a cold pod. In kind, a cold claim took under 2 s with a
cached image, and an image pull adds its own time.

The `SandboxWarmPool` has a `/scale` subresource, but an autoscaler cannot drive it. The
operator re-applies `spec.replicas` from the profile every 60 seconds with a forced
server-side apply, so a change made through `/scale` is reverted. Change the profile's
`warmPool.replicas` instead.

## LiteLLM

Sandbox pods never hold a provider key. The operator mints one LiteLLM virtual key per run,
limited to the profile's models, budget and `maxRuntime`, and deletes it when the run ends.

### Image

Run
`ghcr.io/berriai/litellm-database:v1.106.6@sha256:7ef0e78fe00ad5ea7ea45666cc1d1f02cb7076aee69091543bf397de52db33ce`,
pinned by digest. LiteLLM 1.107.0 shipped a credential stealer in its PyPI wheels and official
images that read service-account token files and the LiteLLM database
([Microsoft's analysis](https://www.microsoft.com/en-us/security/blog/2026/10/23/inside-the-litellm-1-107-0-supply-chain-compromise-credential-theft-through-python-startup-hooks/)).
Upgrade only to a release that has been public for a few days, and pin the new digest.
LiteLLM signs its images; its
[image verification instructions](https://docs.litellm.ai/docs/proxy/docker_image_security#verify-image-signatures)
are an option for installers. skua does not verify images itself.

### Runtime

LiteLLM needs:

- Postgres for virtual keys.
- Redis once LiteLLM runs more than one replica. Add the Redis keys from LiteLLM's
  [production guide](https://docs.litellm.ai/docs/proxy/prod#redis).
- A service account with `automountServiceAccountToken: false` and no RBAC bindings, so a
  compromised image finds no Kubernetes credential.
- `config.gateway.baseURL` set to LiteLLM's in-cluster URL, and `config.gateway.egress` set
  to its namespace, pod labels and port, so sandbox pods may reach it.

### Settings

[examples/litellm-config.yaml](examples/litellm-config.yaml) shows these settings:

- `general_settings.user_api_key_cache_ttl: 10`. Each LiteLLM replica caches key lookups,
  60 seconds by default, and Redis does not shorten it. With 10, a deleted run key stops
  working on every replica within about 10 seconds.
- `general_settings.forward_client_headers_to_llm_api: false`, so client headers never reach
  an upstream ([LiteLLM issue 42172](https://github.com/BerriAI/litellm/issues/42172)).
- `litellm_settings.upperbound_key_generate_params.max_budget`, at or above the largest
  `limits.budgetUSD` of any profile (default `10`). A mint above the cap fails with HTTP 400.
  The example caps `max_budget` only. In v1.106.6 the caps also fill missing fields on every
  `/key/generate` call, the master key's included, so a `duration` cap would make the
  operator's own key expire at that age. skua sets `duration` on every run key to
  `limits.maxRuntime` plus 5 minutes.
- Caller-chosen key values stay enabled. skua sends its own `key` value on every mint so that
  a retried mint finds the same key. A `custom_key_generate` or `custom_key_policy` hook that
  rejects the `key` field, or `key_generation_settings` that bar the `internal_user` role,
  stops every run.
- One deployment per model name and no order-based model groups
  ([LiteLLM issue 41954](https://github.com/BerriAI/litellm/issues/41954)).
- Every model a profile names must carry an input price. A profile stays not `Ready` with
  reason `ModelUnpriced` otherwise. Catalog models get prices from LiteLLM's cost map; a
  custom deployment sets `input_cost_per_token` and `output_cost_per_token`.

Claude models are configured as `anthropic/` deployments, and omp reaches them through
LiteLLM's `/v1/messages`.

### Operator credential

The operator never holds the master key. Create its credential once: a LiteLLM
`internal_user` and one key for it, limited by `allowed_routes` to the four routes the
operator calls. Run this from a shell that holds the master key in `LITELLM_MASTER_KEY` and
LiteLLM's URL in `LITELLM_URL`. The key value goes from LiteLLM's response straight into the
Secret through stdin:

```sh
kubectl create namespace skua-system
curl -fsS "$LITELLM_URL/user/new" \
  -H "Authorization: Bearer $LITELLM_MASTER_KEY" -H 'Content-Type: application/json' \
  -d '{"user_id":"skua-operator","user_role":"internal_user"}' >/dev/null
key=$(curl -fsS "$LITELLM_URL/key/generate" \
  -H "Authorization: Bearer $LITELLM_MASTER_KEY" -H 'Content-Type: application/json' \
  -d '{"user_id":"skua-operator","key_alias":"skua-operator","allowed_routes":["/key/generate","/key/delete","/key/info","/model_group/info"]}' |
  jq -er .key) &&
  kubectl create secret generic skua-litellm-operator -n skua-system --from-file=apiKey=<(printf %s "$key")
unset key
```

Point `config.gateway.operatorKeySecret` at it: `namespace: skua-system`,
`name: skua-litellm-operator`, `key: apiKey`. The namespace must differ from the run and
sandbox namespaces. The chart grants the operator one `get` on that one Secret.

Run keys may not exceed the operator key's `max_budget`, which the upperbound fills when the
request omits it. Give the `skua-operator` user no budget of its own: LiteLLM counts every
run key's spend against the user that owns it, so a user budget would cap all runs together.

### Web search

With only its LiteLLM key, an agent's omp `web_search` works for catalog Claude
models and GPT-5 or newer through `web/hosted`, which runs the search on the model's own
endpoint. Other models need a search host that sandbox egress reaches. Open internet egress
covers keyless search engines; an in-cluster search host needs a
`config.network.extraEgress` entry.

## OIDC issuer

Devs log in from the skua omp extension, and `skua-api` checks their tokens. Any OIDC issuer
works if it offers:

- A public client (no secret) with the authorization code flow and PKCE `S256`. The
  issuer's discovery document must list `S256` in `code_challenge_methods_supported`; the
  extension refuses to log in otherwise.
- A loopback redirect. The extension listens on `http://127.0.0.1:<port>/callback`. With its
  default `loopbackPort: 0` the port is random on each login, so the issuer must accept any
  port on a loopback redirect. If it matches ports exactly, register one port and have devs
  set `loopbackPort` to it.
- The scopes the extension asks for by default: `openid profile email offline_access`.
- Optionally, the device flow. When the issuer advertises `device_authorization_endpoint`,
  the extension falls back to it when no browser opens.

The issuer URL goes to `config.oidc.issuerURL` and the client id to `config.oidc.audience`.
Tokens carry the client id as their audience.

The issuer URL must resolve to the same name from devs' laptops and from `skua-api`.
`skua-api` fetches the issuer's keys from that URL and requires the token's `iss` to match it
exactly.

## GitHub App (optional)

Omit `config.github` for an install without a forge (profiles with `forge: none`).

skua uses a GitHub App Secret that already exists in the cluster; it creates none. Name it
with `config.github.appSecret.namespace` and `config.github.appSecret.name`. The key names
default to Flux's: `githubAppID`, `githubAppInstallationID` and `githubAppPrivateKey`. The
private key is a PEM RSA key, PKCS#1 or PKCS#8. The namespace must differ from the run and
sandbox namespaces. The chart grants the operator one `get` on that one Secret.

The App needs:

- Repository permissions `Contents: Read and write` and `Pull requests: Read and write`. An
  App installed read-only, as Flux needs it, is not enough: grant write or name another App.
- An installation on every target repository.
- Operator egress to the GitHub API (`config.github.apiURL`, default
  `https://api.github.com`). GitHub Enterprise Server sets `apiURL` and `gitURL`.

The App key never enters a sandbox. For each run, the operator mints a one-hour token for
the run's one repository with only those two permissions, re-mints it every 30 minutes, and
revokes it when the run ends. All runs share the installation's rate limit, at most 12,500
requests an hour.

A token cannot limit which branches it pushes to, and it cannot force review. Every target
repository needs the ruleset in [examples/github-ruleset.json](examples/github-ruleset.json).
Without it, an agent can push to the default branch or merge its own pull request. A
repository admin applies it:

```sh
gh api -X POST repos/<owner>/<repo>/rulesets --input docs/install/examples/github-ruleset.json
```

It targets every branch except `skua/**` and:

- blocks creating, deleting and force-pushing those branches;
- requires a pull request with one approving review, so nothing reaches them by a direct
  push;
- requires approval of the most recent push, and dismisses approvals on a new push.

Every skua pull request has the App as its author, and GitHub does not let an author approve
its own pull request, so a human approves each merge. The ruleset applies to people as well.
Repository admins bypass it. To let other people create branches outside `skua/**`, add
bypass entries for their role or team, never one for the App.

The branch ruleset leaves tags open, and the token's `Contents: write` can push tags. Add a
tag ruleset too:

```json
{
  "name": "skua-tags",
  "target": "tag",
  "enforcement": "active",
  "bypass_actors": [{ "actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "always" }],
  "conditions": { "ref_name": { "include": ["~ALL"], "exclude": [] } },
  "rules": [{ "type": "creation" }, { "type": "update", "parameters": { "update_allows_fetch_and_merge": false } }, { "type": "deletion" }]
}
```

The field names follow GitHub's
[REST rulesets reference](https://docs.github.com/en/rest/repos/rules#create-a-repository-ruleset).
No skua test applies them to GitHub.

## Ingress or Gateway with TLS

Devs' laptops reach `skua-api` through an Ingress or a Gateway API route with TLS, on a name
and certificate the laptops trust. The chart renders either (`ingress` or `httpRoute`
values). Sandbox runners stay inside the cluster and use the Service.

The extension follows each run over Server-Sent Events. `skua-api` sends a heartbeat comment
every 15 seconds, and the extension resumes with `Last-Event-ID` after a drop. The proxy must
not buffer responses, and its read timeout must sit well above 15 seconds. For ingress-nginx:

```yaml
ingress:
  annotations:
    nginx.ingress.kubernetes.io/proxy-buffering: "off"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
```

`make check-ingress-stream` tests a TLS event stream through ingress-nginx in kind. Other
controllers and Gateway implementations need their own equivalents of both settings.

## Install-time permissions

The installer needs cluster-admin. Kubernetes lets a user grant only permissions the user
already holds, and the chart creates:

- ClusterRoles and ClusterRoleBindings `skua-operator` and `skua-api`;
- ClusterRoleBinding `skua-api-auth-delegator` to `system:auth-delegator`, so `skua-api` can
  review the runners' service account tokens;
- the sandbox Namespace, when `sandboxNamespace.create` is true;
- Roles and RoleBindings in the run namespace, the sandbox namespace,
  `config.agentSandbox.namespace`, `config.gateway.operatorKeySecret.namespace` and, when
  `config.github` is set, `config.github.appSecret.namespace`.

skua's CRDs ship apart from the chart and are cluster-scoped too. The namespaces holding
agent-sandbox, the LiteLLM operator key Secret and the App Secret must exist before the
chart is installed.
