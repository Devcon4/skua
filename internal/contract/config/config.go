// Package config loads the skua control-plane configuration shared by the operator, skua-api and the installer chart.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

const (
	EnvVar   = "SKUA_CONFIG"
	FlagName = "--config"
)

type Config struct {
	RunNamespace     string       `json:"runNamespace"`
	SandboxNamespace string       `json:"sandboxNamespace"`
	OIDC             OIDC         `json:"oidc"`
	Gateway          Gateway      `json:"gateway"`
	API              API          `json:"api"`
	GitHub           *GitHub      `json:"github,omitempty"`
	Network          Network      `json:"network"`
	Defaults         Defaults     `json:"defaults"`
	Retention        Retention    `json:"retention"`
	Runner           Runner       `json:"runner"`
	AgentSandbox     AgentSandbox `json:"agentSandbox"`
}

type OIDC struct {
	IssuerURL string `json:"issuerURL"`
	Audience  string `json:"audience"`
}

type Gateway struct {
	BaseURL           string       `json:"baseURL"`
	OperatorKeySecret SecretKeyRef `json:"operatorKeySecret"`
	Egress            EgressTarget `json:"egress"`
}

type API struct {
	RunnerURL string       `json:"runnerURL"`
	Egress    EgressTarget `json:"egress"`
}

type GitHub struct {
	AppSecret AppSecret `json:"appSecret"`
	APIURL    string    `json:"apiURL"`
	GitURL    string    `json:"gitURL"`
}

type AppSecret struct {
	Namespace         string `json:"namespace"`
	Name              string `json:"name"`
	AppIDKey          string `json:"appIDKey"`
	InstallationIDKey string `json:"installationIDKey"`
	PrivateKeyKey     string `json:"privateKeyKey"`
}

type SecretKeyRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Key       string `json:"key"`
}

type EgressTarget struct {
	Namespace string            `json:"namespace"`
	PodLabels map[string]string `json:"podLabels,omitempty"`
	Port      int32             `json:"port"`
}

type Network struct {
	PodCIDRs     []string       `json:"podCIDRs"`
	ServiceCIDRs []string       `json:"serviceCIDRs"`
	NodeCIDRs    []string       `json:"nodeCIDRs"`
	ExtraEgress  []EgressTarget `json:"extraEgress,omitempty"`
}

type Defaults struct {
	RuntimeClassName string `json:"runtimeClassName"`
}

type Retention struct {
	AfterFetch metav1.Duration `json:"afterFetch"`
	Max        metav1.Duration `json:"max"`
}

type Runner struct {
	Image             string `json:"image"`
	SupportedVersions string `json:"supportedVersions"`
}

type AgentSandbox struct {
	Namespace string `json:"namespace"`
}

// Load reads the YAML from the file named by --config, or else from the SKUA_CONFIG environment variable, and returns Parse of it.
func Load(args []string, getenv func(string) string) (*Config, error) {
	path, ok, err := configFlag(args)
	if err != nil {
		return nil, err
	}
	if ok {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		return Parse(data)
	}
	if data := getenv(EnvVar); data != "" {
		return Parse([]byte(data))
	}
	return nil, fmt.Errorf("config: neither %s <file> nor %s is set", FlagName, EnvVar)
}

func configFlag(args []string) (string, bool, error) {
	for i, a := range args {
		if a == "--" {
			break
		}
		if v, ok := strings.CutPrefix(a, FlagName+"="); ok {
			if v == "" {
				return "", false, fmt.Errorf("config: %s needs a file", FlagName)
			}
			return v, true, nil
		}
		if a == FlagName {
			if i+1 >= len(args) || args[i+1] == "" {
				return "", false, fmt.Errorf("config: %s needs a file", FlagName)
			}
			return args[i+1], true, nil
		}
	}
	return "", false, nil
}

// Parse strictly decodes one YAML document, fills defaults and validates it; the error names every invalid field.
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := yaml.UnmarshalStrict(data, &c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	setDefault(&c.RunNamespace, "skua")
	setDefault(&c.SandboxNamespace, "skua-sandboxes")
	setDefault(&c.Gateway.OperatorKeySecret.Namespace, "skua-system")
	if c.GitHub != nil {
		setDefault(&c.GitHub.AppSecret.AppIDKey, "githubAppID")
		setDefault(&c.GitHub.AppSecret.InstallationIDKey, "githubAppInstallationID")
		setDefault(&c.GitHub.AppSecret.PrivateKeyKey, "githubAppPrivateKey")
		setDefault(&c.GitHub.APIURL, "https://api.github.com")
		setDefault(&c.GitHub.GitURL, "https://github.com")
	}
	setDefault(&c.Defaults.RuntimeClassName, "gvisor")
	setDefault(&c.AgentSandbox.Namespace, "agent-sandbox-system")
	if c.Retention.AfterFetch.Duration == 0 {
		c.Retention.AfterFetch.Duration = 24 * time.Hour
	}
	if c.Retention.Max.Duration == 0 {
		c.Retention.Max.Duration = 168 * time.Hour
	}
}

func setDefault(field *string, value string) {
	if *field == "" {
		*field = value
	}
}

type configValidation struct {
	c    *Config
	errs []error
}

func (c *Config) validate() error {
	v := &configValidation{c: c}
	v.required("oidc.issuerURL", c.OIDC.IssuerURL)
	v.required("oidc.audience", c.OIDC.Audience)
	v.required("gateway.baseURL", c.Gateway.BaseURL)
	v.required("gateway.operatorKeySecret.name", c.Gateway.OperatorKeySecret.Name)
	v.required("gateway.operatorKeySecret.key", c.Gateway.OperatorKeySecret.Key)
	v.separateNamespace("gateway.operatorKeySecret.namespace", c.Gateway.OperatorKeySecret.Namespace)
	v.required("api.runnerURL", c.API.RunnerURL)
	v.required("runner.image", c.Runner.Image)
	v.gitHub()

	v.egress("gateway.egress", c.Gateway.Egress)
	v.egress("api.egress", c.API.Egress)
	for i, t := range c.Network.ExtraEgress {
		v.egress(fmt.Sprintf("network.extraEgress[%d]", i), t)
	}

	v.cidrs("network.podCIDRs", c.Network.PodCIDRs)
	v.cidrs("network.serviceCIDRs", c.Network.ServiceCIDRs)
	v.cidrs("network.nodeCIDRs", c.Network.NodeCIDRs)

	v.retention()
	v.supportedRunnerVersions()
	return errors.Join(v.errs...)
}

func (v *configValidation) fail(format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf(format, args...))
}

func (v *configValidation) required(field, value string) {
	if value == "" {
		v.fail("%s: required", field)
	}
}

func (v *configValidation) separateNamespace(field, ns string) {
	if ns != "" && (ns == v.c.RunNamespace || ns == v.c.SandboxNamespace) {
		v.fail("%s: %q must differ from runNamespace and sandboxNamespace", field, ns)
	}
}

func (v *configValidation) gitHub() {
	if v.c.GitHub == nil {
		return
	}
	v.required("github.appSecret.namespace", v.c.GitHub.AppSecret.Namespace)
	v.required("github.appSecret.name", v.c.GitHub.AppSecret.Name)
	v.separateNamespace("github.appSecret.namespace", v.c.GitHub.AppSecret.Namespace)
}

func (v *configValidation) egress(field string, t EgressTarget) {
	v.required(field+".namespace", t.Namespace)
	if t.Port < 1 || t.Port > 65535 {
		v.fail("%s.port: %d is not a port in 1..65535", field, t.Port)
	}
}

func (v *configValidation) cidrs(field string, values []string) {
	if len(values) == 0 {
		v.fail("%s: at least one CIDR required", field)
	}
	for i, value := range values {
		if _, _, err := net.ParseCIDR(value); err != nil {
			v.fail("%s[%d]: invalid CIDR %q", field, i, value)
		}
	}
}

func (v *configValidation) retention() {
	r := v.c.Retention
	if r.AfterFetch.Duration < 0 {
		v.fail("retention.afterFetch: %s is negative", r.AfterFetch.Duration)
	}
	if r.Max.Duration < 0 {
		v.fail("retention.max: %s is negative", r.Max.Duration)
	}
	if r.AfterFetch.Duration > r.Max.Duration {
		v.fail("retention.afterFetch: %s is greater than retention.max %s", r.AfterFetch.Duration, r.Max.Duration)
	}
}

func (v *configValidation) supportedRunnerVersions() {
	constraint := v.c.Runner.SupportedVersions
	if _, err := semver.NewConstraint(constraint); constraint == "" || err != nil {
		v.fail("runner.supportedVersions: %q is not a semver constraint", constraint)
	}
}
