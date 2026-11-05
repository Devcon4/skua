---
name: code-review
description: "Review skua's Go code (operator, skua-api, runner, clients, fakes) and the omp extension for correctness, security and maintainability, using StrixOtus as the operator template. Use when asked to review code, before merging a change set, or after a spec wave lands. Defines the lenses, the reviewer contract, the findings table and how findings are fixed and verified."
argument-hint: "What to review, e.g. 'internal/operator after wave 4' or 'the whole Go tree'"
---

# Code review

A review finds what is wrong with code that already builds and passes its tests. It does not
restyle code, add features or relitigate decisions recorded in `docs/adr/` or a proposal's
`D-` entries. The build gate (`make generate manifests fmt vet lint build test`) runs first;
a review never starts on a red tree.

## Scope

One review covers one slice the reviewer can hold in mind: a package group, not the repo.
The default slices for skua:

| Slice | Paths |
| --- | --- |
| Operator | `internal/operator/...`, `internal/contract/sandbox/`, `api/v1alpha1/`, `config/`, `test/integration/operator/` |
| skua-api | `internal/api/...`, `internal/contract/wire/`, `cmd/skua/api.go`, `test/integration/api/` |
| Runner | `internal/runner/...`, `cmd/skua/runner.go`, `cmd/skua/gitcred.go` |
| Integrations | `internal/integration/...`, `internal/contract/config/` |
| Test doubles | `internal/**/<pkg>test/`, `test/fakes/...`, `test/integration/sandboxtest/`, `test/e2e/` |
| Tooling | `tools/...`, `test/kindenv/`, `test/install/`, `test/pins/`, `test/layout/`, `mise.toml`, `Makefile` |
| Extension | `extension/src/`, `extension/index.ts`, `extension/test/` |

## Lenses

Each slice gets every lens that applies. One reviewer per lens per slice, read-only.

1. **Correctness.** Logic errors, races, leaked goroutines, unclosed bodies, error paths that
   swallow or mis-map errors, off-by-one in time or size limits, retry loops without bounds,
   behaviour that contradicts the spec Contract or the proposal.
2. **Kubernetes operator idioms**, against `/home/dev/code/StrixOtus` (`AGENTS.md` conventions,
   `internal/controller/conditions.go`, `child.go`, one controller and its test):
   level-triggered and idempotent reconciles; status written by merge patch from a deep-copied
   base with an empty diff skipped; conditions with `ObservedGeneration`; finalizers added
   and released safely (NotFound on release is success); RBAC markers minimal and matching
   the calls made; cache scope versus `APIReader`; owner references and garbage collection;
   requeue timing; no work done in watch mappers.
3. **Security.** Credentials: never logged, never in errors, never in a sandbox beyond the
   per-run key and repo token; Secret reads scoped by name; token and key lifetimes and
   revocation on every terminal path; authentication on every skua-api route (OIDC for devs,
   TokenReview plus pod-to-run binding for runners); input bounds (body sizes, result caps,
   names); NetworkPolicy and pod security in rendered objects.
4. **Tests.** Each test fails on a plausible bug; fakes reproduce the real limits they claim
   (expiry, budgets, 409s, dropped streams); no test of wiring or copies; no sleeps where a
   fake clock fits; flakiness (timing windows, shared state, port reuse).
5. **Simplicity and maintainability.** Dead code, duplicated helpers across packages,
   abstractions with one caller, and comments outside `.agents/rules/minimal-comments.md`.
   Control flow, in Go and TypeScript alike: a block nested past three levels; any `else` or
   `else if` (lint catches TypeScript; Go needs review where `revive` does not fire); a
   missing early return or guard clause; a business rule left as an inline condition or a
   loose boolean or string instead of a named predicate, step function or domain type
   (`runIsTerminal`, `tokenNeedsRemint`); names that differ from the domain terms
   (`AgentRun`, `AgentProfile`, claim, runner).
6. **Structure.** What `test/layout` cannot see, against `AGENTS.md` Code conventions:
   a file over 300 lines; more than one concern in a file, or a file not named for its
   concern; a contract string (endpoint path, refusal reason, env name, finalizer, Secret
   key) written twice instead of one exported constant; a new shell script where Go or a
   maintained tool fits.

## Reviewer contract

Paste into every reviewer prompt:

> Read-only. Return at most 8 findings for your lens and slice, most severe first. Zero is a
> valid answer if you list what you checked. Each finding: severity (`blocker`, `major`,
> `minor`), `file:line`, what is wrong, evidence (code quote, a failing input, or the rule it
> breaks with its source), the consequence if left, and the smallest fix. Do not report style
> that gofmt or golangci-lint already enforce. Do not propose features. A finding that
> contradicts a `D-` decision or an ADR is a question for the dev, not a fix.

## Findings and fixes

1. The orchestrator merges reviewer output into one table per review, kept in
   `docs/scratch/<slug>.findings.md` (scratch rules apply): `# | Lens | Sev | File | Finding |
   Evidence | Disposition`.
2. Disposition per row: `fix` (blocker and major by default), `fix-later: <card or Q>`,
   `reject: <reason>` (false positive, or covered by a test the reviewer missed), or
   `ask-dev`.
3. Fixes go to implementers grouped by file so no two touch one file at once. Each fix that
   changes behaviour gets a test that fails before it, unless an existing test already
   covers it.
4. Verification: the build gate again, plus each fixed row's test named in the table. A row
   is `fixed` only with that evidence.
5. A second, narrower pass reviews only the changed lines, with the lenses that raised
   findings. Stop after two passes; anything left becomes `fix-later` or `ask-dev`.
