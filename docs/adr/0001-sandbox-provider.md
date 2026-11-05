---
status: accepted
date: 2026-11-03
decided: 2026-11-03
deciders: dev (proposal approval), orchestrator
---

# 0001: Sandbox provider

## Context and Problem Statement
Every skua run gets its own pod, and something has to own that pod's spec, RuntimeClass,
NetworkPolicy and deletion. In agent-sandbox's benchmark a cold pod start took 42 to 50 s
at p50, while a claim on a warm pool adopted a ready pod in about 100 ms. A claim that sets
`env` or volumes skips the pool and cold-starts. agent-sandbox releases weekly, often with
"Action Required" notes.

## Decision Drivers
* Dev ruling: a sandbox provider is required, and it is agent-sandbox behind skua's
  `SandboxProvider` interface.
* Dev ruling: each profile gets its own pool with warm-pool support.
* The agent is pluggable, so the image stays unconstrained.

## Considered Options
* agent-sandbox: templates, warm pools and claims are built in; skua depends on a
  fast-moving project it does not control.
* Operator-owned plain Pods: no extra dependency; skua builds pooling, adoption and cleanup
  itself, and every run cold-starts until it does.
* kagent with Agent Substrate: the closest whole platform; it is alpha on a pre-1.0 runtime,
  and a custom agent image must implement kagent's A2A contract.

## Decision Outcome
Chosen option: "agent-sandbox", because it gives each profile a warm pool without skua
writing pod pooling, and it accepts any image. Each `AgentProfile` renders into one
`SandboxTemplate` and one `SandboxWarmPool`. Each `AgentRun` becomes one `SandboxClaim` with
no per-run env or volumes.

Reversing this means rewriting the `SandboxProvider` implementation and changing the
installer prerequisites.

### Consequences
* Good, because a warm claim binds in about 100 ms.
* Good, because RuntimeClass and NetworkPolicy are set once per profile.
* Bad, because installers install and upgrade agent-sandbox themselves, including a CRD step
  Helm skips.
* Bad, because a weekly agent-sandbox release can break skua between skua releases, so
  maintainers track a supported version range.
* Bad, because warm pods hold their CPU and memory requests while idle; operators size
  `warmPool.replicas` per profile, and 0 is allowed.
* Neutral, because per-run data travels over the runner's connection (ADR-0003).

### Confirmation
The operator checks the installed agent-sandbox version at startup and reports an
out-of-range version instead of crash-looping. The walking-skeleton e2e test in kind passes
only when a run binds a claim on a warm pool and opens a PR. Review rejects per-run env or
volumes on a `SandboxClaim`.

## More Information
Revisit if agent-sandbox supports claim-time env without a cold start, or if Agent Substrate
reaches 1.0 with a contract any agent can meet.

Sources: https://github.com/kubernetes-sigs/agent-sandbox/blob/main/docs/performance-tuning.md,
https://github.com/kubernetes-sigs/agent-sandbox/blob/main/extensions/api/v1beta1/sandboxclaim_types.go,
https://kagent.dev/docs/kagent/1.x/agents/agent-harness/.
