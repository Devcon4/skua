Status: approved
Updated: 2026-11-05
Owner: orchestrator

# Proposal: omp extension structure by feature slice

## Recommendation

Apply ADR 0007's categories to `extension/`.

```
src/
  contract/     wire types shared with skua-api
  integration/  skua-api client, an integration like the Go side's LiteLLM client
  support/      auth, profiles and version, shared by the slices
  relay/        slice: the omp provider that relays a task to a remote run
  commands/     slice: one file per /skua subcommand
index.ts        composition root
```

- Each file holds one concern. `relay.ts` (501 lines), `commands.ts` (331), `api.ts` (342) and `oidc.ts` (325) split by concern.
- Slices never import each other. Unit tests move beside the code they test and are named `*.spec.ts`.
- Fakes live in `test/fakes/`, one file per faked system.
- The import rules are review rules; no test enforces the folder layout (D-1).

Behaviour, the omp-facing surface and the set of tests do not change.

Key tradeoff: about 30 files move and about 18 are created by splits. In exchange, the extension follows the same rules and review lens as the Go code.

Calls needed: none (D-1, D-2).

## Problem

- **`src/` is flat.** It holds 13 files. `commands.ts` imports 8 of the 12 other modules, including `relay.ts`, which it imports only for the `LiveRun` type (`commands.ts:10`).
- **Large mixed files:**
  - `relay.ts` (501 lines) holds the provider stream, step and yield id parsing, follow-up numbering, message scanning, error text and output formatting (`relay.ts:63-276`).
  - `commands.ts` (331) holds parsing, every subcommand, the browser opener's `PATH` lookup and formatting (`commands.ts:57-311`).
  - `api.ts` (342) holds requests, retries and the event-stream follow in one closure (`api.ts:96-130`).
  - `oidc.ts` (325) holds discovery, id_token checks, both login flows and refresh.
- **Tests are split by layer, not by the code they test.** `test/unit/` mirrors `src/` from a separate tree. `test/fake/api.ts` is 539 lines, and 5 unit tests import it.
- **Nothing enforces boundaries.**

## Constraints

- (dev) Same principles as the Go restructure (ADR 0007): feature slices, one concern per file, one fake convention, and rules enforced by a test.
- (dev) No behaviour change. omp still loads `./index.ts` (`package.json`).
- (dev) `test-extension` stays under 60 s (today it takes about 20 s).
- (dev) Library-first for protocol and security code: omp's own SSE and OAuth helpers stay.
- (inferred) No runtime dependencies. Dev dependencies go in `extension/dev/`.
- (inferred) The wire types stay hand-written (Go D-12).

## Design

### Target tree and symbol homes

```
extension/
  index.ts                        composition root: wires support and integration into the relay and commands slices
  src/
    contract/wire.ts
    integration/skuaapi/
      client.ts                   createApi, ApiError, request methods
      follow.ts                   followEvents (takes the shared state below as a parameter)
      retry.ts                    sleep, tokenStep, retryAfterMs (shared by client and follow)
      sse.ts
    support/
      auth/      config.ts discovery.ts idtoken.ts login.ts refresh.ts token.ts
      profiles/  cache.ts (cache, readyModels) agents.ts (agent files) files.ts (writeAtomic)
      version.ts
    relay/
      provider.ts                 createRelay, RelayDeps, Relay, LiveRun
      turn.ts                     createTurn
      ids.ts                      STEP_ID, YIELD_ID, followUpOf
      messages.ts                 findAnchor, afterLastAssistant, yieldRejected, contentText, stepResultText,
                                  systemPromptText, yieldData
      errors.ts                   relayErrorText, createErrorText, streamErrorText
      outputs.ts                  outputLines
      inputs.ts  result.ts  workspace.ts
    commands/
      dispatch.ts                 runSkuaCommand, parseSkuaCommand
      deps.ts                     CommandDeps, SkuaServices, NotifyLevel, readConfig, errText, NOT_CONFIGURED,
                                  resetEndpointState, and the LiveRun and VersionCheck shapes it reads
      usage.ts  login.ts  sync.ts  list.ts  abort.ts  status.ts  open-url.ts
      format.ts                   formatRuns, formatAge, profileNames, listed, liveLine
  test/
    fakes/  api.ts  oidc.ts  cli.ts  script-provider/   (was test/fake and test/rpc/script-provider); fakes.spec.ts
    rpc/  kind/                                        suites, unchanged
```

Each existing unit test file moves beside the file that holds its entry point and is renamed `*.spec.ts`. For example, `test/unit/relay.test.ts` becomes `src/relay/provider.spec.ts`. No file gains a test it lacks today. `test/unit/fake.test.ts` becomes `test/fakes/fakes.spec.ts`. The RPC and kind suites keep their `*.test.ts` names.

### Rules

1. `contract` imports nothing from `src/`.
2. `integration` imports `contract` only.
3. `support` packages import `contract` and `integration`, and never a slice.
4. Slices (`relay`, `commands`) import `contract`, `integration` and `support`, and never each other. A slice that needs a shape from another declares it. Only `index.ts` wires things together.
5. Production code never imports from `test/`.
6. One concern per file. Review flags a file over 300 lines.

### Tooling and paths that change

- `extension.mk` unit target: `$(OMP_TEST_FAST) ./extension/src/ ./extension/test/fakes/`. The RPC target adds `./extension/test/rpc/`. `extension-check` and `test-extension` keep their names. Bun's test runner discovers `*.spec.ts` as well as `*.test.ts`.
- `test/rpc/driver.ts:43` points at `../fakes/script-provider/`. `test/fakes/cli.ts` updates the path in its usage text.
- `docs/development.md:185,189`, `extension/README.md:158-159` and ADR 0006 (a link fix to `relay/provider.ts`) update their paths.

## Decisions

- D-1 (dev, 2026-11-05): no test of the folder or import layout; the rules are for review (answers Q1).
- D-2 (dev, 2026-11-05): TypeScript unit tests are `*.spec.ts`. Fakes stay in `test/fakes/`; the dev answered the test-naming half of Q2, and the orchestrator chose the common TypeScript layout for fakes.

## Not doing

- No change to behaviour, the omp-facing surface, the wire format or the commands.
- No OpenAPI generation (Go D-12).
- No runtime dependencies.

## Review log

- **Round 1** (fact check and implementer dry run): 5 findings, all accepted.
  - The layout rule no longer bans fakes in tests.
  - `version` is placed under `support`.
  - Groups are mapped onto ADR 0007's categories, so slices never import each other.
  - The Make commands, `driver.ts:43`, the doc paths and the home of `fake.test.ts` are now stated.
  - A symbol table covers the four split files.
  - The test note now says tests move and none are added.
  - One Problem count is corrected: 5 unit tests import the API fake, not 8.
  - The no-op `tsconfig` bullet is dropped.
