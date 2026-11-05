---
status: accepted
date: 2026-11-03
decided: 2026-11-03
deciders: dev (proposal approval), orchestrator
---

# 0004: Control plane storage

## Context and Problem Statement
A run returns text, schema-checked JSON, or git changes up to a PR. The result must wait for
its owner even if the dev's laptop slept through the run's end. A PVC created through
agent-sandbox belongs to the `Sandbox`, so once the pod is gone nothing in skua reads it. A
store inside `skua-api` needs a volume and backups. etcd rejects requests above 1.5 MiB by
default.

## Decision Drivers
* Dev ruling: prefer no artifacts; runs return text or act through git.
* Dev ruling: no volumes for skua; files, if ever needed, go through S3 signed URLs the dev
  machine fetches.
* Dev ruling: sandbox pods may have scratch disks; history after a run matters little.

## Considered Options
* `AgentRun` CRDs plus git: the result lives in `status.result`, capped at `maxResultBytes`
  (default 256 KiB), and larger output goes to the run's branch; a run without a forge
  cannot return large output.
* PVC-backed store in `skua-api`: results, transcripts and files with no cap; `skua-api`
  becomes stateful with its own retention and backup.
* Object storage for every result: no cap; every install needs a bucket and credentials
  before its first run.

## Decision Outcome
Chosen option: "`AgentRun` CRDs plus git", because a capped result fits in the object the
operator already reconciles, and git carries everything larger. `skua-api` and the operator
use no volumes. The agent workspace is scratch that Kubernetes creates and deletes with the
pod. The cluster stores no transcript; the dev's omp session holds the relayed conversation.
An S3 store with presigned URLs is added only if a use case needs files.

Reversing this adds a volume or bucket that operators back up, and a result API outside the
CRD.

### Consequences
* Good, because `skua-api` holds only live events in memory, so a restart or reschedule moves
  no data.
* Good, because a result lasts as long as its `AgentRun`.
* Bad, because losing the pod loses unpushed work; the run fails with `PodLost`.
* Bad, because a `skua-api` restart drops running runs' event history; the extension
  rebuilds from the runner's buffer and the final result.
* Bad, because results add etcd load, so operators who raise `maxResultBytes` stay well under
  the request limit.

### Confirmation
`skua-api` writes no result above the profile's `maxResultBytes`. A chart test fails if the
`skua-api` or operator Deployment declares a volume. The walking-skeleton e2e test in kind
passes only when a research run's schema-valid JSON is read back from `status.result`.

## More Information
Sources: https://etcd.io/docs/v3.5/dev-guide/limit/,
https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/,
https://github.com/kubernetes-sigs/agent-sandbox/blob/main/api/v1beta1/sandbox_types.go.
