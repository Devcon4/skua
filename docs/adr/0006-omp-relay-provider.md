---
status: accepted
date: 2026-11-03
decided: 2026-11-03
deciders: dev (proposal approval), orchestrator
---

# 0006: Remote runs in omp

## Context and Problem Statement
A dev's local omp session must mix remote items with local agents in one `task` batch, show
each remote run as an Agent Hub row, and steer it while it works. omp has no hook for
external agents or remote task executors. A spike tried omp 18.16.0's extension API against
a fake skua-api.

## Decision Drivers
* D-1: remote runs behave like local subagents in omp.
* D-11: no omp changes.

## Considered Options
* Relay provider plus a passthrough `skua_step` tool and one agent file per profile: each
  remote item is a real local child; it depends on several omp extension seams.
* Plain text relay: same rows, but remote tool calls show as text and a steer waited until
  the run ended.
* Shadowing the built-in `task` tool: full control of the item schema; it reimplements omp's
  async job delivery and must track every change to `task`.
* A separate `skua_run` tool: the smallest surface; no Agent Hub row and no place in a
  `task` batch, so it misses D-1.
* Per-call `model` on one remote agent: one agent file; every dev must enable
  `task.perCallModel` and grant each profile's model in their global omp config.

## Decision Outcome
Chosen option: "relay provider plus passthrough tool", because it was the only option the
spike saw meet D-1, with a steer reaching the remote agent within one remote step.

Reversing this rewrites the extension's relay and profile sync; skua-api is unaffected.

### Consequences
* Good, because Agent Hub, `agent://`, `history://`, steering, parking and revival come from
  omp itself.
* Good, because the cluster knows nothing about omp, so other harnesses can use the same
  HTTP contract.
* Bad, because the relay depends on omp's provider API, task agent discovery and message
  envelope shapes, and omp releases often. Maintainers widen the pinned omp range only after
  the suite passes on a newer release.
* Bad, because a profile an operator adds or removes reaches a dev only at the next sync.
* Neutral, because omp sends one abort signal for Esc, kill and exit, so all of them detach
  and cancelling is an explicit command.

### Confirmation
The RPC acceptance suite drives the extension inside `omp --mode rpc` against a fake
skua-api and fails on any D-1 regression. A scheduled CI job reruns it on the newest omp
release. The extension warns at startup when omp is outside the tested range.

## More Information
Revisit if omp gains external agents or remote task executors.
Usage: `extension/README.md`. Code: `extension/src/relay/provider.ts`, `extension/test/rpc/`.
