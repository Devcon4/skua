# Install with Flux

This page installs agent-sandbox and skua with Flux. It covers the same steps as [install.md](install.md), so read that page first. The two example files are [examples/flux/agent-sandbox.yaml](examples/flux/agent-sandbox.yaml) and [examples/flux/skua.yaml](examples/flux/skua.yaml). They use the Flux APIs `source.toolkit.fluxcd.io/v1`, `kustomize.toolkit.fluxcd.io/v1` and `helm.toolkit.fluxcd.io/v2`.

The repo's CI installs Flux in kind, applies `agent-sandbox.yaml` at v1.0.8, upgrades it to the file as written (v1.0.9), and dry-runs `skua.yaml` against the Flux CRDs. Nothing here is verified on a real cluster.

## Where each object goes

Both files hold two kinds of objects. The sources and Kustomizations go in the path that Flux syncs for the cluster, next to Flux's own `flux-system` objects. Each file ends with a HelmRelease that goes in your fleet repo, the Git repository that GitRepository `flux-system` points at, under `./infrastructure/<name>`:

```text
clusters/<cluster>/agent-sandbox.yaml               GitRepository agent-sandbox, Kustomizations agent-sandbox-crds and agent-sandbox
clusters/<cluster>/skua.yaml                        GitRepository skua, Kustomization skua-crds, OCIRepository skua, Kustomization skua
infrastructure/agent-sandbox/helmrelease.yaml       HelmRelease agent-sandbox
infrastructure/skua/helmrelease.yaml                HelmRelease skua
infrastructure/skua/namespace.yaml                  Namespace skua-system
infrastructure/skua/litellm-operator-key.yaml       the LiteLLM operator key Secret, SOPS-encrypted, or an ExternalSecret
```

`clusters/<cluster>` stands for whatever path your `flux-system` Kustomization applies. Kustomizations `agent-sandbox` and `skua` read `./infrastructure/agent-sandbox` and `./infrastructure/skua` from GitRepository `flux-system`. A directory without a `kustomization.yaml` is applied as a whole.

The HelmReleases sit apart so that Flux creates them only through a Kustomization with `dependsOn`. A HelmRelease in the cluster sync path would be applied at once, before its CRDs exist.

## CRDs before the charts

Each install applies its CRDs through its own Kustomization, ordered before the chart:

1. `agent-sandbox-crds` applies `./helm/crds` from agent-sandbox's Git tag.
2. `agent-sandbox` depends on `agent-sandbox-crds` and applies the agent-sandbox HelmRelease, which installs the chart from `./helm` of the same tag.
3. `skua-crds` applies `./config/crd` from skua's Git tag.
4. `skua` depends on `skua-crds` and `agent-sandbox`, and applies the skua HelmRelease, which installs the chart from the OCIRepository.

Both CRD Kustomizations use `prune: false`, so removing one from Git never deletes the CRDs and with them every object of those kinds. Both HelmReleases set `install.crds: Skip` and `upgrade.crds: Skip`, so Helm never touches CRDs. kustomize-controller applies server-side, as the manual steps do. Each Kustomization sets `wait: true`, so a dependent waits until the objects it depends on are healthy.

The GitRepository objects use `ignore` to fetch only `helm/` from agent-sandbox and only `config/crd/` from skua.

## Fill in the placeholders

In `skua.yaml`:

- `v<version>` in GitRepository `skua` and `<version>` in OCIRepository `skua`: the same skua release, for example `v0.1.0` and `0.1.0`.
- To pin the chart by digest, add `spec.ref.digest` to OCIRepository `skua` with the digest from the chart line of the release's `digests.txt`. Flux then ignores the tag.
- The HelmRelease `values`: the same keys as [examples/values.yaml](examples/values.yaml), described in [configuration.md](configuration.md). Replace every `<...>` value. The example names Flux's App Secret in `flux-system` with Flux's key names `githubAppID`, `githubAppInstallationID` and `githubAppPrivateKey`. Leave out `config.github` for an install with only `forge: none` profiles.

`agent-sandbox.yaml` needs no edits for v1.0.9.

## Secrets

Keep Secret values out of Git in plain text. Two options work with this layout:

- **SOPS.** Write the Secret, encrypt it with `sops --encrypt --encrypted-regex '^(data|stringData)$' --in-place <file>`, and commit the encrypted file. Add decryption to Kustomization `skua` so kustomize-controller decrypts it on apply:

  ```yaml
  spec:
    decryption:
      provider: sops
      secretRef:
        name: sops-age
  ```

- **External Secrets.** Commit an `ExternalSecret` that writes the Secret from your secret store. External Secrets Operator must run on the cluster.

The LiteLLM operator key Secret lives in `skua-system`, the default of `config.gateway.operatorKeySecret.namespace`. Commit Namespace `skua-system` in `infrastructure/skua` too: the chart puts a Role there and Helm fails when the namespace is missing. kustomize-controller applies Namespaces before the other objects of a Kustomization. The operator reads the Secret at startup and restarts until it exists.

The GitHub App Secret is the one Flux already uses, or one made the same way:

```sh
flux create secret githubapp skua-github-app --namespace flux-system --app-id=<app id> --app-installation-id=<installation id> --app-private-key=<path to the .pem file> --export > skua-github-app.yaml
```

Use the numeric `--app-id`, not `--app-client-id`; skua reads the numeric App ID. Encrypt the file with SOPS before committing it. The App needs `contents` and `pull_requests` write; a read-only App that only lets Flux fetch is not enough. [prerequisites.md](prerequisites.md) covers the App.

## Upgrade

**agent-sandbox.** Change the tag in both places in one commit: `spec.ref.tag` of GitRepository `agent-sandbox` and `values.image.tag` of HelmRelease `agent-sandbox`. The CRDs and the chart then come from the same tag. Upgrade to a version inside the range the running skua release supports, or the operator marks every profile `AgentSandboxUnsupported` until skua catches up.

Keep the HelmRelease's chart on the default `reconcileStrategy: ChartVersion`. agent-sandbox bumps its chart version with every tag, so the chart follows the GitRepository tag. `reconcileStrategy: Revision` appends the commit to the chart version, for example `0.1.3+a502928b6eff`. agent-sandbox's chart writes that version into the `helm.sh/chart` label, and the API server rejects the `+`, so the install fails.

**skua.** `dependsOn` orders when Flux creates and updates the HelmRelease object. helm-controller still upgrades the release as soon as the OCIRepository moves to a new tag. Upgrade in two commits:

1. Bump `spec.ref.tag` of GitRepository `skua` and wait for the new CRDs: `flux get kustomizations skua-crds` shows the new revision and `Ready`.
2. Bump `spec.ref.tag` (and `spec.ref.digest`, when set) of OCIRepository `skua`.

Check the result with `flux get helmreleases -n flux-system` and `kubectl get agentprofiles`.
