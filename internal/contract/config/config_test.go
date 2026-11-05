package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const exampleYAML = `runNamespace: skua
sandboxNamespace: skua-sandboxes
oidc: {issuerURL: http://dex.127.0.0.1.nip.io:32000, audience: skua-omp}
gateway:
  baseURL: http://litellm.gateway.svc:4000
  operatorKeySecret: {name: skua-litellm-operator, key: apiKey}
  egress: {namespace: gateway, podLabels: {app: litellm}, port: 4000}
api:
  runnerURL: http://skua-api.skua.svc
  egress: {namespace: skua, podLabels: {app.kubernetes.io/name: skua-api}, port: 8080}
github:
  appSecret: {namespace: flux-system, name: skua-test-github-app, appIDKey: githubAppID, installationIDKey: githubAppInstallationID, privateKeyKey: githubAppPrivateKey}
  apiURL: https://api.github.com
  gitURL: https://github.com
network: {podCIDRs: [10.244.0.0/16], serviceCIDRs: [10.96.0.0/16], nodeCIDRs: [172.18.0.0/16], extraEgress: [{namespace: skua-e2e, podLabels: {app: forge}, port: 8080}]}
defaults: {runtimeClassName: gvisor}
retention: {afterFetch: 24h, max: 168h}
runner: {image: skua:dev, supportedVersions: ">=0.1.0 <0.2.0"}
`

const minimalYAML = `oidc: {issuerURL: http://dex, audience: skua-omp}
gateway:
  baseURL: http://litellm.gateway.svc:4000
  operatorKeySecret: {name: skua-litellm-operator, key: apiKey}
  egress: {namespace: gateway, port: 4000}
api:
  runnerURL: http://skua-api.skua.svc
  egress: {namespace: skua, port: 8080}
network: {podCIDRs: [10.244.0.0/16], serviceCIDRs: [10.96.0.0/16], nodeCIDRs: [172.18.0.0/16]}
runner: {image: skua:dev, supportedVersions: ">=0.1.0 <0.2.0"}
`

func replaced(t *testing.T, doc, old, repl string) string {
	t.Helper()
	out := strings.Replace(doc, old, repl, 1)
	if out == doc {
		t.Fatalf("%q not found in document", old)
	}
	return out
}

func TestParseExample(t *testing.T) {
	c, err := Parse([]byte(exampleYAML))
	if err != nil {
		t.Fatal(err)
	}
	want := &Config{
		RunNamespace:     "skua",
		SandboxNamespace: "skua-sandboxes",
		OIDC:             OIDC{IssuerURL: "http://dex.127.0.0.1.nip.io:32000", Audience: "skua-omp"},
		Gateway: Gateway{
			BaseURL:           "http://litellm.gateway.svc:4000",
			OperatorKeySecret: SecretKeyRef{Namespace: "skua-system", Name: "skua-litellm-operator", Key: "apiKey"},
			Egress:            EgressTarget{Namespace: "gateway", PodLabels: map[string]string{"app": "litellm"}, Port: 4000},
		},
		API: API{
			RunnerURL: "http://skua-api.skua.svc",
			Egress:    EgressTarget{Namespace: "skua", PodLabels: map[string]string{"app.kubernetes.io/name": "skua-api"}, Port: 8080},
		},
		GitHub: &GitHub{
			AppSecret: AppSecret{Namespace: "flux-system", Name: "skua-test-github-app", AppIDKey: "githubAppID", InstallationIDKey: "githubAppInstallationID", PrivateKeyKey: "githubAppPrivateKey"},
			APIURL:    "https://api.github.com",
			GitURL:    "https://github.com",
		},
		Network: Network{
			PodCIDRs:     []string{"10.244.0.0/16"},
			ServiceCIDRs: []string{"10.96.0.0/16"},
			NodeCIDRs:    []string{"172.18.0.0/16"},
			ExtraEgress:  []EgressTarget{{Namespace: "skua-e2e", PodLabels: map[string]string{"app": "forge"}, Port: 8080}},
		},
		Defaults:     Defaults{RuntimeClassName: "gvisor"},
		Runner:       Runner{Image: "skua:dev", SupportedVersions: ">=0.1.0 <0.2.0"},
		AgentSandbox: AgentSandbox{Namespace: "agent-sandbox-system"},
	}
	want.Retention.AfterFetch.Duration = 24 * time.Hour
	want.Retention.Max.Duration = 168 * time.Hour
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("got %+v\nwant %+v", c, want)
	}
}

func TestParseDefaults(t *testing.T) {
	c, err := Parse([]byte(minimalYAML))
	if err != nil {
		t.Fatal(err)
	}
	if c.RunNamespace != "skua" || c.SandboxNamespace != "skua-sandboxes" {
		t.Fatalf("namespaces = %q, %q", c.RunNamespace, c.SandboxNamespace)
	}
	if c.Gateway.OperatorKeySecret.Namespace != "skua-system" {
		t.Fatalf("gateway.operatorKeySecret.namespace = %q, want skua-system", c.Gateway.OperatorKeySecret.Namespace)
	}
	if c.Defaults.RuntimeClassName != "gvisor" {
		t.Fatalf("runtimeClassName = %q", c.Defaults.RuntimeClassName)
	}
	if c.AgentSandbox.Namespace != "agent-sandbox-system" {
		t.Fatalf("agentSandbox.namespace = %q, want agent-sandbox-system", c.AgentSandbox.Namespace)
	}
	if c.Retention.AfterFetch.Duration != 24*time.Hour || c.Retention.Max.Duration != 168*time.Hour {
		t.Fatalf("retention = %v, %v", c.Retention.AfterFetch.Duration, c.Retention.Max.Duration)
	}
	if c.GitHub != nil {
		t.Fatalf("github = %+v, want nil", c.GitHub)
	}
}

func TestParseGitHubDefaults(t *testing.T) {
	doc := minimalYAML + "github: {appSecret: {namespace: flux-system, name: app}}\n"
	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	want := &GitHub{
		AppSecret: AppSecret{Namespace: "flux-system", Name: "app", AppIDKey: "githubAppID", InstallationIDKey: "githubAppInstallationID", PrivateKeyKey: "githubAppPrivateKey"},
		APIURL:    "https://api.github.com",
		GitURL:    "https://github.com",
	}
	if !reflect.DeepEqual(c.GitHub, want) {
		t.Fatalf("github = %+v, want %+v", c.GitHub, want)
	}
}

func TestParseKeepsSetValues(t *testing.T) {
	doc := replaced(t, minimalYAML, "{name: skua-litellm-operator, key: apiKey}", "{namespace: llm-keys, name: skua-litellm-operator, key: apiKey}") +
		"runNamespace: agents\nsandboxNamespace: boxes\ndefaults: {runtimeClassName: kata}\nretention: {afterFetch: 1h, max: 2h}\nagentSandbox: {namespace: sandbox-ctl}\n" +
		"github: {appSecret: {namespace: ns, name: app, appIDKey: a, installationIDKey: b, privateKeyKey: c}, apiURL: https://ghe.example/api/v3, gitURL: https://ghe.example}\n"
	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if c.RunNamespace != "agents" || c.SandboxNamespace != "boxes" || c.Defaults.RuntimeClassName != "kata" {
		t.Fatalf("got %q, %q, %q", c.RunNamespace, c.SandboxNamespace, c.Defaults.RuntimeClassName)
	}
	if c.Gateway.OperatorKeySecret.Namespace != "llm-keys" {
		t.Fatalf("gateway.operatorKeySecret.namespace = %q, want llm-keys", c.Gateway.OperatorKeySecret.Namespace)
	}
	if c.Retention.AfterFetch.Duration != time.Hour || c.Retention.Max.Duration != 2*time.Hour {
		t.Fatalf("retention = %v, %v", c.Retention.AfterFetch.Duration, c.Retention.Max.Duration)
	}
	if c.AgentSandbox.Namespace != "sandbox-ctl" {
		t.Fatalf("agentSandbox.namespace = %q, want sandbox-ctl", c.AgentSandbox.Namespace)
	}
	g := c.GitHub
	if g.AppSecret.AppIDKey != "a" || g.AppSecret.InstallationIDKey != "b" || g.AppSecret.PrivateKeyKey != "c" || g.APIURL != "https://ghe.example/api/v3" || g.GitURL != "https://ghe.example" {
		t.Fatalf("github = %+v", g)
	}
}

func TestParseValidation(t *testing.T) {
	cases := []struct {
		name, old, repl, field string
	}{
		{"issuer", "issuerURL: http://dex", "issuerURL: ''", "oidc.issuerURL"},
		{"audience", "audience: skua-omp", "audience: ''", "oidc.audience"},
		{"gateway base", "baseURL: http://litellm.gateway.svc:4000", "baseURL: ''", "gateway.baseURL"},
		{"operator key name", "name: skua-litellm-operator", "name: ''", "gateway.operatorKeySecret.name"},
		{"operator key key", "key: apiKey", "key: ''", "gateway.operatorKeySecret.key"},
		{"operator key secret", "  operatorKeySecret: {name: skua-litellm-operator, key: apiKey}\n", "", "gateway.operatorKeySecret"},
		{"operator key in run namespace", "{name: skua-litellm-operator, key: apiKey}", "{namespace: skua, name: skua-litellm-operator, key: apiKey}", "gateway.operatorKeySecret.namespace"},
		{"operator key in sandbox namespace", "{name: skua-litellm-operator, key: apiKey}", "{namespace: skua-sandboxes, name: skua-litellm-operator, key: apiKey}", "gateway.operatorKeySecret.namespace"},
		{"runner url", "runnerURL: http://skua-api.skua.svc", "runnerURL: ''", "api.runnerURL"},
		{"runner image", "image: skua:dev", "image: ''", "runner.image"},
		{"pod cidr", "podCIDRs: [10.244.0.0/16]", "podCIDRs: [10.244.0.0/16, not-a-cidr]", "network.podCIDRs[1]"},
		{"service cidr", "serviceCIDRs: [10.96.0.0/16]", "serviceCIDRs: [10.96.0.0]", "network.serviceCIDRs[0]"},
		{"node cidr", "nodeCIDRs: [172.18.0.0/16]", "nodeCIDRs: [172.18.0.0/33]", "network.nodeCIDRs[0]"},
		{"empty pod cidrs", "podCIDRs: [10.244.0.0/16]", "podCIDRs: []", "network.podCIDRs"},
		{"empty service cidrs", "serviceCIDRs: [10.96.0.0/16]", "serviceCIDRs: []", "network.serviceCIDRs"},
		{"missing node cidrs", ", nodeCIDRs: [172.18.0.0/16]", "", "network.nodeCIDRs"},
		{"retention order", "runner:", "retention: {afterFetch: 200h, max: 168h}\nrunner:", "retention.afterFetch"},
		{"retention order default max", "runner:", "retention: {afterFetch: 169h}\nrunner:", "retention.afterFetch"},
		{"versions invalid", `supportedVersions: ">=0.1.0 <0.2.0"`, `supportedVersions: "not a version"`, "runner.supportedVersions"},
		{"versions empty", `, supportedVersions: ">=0.1.0 <0.2.0"`, "", "runner.supportedVersions"},
		{"unknown top-level field", "runner:", "bogus: 1\nrunner:", "bogus"},
		{"unknown nested field", "audience: skua-omp", "audience: skua-omp, clientID: x", "clientID"},
		{"gateway egress namespace", "egress: {namespace: gateway, port: 4000}", "egress: {port: 4000}", "gateway.egress.namespace"},
		{"gateway egress port zero", "egress: {namespace: gateway, port: 4000}", "egress: {namespace: gateway}", "gateway.egress.port"},
		{"api egress port too large", "egress: {namespace: skua, port: 8080}", "egress: {namespace: skua, port: 65536}", "api.egress.port"},
		{"api egress missing", "  egress: {namespace: skua, port: 8080}\n", "", "api.egress.namespace"},
		{"extra egress namespace", "nodeCIDRs: [172.18.0.0/16]}", "nodeCIDRs: [172.18.0.0/16], extraEgress: [{namespace: forge, port: 443}, {port: 443}]}", "network.extraEgress[1].namespace"},
		{"extra egress port", "nodeCIDRs: [172.18.0.0/16]}", "nodeCIDRs: [172.18.0.0/16], extraEgress: [{namespace: forge, port: -1}]}", "network.extraEgress[0].port"},
		{"github app secret namespace", "runner:", "github: {appSecret: {name: app}}\nrunner:", "github.appSecret.namespace"},
		{"github app secret name", "runner:", "github: {appSecret: {namespace: flux-system}}\nrunner:", "github.appSecret.name"},
		{"github app secret in run namespace", "runner:", "github: {appSecret: {namespace: skua, name: app}}\nrunner:", "github.appSecret.namespace"},
		{"github app secret in sandbox namespace", "runner:", "github: {appSecret: {namespace: skua-sandboxes, name: app}}\nrunner:", "github.appSecret.namespace"},
		{"negative afterFetch", "runner:", "retention: {afterFetch: -1h}\nrunner:", "retention.afterFetch"},
		{"negative max", "runner:", "retention: {afterFetch: -2h, max: -1h}\nrunner:", "retention.max"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(replaced(t, minimalYAML, tc.old, tc.repl)))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("error %q does not name %q", err, tc.field)
			}
		})
	}
}

func TestParseReportsEveryField(t *testing.T) {
	_, err := Parse([]byte("{}"))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, field := range []string{"oidc.issuerURL", "oidc.audience", "gateway.baseURL", "gateway.operatorKeySecret", "gateway.egress.namespace", "gateway.egress.port", "api.runnerURL", "api.egress.namespace", "api.egress.port", "runner.image", "network.podCIDRs", "network.serviceCIDRs", "network.nodeCIDRs", "runner.supportedVersions"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error %q does not name %q", err, field)
		}
	}
}

func writeConfig(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadFromFlag(t *testing.T) {
	path := writeConfig(t, minimalYAML)
	for _, args := range [][]string{{"--config", path}, {"--config=" + path}, {"--other", "x", "--config", path}} {
		c, err := Load(args, envOf(nil))
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if c.Runner.Image != "skua:dev" {
			t.Fatalf("%v: image = %q", args, c.Runner.Image)
		}
	}
}

func TestLoadFromEnv(t *testing.T) {
	c, err := Load(nil, envOf(map[string]string{"SKUA_CONFIG": minimalYAML}))
	if err != nil {
		t.Fatal(err)
	}
	if c.API.RunnerURL != "http://skua-api.skua.svc" {
		t.Fatalf("runnerURL = %q", c.API.RunnerURL)
	}
}

func TestLoadFlagWinsOverEnv(t *testing.T) {
	path := writeConfig(t, replaced(t, minimalYAML, "image: skua:dev", "image: from-file"))
	env := envOf(map[string]string{"SKUA_CONFIG": replaced(t, minimalYAML, "image: skua:dev", "image: from-env")})
	c, err := Load([]string{"--config", path}, env)
	if err != nil {
		t.Fatal(err)
	}
	if c.Runner.Image != "from-file" {
		t.Fatalf("image = %q, want from-file", c.Runner.Image)
	}
}

func TestLoadNeither(t *testing.T) {
	_, err := Load([]string{"--other", "x"}, envOf(nil))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "--config") || !strings.Contains(err.Error(), "SKUA_CONFIG") {
		t.Fatalf("error %q does not name both sources", err)
	}
}

func TestLoadFlagErrors(t *testing.T) {
	for _, args := range [][]string{{"--config"}, {"--config="}, {"--config", filepath.Join(t.TempDir(), "missing.yaml")}} {
		if _, err := Load(args, envOf(map[string]string{"SKUA_CONFIG": minimalYAML})); err == nil {
			t.Fatalf("%v: expected an error", args)
		}
	}
}

func TestLoadIgnoresArgsAfterDoubleDash(t *testing.T) {
	c, err := Load([]string{"--", "--config", "/nonexistent"}, envOf(map[string]string{"SKUA_CONFIG": minimalYAML}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Runner.Image != "skua:dev" {
		t.Fatalf("image = %q", c.Runner.Image)
	}
}

func TestLoadInvalidEnvIsParsed(t *testing.T) {
	_, err := Load(nil, envOf(map[string]string{"SKUA_CONFIG": "bogus: 1\n"}))
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error = %v, want unknown field bogus", err)
	}
}
