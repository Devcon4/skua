---
status: accepted
date: 2026-11-03
decided: 2026-11-03
deciders: dev (proposal approval), orchestrator
---

# 0005: GitHub credentials

## Context and Problem Statement
Agents push branches and open PRs with `git` and `gh`. Installers already have a GitHub App
Secret in the cluster, often Flux's. Its private key mints tokens for every repo the App is
installed on until the key is rotated, and agent pods read untrusted issue text. Installation
tokens last one hour, can only be re-minted, and can be narrowed to named repos and
permissions. Runs last hours.

## Decision Drivers
* Dev ruling: reuse the cluster's existing App; skua provisions nothing, and GitHub is
  optional.
* Dev ruling: agents use `gh` and `git`; skua sets up their auth.
* Dev ruling: the App key never enters a pod.

## Considered Options
* Operator-minted installation tokens: one repo for one hour, refreshed over the runner
  channel; skua reads a high-value Secret.
* Mount the App Secret in each pod: no refresh; an injected agent mints tokens for every
  installed repo.
* Fine-grained PATs: each belongs to one person and lives up to a year, and someone must
  rotate it.
* App user-to-server tokens: commits show the dev; skua stores a six-month refresh token per
  dev, and the dev cannot approve their agent's PR.

## Decision Outcome
Chosen option: "Operator-minted installation tokens", because they reuse the installer's App
while keeping its key out of every sandbox. The installer names the existing Secret, with
Flux's key names as the default. The operator mints each run's token for its one repo,
re-mints it before expiry, and revokes it when the run ends.

Reversing this changes the installer contract and the operator's token loop.

### Consequences
* Good, because a leaked token reaches one repo for at most an hour.
* Bad, because a token cannot limit pushes to `skua/*` or force review. Install docs require
  a ruleset on every target repo that blocks the App from other refs and requires an
  approving review; without it an injected agent can merge its own PR.
* Bad, because a read-only Flux App must gain `contents` and `pull_requests` write.
* Bad, because all runs share one installation rate limit, at most 12,500 requests an hour.

### Confirmation
The walking-skeleton e2e test in kind uses a test App in Flux's Secret format on a test repo
with the required ruleset. It passes when a run opens a PR, a push outside `skua/*` is
rejected, and no sandbox pod mounts the App Secret. Nothing checks production rulesets,
because reading them needs a permission the App may lack.

## More Information
Revisit if GitHub adds branch scoping to installation tokens.

Sources: https://docs.github.com/en/rest/apps/apps,
https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api,
https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/about-rulesets,
https://fluxcd.io/flux/components/source/gitrepositories/#github.
