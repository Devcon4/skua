# skua omp extension

The skua omp extension runs skua remote agents from a local omp session. After login it reads
the cluster's `Ready` profiles from skua-api and writes one task agent, `skua-<profile>`, per
profile. A `task` item that names one of these agents runs as one `AgentRun` in the cluster.
The extension's relay provider mirrors the run into a local child session, so the remote run
sits next to local agents in the same batch and shows up in Agent Hub, `agent://<id>` and
`history://<id>` like any other subagent.

Each remote tool call shows as a `skua_step` tool card. Steers and parent messages reach the
remote agent at its next tool boundary. The run's result comes back as the child's result.

## Requirements

- omp in the range named in `extension/omp-range.json` (`min` inclusive, `maxExclusive`
  exclusive). The extension uses omp's extension provider API, which can change in any
  release.
- A skua endpoint, its OIDC issuer URL and the public client id. The cluster installer has
  these values.
- Node >= 23.6, only for the tooling (npm, the typechecker). omp loads the TypeScript
  directly; there is no build step. The only runtime imports are omp's own `@oh-my-pi/*`
  modules, which omp provides to every extension.

## Install

Add the package directory to `extensions:` in `~/.omp/agent/config.yml`:

```yaml
extensions:
  - ~/code/skua/extension
```

For a trial without changing the config, start omp with `omp -e ~/code/skua/extension`.

The extension writes the agent files to `agents/` inside the package directory, so that
directory must be writable by the user who runs omp.

## First login

```
/skua login <endpoint> <issuer> <clientId>
```

For example, against the kind development cluster:

```
/skua login http://127.0.0.1:32080 http://dex.127.0.0.1.nip.io:32000 skua-omp
```

Login uses the OIDC authorization code flow with PKCE. It opens the issuer's page in a
browser (`$BROWSER`, else `xdg-open`, `open` or `wslview`) and waits on a loopback port.
When no browser starts or the loopback port cannot be bound, it falls back to the device
code flow and shows a URL and a user code to enter on any device. `/skua login --device`
uses the device code flow directly, which suits SSH sessions. The issuer must advertise
`device_authorization_endpoint` for the device flow to work.

The three values are stored and later logins need none of them: `/skua login` alone signs in
again. Passing a different endpoint clears the stored token, the profile cache and the agent
files. After login the extension syncs the profile list and keeps refreshing it in the
background.

## Commands

| Command | What it does |
| --- | --- |
| `/skua login [<endpoint> <issuer> <clientId>] [--device]` | Sign in; the three values are required the first time. |
| `/skua sync` | Refresh the profile list and the `skua-<profile>` agent files now. |
| `/skua list` | List your runs in the cluster, marking those relayed in this omp. |
| `/skua abort <run>` | Cancel a run in the cluster. |
| `/skua status` | Show the endpoint, login, omp version check, profile cache and relayed runs. |

A new profile is dispatchable as soon as `/skua sync` writes its agent file, without
restarting omp. The `task` tool's description lists it only after omp rebuilds the base
prompt, so name the agent explicitly until then.

## Using remote agents in a task batch

Name the profile's agent in a `task` item. Remote and local items mix in one call:

```json
{
  "context": "Shared notes for every item.",
  "tasks": [
    { "id": "Api", "agent": "skua-golang", "task": "Add pagination to the runs endpoint and open a PR." },
    { "id": "Docs", "agent": "scout", "task": "Find where pagination is documented." }
  ]
}
```

The remote agent receives the batch `context` and the item's `task` as its prompt.

When the session's working directory is a git repo with an `origin` remote, the run gets that
repo and the current `HEAD` commit as its workspace. `HEAD` must be on `origin`: when no
remote-tracking ref of `origin` contains it, the item ends at once with a message saying so
and no run starts. Push first, then dispatch. Without an `origin` remote the run has no
workspace and returns text only.

The child's result is the run's result. When the run pushed a branch or opened a pull
request, the result also names `Branch: skua/<run>` and the PR link. A failed run ends the
child with a normal final message, `Remote run <name> failed: <reason>.`

For schema-checked JSON, set `task.structured: true` in your omp config and give the item an
`outputSchema`. The extension sends the schema with the run and returns the remote agent's
validated JSON as the item's structured output. With `task.structured` off, the schema never
reaches the extension.

## Steering, follow-ups and abort

A steer from Agent Hub or a parent's `write agent://<id>` reaches the remote run at its next
tool boundary. A message to a child whose run has finished starts one follow-up run whose
prompt holds the previous result and the new message. This also works after the child
parks.

Esc, an Agent Hub kill, a cancelled subagent and closing omp all detach the local child. The
run keeps going in the cluster and its result stays readable there. To cancel a run, use
`/skua abort <run>`; `/skua list` shows run names.

## Reattach after an omp restart

Start omp again with `omp --resume <session id>` and send the child a message, for example
`write agent://<id>`. The extension finds the child's run, resumes its event stream from the
last relayed step, and delivers the message as a steer while the run is live, or as a
follow-up once it has ended. No second run starts for the same item.

## State

The extension keeps its state in `<agent dir>/skua/`. The agent dir is `~/.omp/agent` unless
`PI_CODING_AGENT_DIR` moves it.

| File | Contents |
| --- | --- |
| `config.json` | Endpoint, issuer, client id and tuning values (`scopes`, `loopbackPort`, `heartbeatTimeoutMs`, `profileRefreshMs`). |
| `token.json` | The OIDC token cache, mode 0600. |
| `profiles.json` | The last profile list, read at startup so omp never waits on the network. |

The directory is mode 0700. Deleting it logs you out and forgets the endpoint.

## omp version warning

At startup the extension compares omp's version with `extension/omp-range.json`. Outside that
range it shows `skua: omp <version> is outside the tested range ...` once and keeps working,
but remote runs may break in ways the tests would have caught.

Install a version inside the range, for example with `mise install` from the repo root, which
installs the `min` version that `mise.toml` pins. To support a newer omp, raise `maxExclusive`
in `extension/omp-range.json` and run `make test-extension OMP_BIN=<path to that omp>`. Keep
the change only when the suite passes.

## Tests

From the repo root, with Node >= 23.6 and omp installed. The tests run under the Bun that
ships inside omp (`BUN_BE_BUN=1 omp test`), the same runtime that loads the extension, and
resolve `@oh-my-pi/*` from `extension/dev/node_modules` through the `extension/node_modules`
link that `make extension-deps` creates. `OMP_BIN` defaults to `omp` on `PATH`.

| Command | What it runs |
| --- | --- |
| `make test-extension OMP_BIN=$(command -v omp)` | Typecheck, unit tests, and the RPC suite that drives the extension inside `omp --mode rpc` against a fake skua-api and a fake OIDC issuer. Test files run in parallel workers and the tests inside a file run concurrently, so every test owns its fakes, temp dirs and omp process. |
| `make extension-check OMP_BIN=$(command -v omp)` | Typecheck and unit tests only. |
| `make test-extension-kind OMP_BIN=$(command -v omp)` | One login and one relayed run against the kind development cluster. |

The tests need no provider API key, no network host other than `127.0.0.1` (kind aside) and
no stored omp credentials. Before the root Makefile exists, run the same targets as
`make -f extension/extension.mk <target>`.
