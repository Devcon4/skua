package operator

import (
	"testing"

	"github.com/Devcon4/skua/internal/contract/config"
)

const operatorTestConfigYAML = `
runNamespace: skua
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
  appSecret: {namespace: flux-system, name: skua-test-github-app}
network: {podCIDRs: [10.244.0.0/16], serviceCIDRs: [10.96.0.0/16], nodeCIDRs: [172.18.0.0/16]}
retention: {afterFetch: 24h, max: 168h}
runner: {image: skua:dev, supportedVersions: ">=0.1.0 <0.2.0"}
`

func operatorTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(operatorTestConfigYAML))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}
