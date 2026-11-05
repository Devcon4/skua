# Developing skua

This page is for people changing skua. Installers start at [docs/install/](install/README.md).

Every test runs on a laptop or a CI runner. No test needs a provider API key, a GitHub App or a
real cluster. Integrations are faked, and kind stands in for the cluster.

## Repo layout

| Path | Contents |
| --- | --- |
| `cmd/skua/` | The one `skua` binary: `operator`, `api`, `runner`, `git-credential`, and the `gh` shim chosen by argv[0]. |
| `api/v1alpha1/` | `AgentProfile` and `AgentRun` types, group `skua.dev`. |
| `internal/contract/` | Types and names two or more components agree on: `wire` (dev and runner HTTP types, SSE, endpoint paths, refusal reasons), `sandbox` (the pod contract) and `config`. |
| `internal/integration/` | Clients of one external system: `kube` (the scheme), `litellm` and `github`. |
| `internal/operator/` | The operator root, its slices `run`, `profile`, `forge`, `health` and `compat`, and the support packages `patch` and `condition`. |
| `internal/api/` | The `skua-api` root, its slices `dev`, `runnerapi` and `watch`, and the support packages `auth`, `hub` and `agentrun`. |
| `internal/runner/` | The in-sandbox runner, the agent adapter port `agent` with the omp adapter `agent/omprpc`, and `apiclient`, `mcp`, `gitcred` and `proc`. |
| `internal/testenv/` | envtest boot. Only `test/integration/` imports it. |
| `config/` | Generated CRDs and RBAC, the kustomize install (`config/default`) and the kind overlay (`config/kind`). |
| `charts/skua/` | The Helm chart. It ships no CRDs. `ci/` holds the lint values. |
| `images/generic/` | The generic agent image with git, gh, shells and omp. |
| `test/integration/` | Black-box envtest suites: the stack suite with operator, `skua-api` and runner on one envtest, then `operator/`, `api/`, `crd/` (CEL validation) and `sandboxtest/`. Part of `make test`. |
| `test/layout/` | The import-rule test for the layout in `AGENTS.md`. Part of `make test`. |
| `test/fakes/` | Process fakes with their Dockerfiles: `forge`, `llm` (with `scenarios/`) and `omp`. |
| `test/e2e/` | kind acceptance tests (build tag `e2e`) and the fixtures `deploy` applies. |
| `test/kindenv/` | The kind environment as a Go library, with its manifests embedded. |
| `test/install/` | Install checks (build tag `install`) that make their own clusters. |
| `test/pins/` | Fails on an unpinned image, or an omp pin in `mise.toml` that differs from `extension/omp-range.json` `min`. Fixtures in `test/pins/testdata/`. |
| `test/chart/` | Chart render tests. |
| `test/manifests/` | Tests on the kustomize manifests. |
| `tools/kindenv/` | CLI over `test/kindenv`: `up`, `down`, `load`, `logs`, `footprint start` and `footprint stop`. |
| `tools/testbudget/` | Runs `go test -json` for `make test`, prints the slowest tests and the wall time per package, and fails over `TEST_BUDGET`. |
| `mise.toml` | Pinned Go and tool versions, and tasks that call `make`. |
| `extension/` | The omp extension and its tests. See `extension/README.md`. |
| `docs/adr/` | Decision records. |
| `docs/install/` | Installer docs and examples. |
| `.github/` | Workflows and the `kind-prereqs` composite action. |
| `bin/`, `dist/` | envtest assets and build output. Both are ignored by git. |

Where code goes, how tests and fakes are placed, and what `test/layout` enforces are in
`AGENTS.md` under Code conventions.

## Tools

mise installs the pinned tools. Run `mise install` once in the repo root, then either
activate mise in your shell (`mise activate`) or prefix commands with `mise exec --`. It
pins Go, golangci-lint, helm, kind, kubectl, kustomize, flux, shellcheck, zizmor, omp and
controller-gen. setup-envtest and actionlint are `tool` lines in `go.mod` and run through
`go tool`.

Make is the entry point. `mise run <task>` runs the same `make` target for `build`, `lint`,
`test`, `test-e2e`, `kind-up` and `kind-down`, and is optional.

`make test` also needs `git` on `PATH`. The kind environment needs `docker` and `git`, and
`deploy` needs `envsubst`. The extension tests need Node 23.6 or newer for npm and the
typechecker; they run under the Bun bundled in omp.

## Make targets

`make help` prints this list.

| Target | What it does |
| --- | --- |
| `help` | Prints the targets. |
| `generate` | Generates DeepCopy methods for the API types. |
| `manifests` | Generates CRDs into `config/crd/bases`, operator RBAC into `config/rbac` and `skua-api` RBAC into `config/rbac/api`. |
| `fmt` | Fails when gofmt would change a file. |
| `vet` | Runs `go vet`. |
| `lint` | Runs golangci-lint. |
| `test` | Runs unit, envtest and in-process integration tests with `-race` through `tools/testbudget`, and fails when they take longer than `TEST_BUDGET` seconds (default 60). |
| `build` | Builds the static binary `bin/skua`. |
| `docker-build` | Builds the `skua` image. |
| `docker-build-images` | Builds `skua`, the generic agent image and the three e2e fixture images. |
| `kind-up` | Creates or updates the kind cluster `skua-dev`. |
| `kind-down` | Deletes the kind cluster `skua-dev`. |
| `kind-load` | Loads the five images into `skua-dev`. |
| `deploy` | Deploys skua into `skua-dev` with the method `SKUA_DEPLOY` names. |
| `deploy-chart` | Applies the CRD bundle, installs the chart, then applies the e2e fixtures. |
| `deploy-kustomize` | Applies `config/crd` and `config/kind`, then the e2e fixtures. |
| `test-e2e` | Runs the e2e tests in a fresh `skua-dev`. |
| `check-agent-sandbox-upgrade` | Checks the agent-sandbox CRD-first upgrade in its own cluster `skua-r2`. |
| `check-flux-example` | Checks the Flux example in its own cluster `skua-flux`. |
| `check-ingress-stream` | Holds an SSE stream through a TLS ingress for 90 s (past ingress-nginx's 60 s default read timeout) in its own cluster `skua-ingress`. |
| `helm-lint` | Lints the chart strictly with `ci/lint-values.yaml` and `ci/full-values.yaml`. |
| `test-chart` | Runs the chart render tests in `test/chart`. |
| `crds-bundle` | Writes all CRDs to `dist/skua-crds.yaml`. |
| `lint-ci` | Audits the workflows and actions with zizmor and actionlint (with shellcheck on `run:` steps), then runs `test/pins`. |
| `envtest` | Downloads the `ENVTEST_K8S_VERSION` assets into `bin/` through `go tool setup-envtest`. |

`extension/extension.mk` adds `print-omp-version`, `extension-typecheck`, `extension-check`,
`test-extension` and `test-extension-kind`. They are described in `extension/README.md` and
are not in `make help`.

## kind environment

`make kind-up` runs `go run ./tools/kindenv up`. It creates cluster `skua-dev` with kubeconfig
`/tmp/skua-dev.kubeconfig` and installs only what an installer would provide:

- gVisor in the nodes and RuntimeClass `gvisor`.
- agent-sandbox with its extensions in namespace `agent-sandbox-system`.
- Dex as the OIDC issuer at `http://dex.127.0.0.1.nip.io:32000`, with test users in Secret
  `dex/dex-test-users`.
- LiteLLM with Postgres in namespace `gateway`, reachable from the host at
  `http://127.0.0.1:32040`, and the operator key in Secret `skua-system/skua-litellm-operator`
  key `apiKey`.
- A fake GitHub App Secret `flux-system/skua-test-github-app` with Flux key names, a decoy
  Secret beside it, and the matching public key in Secret `skua-e2e/forge-app`.
- Container `skua-e2e-external` on the kind network as the internet egress target.

It writes the cluster's pod, service and node CIDRs and the external target URL to
`/tmp/skua-dev.env`, which `deploy` reads, and a JSON state file that `footprint` reads.
Running it again updates the cluster in place. `make kind-load` and `make kind-down` call
the same CLI. `go run ./tools/kindenv` with no arguments prints its commands, and
`go run ./tools/kindenv up -h` prints the flags.

| Variable | Default | Effect |
| --- | --- | --- |
| `SKUA_KIND_NODE_IMAGE` | kind's default | Node image, such as the 1.29 floor `kindest/node:v1.29.14`. |
| `SKUA_GVISOR` | `auto` | `runsc` fails when the gVisor smoke pod fails. `runc` maps `gvisor` to runc. `auto` tries runsc and falls back to runc. |
| `SKUA_GVISOR_PLATFORM` | `auto` | runsc platform. `auto` picks `kvm` when the node has `/dev/kvm`, else `systrap`. |
| `SKUA_KIND_CACHE` | `~/.cache/skua-kind` | Download cache for gVisor and the agent-sandbox source. |

Every kind and kubectl command names its kubeconfig, for example
`kubectl --kubeconfig /tmp/skua-dev.kubeconfig get agentruns -A`. Nothing reads
`~/.kube/config`.

## Test layers and budgets

A behaviour is tested at the lowest layer that can show it:

1. Unit tests beside the code, with no API server and no network beyond `httptest`.
2. Black-box envtest suites in `test/integration/`. The stack suite runs the operator,
   `skua-api` and the runner on one envtest, with the fakes below and one fake clock. The
   `operator/` suites start one controller each through its slice's `Setup`, and the `api/`
   suites drive `skua-api` through `api.New`.
3. kind acceptance tests in `test/e2e/`, only for behaviour that needs a real component:
   gVisor, NetworkPolicy, the kubelet, TokenReview, Dex, LiteLLM or the agent-sandbox
   controllers.

Budgets on a dev box:

| Suite | Budget |
| --- | --- |
| `make test` | 60 s wall, enforced by `tools/testbudget` |
| `make test-extension` | 60 s wall |
| `make test-e2e` | about 90 s of test time after the cluster is up |

Tests wait on injected clocks and durations, never on real timeouts. A test that cannot
meet its budget is redesigned, or deleted when another layer covers the behaviour. There
are no long-test switches and no scheduled jobs.

## e2e tests

`make test-e2e` runs `kind-up`, `docker-build-images`, `kind-load` and `deploy`, then
`go test -tags e2e -timeout 20m -parallel 4 ./test/e2e/...`, and deletes the cluster when
it finishes. Every e2e test calls `t.Parallel`. A test that changes a profile, or needs a
warm pool no other test claims from, makes its own profile with `tempProfile`.

| Variable | Default | Effect |
| --- | --- | --- |
| `SKUA_DEPLOY` | `helm` for `test-e2e`, `kustomize` for a bare `make deploy` | Install path under test. |
| `E2E_PARALLEL` | `4` | How many e2e tests run at once. |
| `E2E_ARGS` | empty | Extra `go test` flags, such as `-run TestLogin -v`. |
| `E2E_KEEP` | empty | `1` keeps `skua-dev` up afterwards. Delete it with `make kind-down`. |

To iterate on a kept cluster, rerun `make docker-build-images kind-load deploy` and then
`KUBECONFIG=/tmp/skua-dev.kubeconfig go test -tags e2e -count=1 -run <Test> ./test/e2e/...`.

## Fakes

Each integration sits behind an interface, and tests use a fake for it. Fakes take a
`k8s.io/utils/clock` clock, so tests move time instead of sleeping. An in-process fake is
package `<pkg>test` beside the package it fakes. A fake that runs as its own process lives in
`test/fakes/<system>/` with its Dockerfile.

| Integration | Fake | Limits it reproduces |
| --- | --- | --- |
| LLM provider behind LiteLLM | `internal/integration/litellm/litellmtest`, served in kind by image `skua-e2e-fakellm` (`test/fakes/llm`) as Deployment `gateway/fakellm` on port 8080, LiteLLM's only upstream. Scenarios in `test/fakes/llm/scenarios/`. | OpenAI chat completions and Anthropic messages, streaming, scripted tool calls and token usage. Per-turn faults: any HTTP status (429, 5xx) for N attempts, a first-byte delay, a stream dropped after N chunks. The kind LiteLLM is real, so key budgets and expiry are LiteLLM's own. |
| LiteLLM admin API | `internal/integration/litellm/litellmtest` | Key alias rules, alias conflicts and keys owned by another user, key expiry on the fake clock, queued errors, outage, per-call delay. |
| GitHub App token minting | `internal/integration/github/githubtest` | Token expiry on the fake clock, queued errors, rate limiting until a given time. |
| GitHub in kind | Fake forge, image `skua-e2e-forge` (`test/fakes/forge`), Deployment `skua-e2e/skua-e2e-forge` | App JWT checked against `skua-e2e/forge-app`, installation token mint and revoke, pull request create and list, git clone and push over HTTP. |
| agent-sandbox controllers in envtest | `internal/contract/sandbox/sandboxtest`, with its own envtest checks in `test/integration/sandboxtest` | Claim binding to warm or cold pods, a cold-start delay, pool readiness, and the missing sandbox-name annotation on cold claims. |
| agent-sandbox version | `internal/operator/compat/installed_test.go` with the controller-runtime fake client; `TestManagerCacheWithOperatorRBAC` in `test/integration/operator` runs the version check in envtest with the operator's RBAC | Missing or unserved CRDs, no controller, versions outside the supported range. |
| OIDC issuer | `internal/api/auth/authtest`; `extension/test/fake/oidc.ts`; Dex in kind | Expired tokens, tokens from another issuer or signed by an unknown key, any audience, key rotation. |
| Kubernetes TokenReview | `internal/api/auth/authtest` `TokenReviews`; the real API server in kind | Service account tokens bound to a pod and an audience; any other token is unauthenticated. |
| `skua-api` for the runner | `internal/runner/apiclient/apiclienttest` | The runner endpoints over `httptest`, with dropped streams, refusals and failing event and result posts. |
| The agent | `internal/runner/agent/omprpc/omprpctest`, a scripted `omp --mode rpc`, served by image `skua-e2e-fakeagent` (`test/fakes/omp`) as profile `fake` | Behaviour scripted per prompt (`SCRIPT:<name> <args>`), including pushes and pull requests against the fake forge. |
| `skua-api` for the extension | `extension/test/fake/api.ts` | The dev API and SSE stream the extension consumes. |
| Internet | Container `skua-e2e-external`, URL in `/tmp/skua-dev.env` as `SKUA_EXTERNAL_URL` | An egress target outside the cluster CIDRs. |

envtest suites boot through `internal/testenv`, which installs the skua CRDs and the
agent-sandbox CRDs from the module version in `go.mod`. Only `test/integration/` imports it.

A new integration comes with a fake behind the same interface, and the fake reproduces the
real limits that matter to skua: token expiry, budget exhaustion, slow or dropped streams,
409 conflicts and rate limits.

## CI

| Workflow | Trigger | What it proves |
| --- | --- | --- |
| `ci.yml` | pull request, push to `main`, manual | `make generate manifests` leaves the tree clean; build, lint, unit and envtest pass; `helm-lint`, `test-chart`, `crds-bundle` and `lint-ci` pass; the extension tests pass on the pinned omp. |
| `e2e.yml` | pull request, push to `main`, manual | The e2e suite in kind with runsc required. Pull requests run the Helm deploy on kind's default node. Pushes to `main` and manual runs add the kustomize deploy on the 1.29 floor node `kindest/node:v1.29.14`. The job summary records the kind footprint (wall time, peak node memory, runsc platform). |
| `install-checks.yml` | manual, pull requests touching `test/install/`, `test/kindenv/` or `docs/install/examples/flux/` | `check-agent-sandbox-upgrade`, `check-flux-example` and `check-ingress-stream`. |
| `release.yml` | push of a tag `v*.*.*` | Builds and publishes a release. |

`e2e.yml` runs on the runner label in the repository variable `SKUA_E2E_RUNNER`, or
`ubuntu-latest` when it is unset. Set it to a runner with at least 4 CPUs when the default
runner has fewer. No workflow uses a secret other than the job's own `github.token`.

## Releasing

The dev tags a commit on `main` and pushes the tag:

```sh
git tag v0.2.0
git push origin v0.2.0
```

`release.yml` then, for version `0.2.0` and the repository owner in lower case:

1. Builds and pushes `ghcr.io/<owner>/skua:0.2.0` and `ghcr.io/<owner>/skua-agent-generic:0.2.0`
   for `linux/amd64`.
2. Writes `dist/skua-crds.yaml`.
3. Packages the chart with `version` and `appVersion` set to `0.2.0` and the `skua` image
   digest in `image.digest`, and pushes it to `oci://ghcr.io/<owner>/charts/skua`.
4. Writes `digests.txt` with the two images and the chart by digest.
5. Creates GitHub release `v0.2.0` with `skua-crds.yaml`, `skua-0.2.0.tgz` and `digests.txt`.

Releases are not signed. The images carry `org.opencontainers.image.source`, so GHCR links
each package to this repository. After the first release, set the three packages (`skua`,
`skua-agent-generic`, `charts/skua`) to public under the package's settings on GitHub,
once; later pushes keep that visibility.

## Bumping omp

`extension/omp-range.json` holds the supported omp range. `mise.toml` pins omp
(`oh-my-pi`) at its `min`, `ci.yml` tests the extension on that pin, and `test/pins` fails
when the two differ. To support a newer omp, raise `maxExclusive`, run
`make test-extension OMP_BIN=<path to that omp>` and fix what fails. To raise the floor,
change `min` and the `mise.toml` pin together.

## Bumping Go and tools

Tool versions live in `mise.toml`; change the version there and run `mise install`.
setup-envtest and actionlint are bumped with `go get -tool <module>@<version>`. A Go bump
changes `mise.toml`, the `go` line in `go.mod`, and the `golang` builder image in
`Dockerfile` and the three `test/fakes/*/Dockerfile`s, by tag and digest. Run
`make lint-ci` to check the image pins.

## Bumping agent-sandbox

Change these together in one pull request:

- `go.mod`: `sigs.k8s.io/agent-sandbox`. envtest takes its CRDs from this version.
- `internal/operator/compat/installed.go`: `MinAgentSandboxVersion` and
  `MaxAgentSandboxVersion`.
- `test/kindenv/versions.go`: `AgentSandboxVersion` and `AgentSandboxCommit`.
- `test/install/versions_test.go`: the old and new versions and their commits.
- `docs/install/prerequisites.md`: the supported range.
- `docs/install/examples/flux/agent-sandbox.yaml`: the source tag and `image.tag`.

Then run `make test`, `make test-e2e` and the three `check-*` targets. Review
`internal/contract/sandbox/sandboxtest` against the new controller's binding behaviour.

## Not verified

These were left out by the rule that tests need no keys, App or real cluster:

- Real-cluster checks. The install docs are written from kind runs and research, and no
  installer cluster has run them.
- gVisor cost on real nodes. kind numbers measure nested virtualization.
- A real GitHub App and pull request. Only the fake forge is exercised; the agent image
  ships `gh`.
- Provider traffic through LiteLLM: thinking round-trips, prompt cache hits, tool-use
  streaming, the OpenAI Responses API, spend accuracy against provider prices, and the
  `/anthropic/*` passthrough fallback.

## Gotchas

- gVisor's `systrap` platform fails under Docker Desktop on WSL2 with a seccomp `EBUSY`; `tools/kindenv up` picks `kvm` when the node has `/dev/kvm`.
- gVisor releases ship `gvisor.tar.bz2` holding `runsc`, `containerd-shim-runsc-v1` and `gvisor-bin/`; the old per-binary `runsc` URL returns 404.
- The agent-sandbox chart templates its own Namespace, so installing it with `--create-namespace` needs `--set namespace.create=false`.
- The `SandboxTemplate` CRD rejects `metadata.labels` under `ephemeral.volumeClaimTemplate`.
- A cold claim's Sandbox gets no `agents.x-k8s.io/sandbox-name` annotation, so map pods to claims through the `agents.x-k8s.io/claim-uid` label.
- LiteLLM run keys need `key_type: llm_api`.
- LiteLLM returns 200 and changes nothing when a key value is sent again under another alias.
- omp adds an implicit `ollama` provider when something answers on `127.0.0.1:11434`.
