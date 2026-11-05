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

func (c *Config) validate() error {
	var errs []error
	required := func(field, value string) {
		if value == "" {
			errs = append(errs, fmt.Errorf("%s: required", field))
		}
	}
	required("oidc.issuerURL", c.OIDC.IssuerURL)
	required("oidc.audience", c.OIDC.Audience)
	required("gateway.baseURL", c.Gateway.BaseURL)
	required("gateway.operatorKeySecret.name", c.Gateway.OperatorKeySecret.Name)
	required("gateway.operatorKeySecret.key", c.Gateway.OperatorKeySecret.Key)
	separateNamespace := func(field, ns string) {
		if ns != "" && (ns == c.RunNamespace || ns == c.SandboxNamespace) {
			errs = append(errs, fmt.Errorf("%s: %q must differ from runNamespace and sandboxNamespace", field, ns))
		}
	}
	separateNamespace("gateway.operatorKeySecret.namespace", c.Gateway.OperatorKeySecret.Namespace)
	required("api.runnerURL", c.API.RunnerURL)
	required("runner.image", c.Runner.Image)
	if c.GitHub != nil {
		required("github.appSecret.namespace", c.GitHub.AppSecret.Namespace)
		required("github.appSecret.name", c.GitHub.AppSecret.Name)
		separateNamespace("github.appSecret.namespace", c.GitHub.AppSecret.Namespace)
	}

	egress := func(field string, t EgressTarget) {
		required(field+".namespace", t.Namespace)
		if t.Port < 1 || t.Port > 65535 {
			errs = append(errs, fmt.Errorf("%s.port: %d is not a port in 1..65535", field, t.Port))
		}
	}
	egress("gateway.egress", c.Gateway.Egress)
	egress("api.egress", c.API.Egress)
	for i, t := range c.Network.ExtraEgress {
		egress(fmt.Sprintf("network.extraEgress[%d]", i), t)
	}

	cidrs := func(field string, values []string) {
		if len(values) == 0 {
			errs = append(errs, fmt.Errorf("%s: at least one CIDR required", field))
		}
		for i, v := range values {
			if _, _, err := net.ParseCIDR(v); err != nil {
				errs = append(errs, fmt.Errorf("%s[%d]: invalid CIDR %q", field, i, v))
			}
		}
	}
	cidrs("network.podCIDRs", c.Network.PodCIDRs)
	cidrs("network.serviceCIDRs", c.Network.ServiceCIDRs)
	cidrs("network.nodeCIDRs", c.Network.NodeCIDRs)

	if c.Retention.AfterFetch.Duration < 0 {
		errs = append(errs, fmt.Errorf("retention.afterFetch: %s is negative", c.Retention.AfterFetch.Duration))
	}
	if c.Retention.Max.Duration < 0 {
		errs = append(errs, fmt.Errorf("retention.max: %s is negative", c.Retention.Max.Duration))
	}
	if c.Retention.AfterFetch.Duration > c.Retention.Max.Duration {
		errs = append(errs, fmt.Errorf("retention.afterFetch: %s is greater than retention.max %s", c.Retention.AfterFetch.Duration, c.Retention.Max.Duration))
	}

	if _, err := semver.NewConstraint(c.Runner.SupportedVersions); c.Runner.SupportedVersions == "" || err != nil {
		errs = append(errs, fmt.Errorf("runner.supportedVersions: %q is not a semver constraint", c.Runner.SupportedVersions))
	}

	return errors.Join(errs...)
}
