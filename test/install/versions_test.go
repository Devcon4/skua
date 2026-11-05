//go:build install

package install

const (
	defaultNodeImage       = "kindest/node:v1.29.14@sha256:8703bd94ee24e51b778d5556ae310c6c0fa67d761fae6379c8e0bb480e6fea29"
	agentSandboxRepository = "https://github.com/kubernetes-sigs/agent-sandbox.git"
	oldAgentSandboxVersion = "v1.0.8"
	oldAgentSandboxCommit  = "a502928b6eff4a17013d24e9c5481d79e99bcdd5"
	newAgentSandboxVersion = "v1.0.9"
	newAgentSandboxCommit  = "b1277676f8bf0e82ea299928997ecf5d696a92f1"
	registryImage          = "registry:3@sha256:d4032d4cc323c1731c707931ecada07a53076eac38e3b61ae57533f170f2c6c3"
	ingressChartRepository = "https://kubernetes.github.io/ingress-nginx"
	ingressChartVersion    = "4.15.1"
	ingressImageTag        = "v1.15.1"
	ingressImageDigest     = "sha256:594ceea76b01c592858f803f9ff4d2cb40542cae2060410b2c95f75907d659e1"
)
