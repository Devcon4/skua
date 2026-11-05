---
status: accepted
date: 2026-11-03
decided: 2026-11-03
deciders: dev (proposal approval), orchestrator
---

# 0002: LLM gateway

## Context and Problem Statement
Agent pods read untrusted text and run install scripts, so a provider key in a pod could
leak and be spent without limit. Each run needs its own credential with a model list, a
dollar budget and an expiry. Anthropic's terms forbid routing subscription credentials for
others, so cluster agents bill to org API keys.

## Decision Drivers
* Dev ruling: agents bill through a gateway; pods hold virtual keys, never provider keys.
* Dev ruling: LiteLLM is the gateway.
* omp's thinking and cache fields survive native Anthropic Messages.

## Considered Options
* LiteLLM: the open-source build mints keys with `models`, `max_budget` and `duration` and
  records dollar spend per key; two of its releases were compromised in 2026.
* agentgateway: per-key USD budgets and model patterns; Anthropic over Responses is not
  implemented, and a model missing from its cost catalog is charged zero.
* Agent Router (formerly Envoy AI Gateway): budgets are token quotas rather than dollars,
  and it requires Envoy Gateway.
* omp auth-gateway: ships with omp; no dollar budgets, and it exists to pool subscription
  accounts, which Anthropic's terms forbid for shared use.

## Decision Outcome
Chosen option: "LiteLLM", because its open-source build mints a per-run key with a model
list, a dollar budget and an expiry in one call. The operator mints one key per run, limited
to the profile's models, budget and `maxRuntime`, and revokes it when the run ends.

Reversing this means replacing the operator's key mint and revoke and changing the installer
prerequisite.

### Consequences
* Good, because a leaked pod key spends at most one run's budget before it expires.
* Bad, because LiteLLM shipped malicious PyPI releases 1.82.7 and 1.82.8 in March 2026 and
  malicious 1.107.0 container images in October 2026 that targeted service-account files.
  Install docs require a digest-pinned, cosign-verified image and a minimal service account,
  so every upgrade is a manual digest bump.
* Bad, because installers operate LiteLLM's Postgres, plus Redis past one replica.
* Bad, because a gateway bug that drops cache markers raises cost silently, so maintainers
  re-test omp traffic on each supported LiteLLM version.

### Confirmation
A profile whose model LiteLLM does not serve stays not `Ready`. The walking-skeleton e2e
test in kind runs through a per-run key and asserts that a model outside the key's list is
refused and that the key is revoked after the run.

## More Information
Revisit after another LiteLLM compromise, or when agentgateway serves Anthropic over
Responses and refuses unpriced models.

Sources: https://docs.litellm.ai/docs/proxy/virtual_keys,
https://docs.litellm.ai/blog/security-update-march-2026,
https://www.microsoft.com/en-us/security/blog/2026/10/23/inside-the-litellm-1-107-0-supply-chain-compromise-credential-theft-through-python-startup-hooks/,
https://agentgateway.dev/docs/standalone/latest/documentation/llm/cost-controls/budget-limits/per-key/,
https://theagentrouter.ai/docs/next/capabilities/traffic/usage-based-ratelimiting/,
https://code.claude.com/docs/en/legal-and-compliance.
