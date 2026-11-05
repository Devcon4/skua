# Installing skua

Read these pages in order:

1. [prerequisites.md](prerequisites.md): what the cluster needs before skua: Kubernetes version, NetworkPolicy, agent-sandbox, gVisor, node disk, LiteLLM, the OIDC client, the GitHub App and ingress.
2. [install.md](install.md): install skua with its Helm chart, from picking a release to the first profile, upgrades and uninstall.
3. [configuration.md](configuration.md): every chart value and every key of skua's `config`, with defaults and what an invalid value does.
4. [flux.md](flux.md): the same install with Flux, from the worked example in [examples/flux/](examples/flux/).

[examples/](examples/) holds the files these pages use: chart values, the first `AgentProfile`, the LiteLLM config, the GitHub ruleset, the gVisor RuntimeClass and the Flux objects.
