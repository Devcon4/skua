---
status: accepted
date: 2026-11-03
decided: 2026-11-03
deciders: dev (proposal approval), orchestrator
---

# 0003: Agent runner injection

## Context and Problem Statement
Each sandbox pod needs a process that starts the agent, sees every event, delivers steers
mid-run and reports the result. `omp --mode rpc` and ACP both talk over stdio with no
standard network transport, so that process must own the agent's stdin and stdout. A warm
pod is generic until claimed (ADR-0001), so it also waits for its run.

## Decision Drivers
* Dev ruling: the sandbox agent is pluggable (omp, pi, Pi Durable or a custom one).
* Dev ruling: custom images work without anyone setting up the runner.

## Considered Options
* Injected wrapper: an init container copies a static runner into the pod, which runs as the
  agent container's PID 1; any image works, but the image's entrypoint no longer runs.
* Runner baked into each image: no init container; every image author installs and upgrades
  the runner.
* Sidecar runner: the agent container is untouched; a sidecar cannot hold another
  container's stdio.
* MCP server or skill only: an MCP server runs only when the agent calls it, so it cannot
  start the agent, see every event or push a steer.

## Decision Outcome
Chosen option: "Injected wrapper", because it is the only option that owns the agent's stdio
and accepts an image holding only the agent, `git`, `gh` and a toolchain. The agent container
runs `skua-runner run -- <profile command>`. The runner also serves a `skua` MCP server with
`submit_result` and writes a `skua-run` skill into the workspace, for schema-checked results
and skua's git conventions.

Reversing this means every profile image ships the runner and upgrades it apart from skua.

### Consequences
* Good, because a stock image needs no runner, skua config or entrypoint.
* Good, because the runner upgrades with skua's image; `skua-api` refuses runners outside its
  supported version range.
* Bad, because the image's entrypoint is replaced, so authors move entrypoint setup into the
  profile command.
* Bad, because the runner must run on any base image, so it stays a static binary with no
  libc or shell dependency.
* Bad, because the agent shares the runner's container and can read its pod token;
  `skua-api` accepts one connection per pod, opened by the runner before the agent starts.

### Confirmation
The walking-skeleton e2e test in kind runs the `generic` profile with the injected runner and
`omp-rpc`. It passes only when one run opens a PR and one research run returns schema-valid
JSON through `submit_result`.

## More Information
Revisit if omp gains a network transport, which would make a sidecar workable.

Sources: https://agentclientprotocol.com/rfds/streamable-http-websocket-transport.md,
https://kubernetes.io/docs/concepts/workloads/pods/init-containers/.
