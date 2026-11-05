# skua

skua runs long-running coding agents in a shared Kubernetes cluster and lets each dev drive them from local omp. A dev's omp `task` call becomes an `AgentRun` that skua schedules into a gVisor sandbox from a warm pool, with a per-run LiteLLM key and a short-lived repo token. The run streams its progress back to the dev's session and ends with text, schema-checked JSON, or git changes up to a pull request.

## Parts

|Part|Where|What it does|
|---|---|---|
|Operator|`skua operator`, `internal/operator/`|Reconciles `AgentProfile` into agent-sandbox templates and warm pools, and `AgentRun` into a sandbox claim, a LiteLLM key and GitHub tokens; enforces limits and cleanup.|
|skua-api|`skua api`, `internal/api/`|One replica. Dev-facing HTTP API with OIDC; runner-facing API with TokenReview; an in-memory event hub between them.|
|Runner|`skua runner`, `internal/runner/`|Injected into every sandbox as PID 1. Drives the agent (omp in RPC mode), relays events, serves `submit_result` over MCP, and reports the result.|
|omp extension|`extension/`|Registers one omp agent per profile, so `task` spawns remote runs that show in Agent Hub. Usage: `extension/README.md`.|

Decisions are recorded in [docs/adr/README.md](docs/adr/README.md).

## Who does what

An installer puts skua on a cluster once. They install agent-sandbox, gVisor, LiteLLM and the skua Helm chart, connect an OIDC issuer and optionally a GitHub App, and declare one `AgentProfile` per kind of agent: its image, its models, its runtime class, its warm pool size and the limits of every run. The operator turns each profile into a warm pool of idle sandbox pods.

A dev uses skua from omp. They add the omp extension, run `/skua login` once, and get one task agent `skua-<profile>` per ready profile. A `task` item that names one of these agents starts an `AgentRun` in the cluster. skua-api streams the run back into a local child session, where the dev can watch it in Agent Hub, steer it, or abort it. The runner inside the sandbox drives the agent and holds the run's short-lived LiteLLM key and repo token. No provider key and no GitHub App key reaches the sandbox.

## What a run returns

A remote run returns what a local subagent returns:

- a text report;
- JSON checked against the schema the dev passed with the task;
- git changes: a branch `skua/<run>` pushed to the repo, up to an opened pull request, named in the text result.

A run returns no files and mounts no volume from the dev's machine. Anything larger than the result limit goes into git.

## Install

The short path, with the details in [docs/install/README.md](docs/install/README.md):

1. Prerequisites from [docs/install/prerequisites.md](docs/install/prerequisites.md): Kubernetes 1.29 or newer with NetworkPolicy, agent-sandbox v1.0.9 with its extensions, a gVisor RuntimeClass, LiteLLM with an `internal_user` key for the operator, an OIDC client for the omp extension, and optionally a GitHub App.
2. CRDs, which the chart does not ship:

   ```sh
   kubectl apply --server-side -f https://github.com/Devcon4/skua/releases/download/v<version>/skua-crds.yaml
   ```

3. The chart, with values written from [docs/install/examples/values.yaml](docs/install/examples/values.yaml):

   ```sh
   helm upgrade --install skua oci://ghcr.io/devcon4/charts/skua --version <version> --namespace skua --create-namespace -f values.yaml
   ```

4. A first profile from [docs/install/examples/agentprofile-generic.yaml](docs/install/examples/agentprofile-generic.yaml), then the check:

   ```sh
   kubectl get agentprofiles
   ```

Devs then follow [extension/README.md](extension/README.md) to log in and dispatch runs.

## Development

Install the pinned tools with [mise](https://mise.jdx.dev), then use Make:

```sh
mise install
mise exec -- make build test
```

`mise run build` and `mise run test` call the same targets. Code is grouped by component
under `internal/` (`operator`, `api`, `runner`), with shared types in `internal/contract`
and external clients in `internal/integration`. Every target, the test layers and the fakes
are in [docs/development.md](docs/development.md); the layout rules are in
[AGENTS.md](AGENTS.md).

## License

MIT, see [LICENSE](LICENSE).
