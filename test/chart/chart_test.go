package chart

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/Devcon4/skua/internal/contract/config"
)

const (
	repoRoot        = "../.."
	releaseName     = "skua"
	releaseNS       = "skua"
	operatorAccount = "skua-operator"
	apiAccount      = "skua-api"
	runnerAccount   = "skua-runner"
	configMapName   = "skua-config"
	configKey       = "config.yaml"
)

var (
	chartDir   = filepath.Join(repoRoot, "charts", "skua")
	lintValues = filepath.Join(chartDir, "ci", "lint-values.yaml")
	fullValues = filepath.Join(chartDir, "ci", "full-values.yaml")
	forgeNone  = filepath.Join("testdata", "forge-none.yaml")
)

type valuesSet struct {
	name string
	path string
}

var valuesSets = []valuesSet{
	{"lint", lintValues},
	{"full", fullValues},
	{"forge-none", forgeNone},
}

func helmBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("HELM"); p != "" {
		return p
	}
	local := filepath.Join(repoRoot, "bin", "helm")
	if _, err := os.Stat(local); err == nil {
		return local
	}
	if p, err := exec.LookPath("helm"); err == nil {
		return p
	}
	t.Fatalf("helm not found: set env HELM, or provide %s, or put helm on PATH", filepath.Join("bin", "helm"))
	return ""
}

func kubectlBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("KUBECTL"); p != "" {
		return p
	}
	p, err := exec.LookPath("kubectl")
	if err != nil {
		t.Fatalf("kubectl not found: set env KUBECTL or put kubectl on PATH")
	}
	return p
}

func helmArgs(values string, set []string) []string {
	args := []string{"template", releaseName, chartDir, "-n", releaseNS, "-f", values}
	for _, s := range set {
		args = append(args, "--set", s)
	}
	return args
}

func helmTemplate(t *testing.T, values string, set ...string) (stdout, stderr []byte, err error) {
	t.Helper()
	cmd := exec.Command(helmBinary(t), helmArgs(values, set)...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err = cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

var (
	renderMu    sync.Mutex
	renderCache = map[string]manifestSet{}
)

func render(t *testing.T, values string, set ...string) manifestSet {
	t.Helper()
	key := strings.Join(helmArgs(values, set), "\x00")
	renderMu.Lock()
	defer renderMu.Unlock()
	if objs, ok := renderCache[key]; ok {
		return objs
	}
	out, stderr, err := helmTemplate(t, values, set...)
	if err != nil {
		t.Fatalf("helm %s: %v\n%s", strings.Join(helmArgs(values, set), " "), err, stderr)
	}
	objs := decode(t, out, "helm template -f "+values)
	renderCache[key] = objs
	return objs
}

func kustomizeDefault(t *testing.T) manifestSet {
	t.Helper()
	dir := filepath.Join(repoRoot, "config", "default")
	out, err := exec.Command(kubectlBinary(t), "kustomize", dir).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("kubectl kustomize %s: %v\n%s", dir, err, exitErr.Stderr)
		}
		t.Fatalf("kubectl kustomize %s: %v", dir, err)
	}
	return decode(t, out, "kubectl kustomize "+dir)
}

func readManifests(t *testing.T, path string) manifestSet {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return decode(t, data, path)
}

type manifestSet []unstructured.Unstructured

func decode(t *testing.T, data []byte, source string) manifestSet {
	t.Helper()
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var objs manifestSet
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return objs
			}
			t.Fatalf("decode %s: %v", source, err)
		}
		if m != nil {
			objs = append(objs, unstructured.Unstructured{Object: m})
		}
	}
}

func (s manifestSet) ofKind(kind string) []unstructured.Unstructured {
	var out []unstructured.Unstructured
	for _, o := range s {
		if o.GetKind() == kind {
			out = append(out, o)
		}
	}
	return out
}

func (s manifestSet) find(kind, namespace, name string) (unstructured.Unstructured, bool) {
	for _, o := range s.ofKind(kind) {
		if o.GetNamespace() == namespace && o.GetName() == name {
			return o, true
		}
	}
	return unstructured.Unstructured{}, false
}

func mustFind[T any](t *testing.T, objs manifestSet, kind, namespace, name string) T {
	t.Helper()
	u, ok := objs.find(kind, namespace, name)
	if !ok {
		t.Fatalf("%s %s/%s missing", kind, namespace, name)
	}
	return convert[T](t, u)
}

func convert[T any](t *testing.T, u unstructured.Unstructured) T {
	t.Helper()
	var v T
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &v); err != nil {
		t.Fatalf("convert %s %s/%s: %v", u.GetKind(), u.GetNamespace(), u.GetName(), err)
	}
	return v
}

func renderedConfigData(t *testing.T, objs manifestSet) []byte {
	t.Helper()
	cm := mustFind[corev1.ConfigMap](t, objs, "ConfigMap", releaseNS, configMapName)
	data, ok := cm.Data[configKey]
	if !ok {
		t.Fatalf("ConfigMap %s/%s has no key %s", releaseNS, configMapName, configKey)
	}
	return []byte(data)
}

func renderedConfig(t *testing.T, objs manifestSet) *config.Config {
	t.Helper()
	cfg, err := config.Parse(renderedConfigData(t, objs))
	if err != nil {
		t.Fatalf("config.Parse of rendered %s: %v", configKey, err)
	}
	return cfg
}

func grantsSecrets(r rbacv1.PolicyRule) bool {
	coreGroup := slices.Contains(r.APIGroups, "") || slices.Contains(r.APIGroups, "*")
	return coreGroup && (slices.Contains(r.Resources, "secrets") || slices.Contains(r.Resources, "*"))
}

func isAccount(s rbacv1.Subject, name string) bool {
	return s.Kind == rbacv1.ServiceAccountKind && s.Name == name && s.Namespace == releaseNS
}

type ruleAtom struct {
	ref      string
	group    string
	resource string
	name     string
	url      string
	verb     string
}

func (a ruleAtom) String() string {
	if a.ref != "" {
		return "roleRef " + a.ref
	}
	if a.url != "" {
		return fmt.Sprintf("%s %s", a.verb, a.url)
	}
	s := fmt.Sprintf("%s %s.%s", a.verb, a.resource, a.group)
	if a.name != "" {
		s += "/" + a.name
	}
	return s
}

func atoms(rules []rbacv1.PolicyRule) map[ruleAtom]bool {
	set := map[ruleAtom]bool{}
	for _, r := range rules {
		names := r.ResourceNames
		if len(names) == 0 {
			names = []string{""}
		}
		for _, verb := range r.Verbs {
			for _, g := range r.APIGroups {
				for _, res := range r.Resources {
					for _, n := range names {
						set[ruleAtom{group: g, resource: res, name: n, verb: verb}] = true
					}
				}
			}
			for _, u := range r.NonResourceURLs {
				set[ruleAtom{url: u, verb: verb}] = true
			}
		}
	}
	return set
}

type grantKey struct {
	account string
	scope   string
}

func grants(t *testing.T, objs manifestSet, scopeOf func(string) string) map[grantKey]map[ruleAtom]bool {
	t.Helper()
	clusterRoles := map[string][]rbacv1.PolicyRule{}
	for _, u := range objs.ofKind("ClusterRole") {
		cr := convert[rbacv1.ClusterRole](t, u)
		clusterRoles[cr.Name] = cr.Rules
	}
	roles := map[string][]rbacv1.PolicyRule{}
	for _, u := range objs.ofKind("Role") {
		r := convert[rbacv1.Role](t, u)
		roles[r.Namespace+"/"+r.Name] = r.Rules
	}
	out := map[grantKey]map[ruleAtom]bool{}
	add := func(subjects []rbacv1.Subject, scope string, set map[ruleAtom]bool) {
		for _, s := range subjects {
			for _, account := range []string{operatorAccount, apiAccount} {
				if !isAccount(s, account) {
					continue
				}
				k := grantKey{account, scope}
				if out[k] == nil {
					out[k] = map[ruleAtom]bool{}
				}
				for a := range set {
					out[k][a] = true
				}
			}
		}
	}
	resolve := func(ref rbacv1.RoleRef, namespace, binding string) map[ruleAtom]bool {
		switch ref.Kind {
		case "ClusterRole":
			if rules, ok := clusterRoles[ref.Name]; ok {
				return atoms(rules)
			}
			return map[ruleAtom]bool{{ref: "ClusterRole/" + ref.Name}: true}
		case "Role":
			if rules, ok := roles[namespace+"/"+ref.Name]; ok {
				return atoms(rules)
			}
		}
		t.Errorf("%s refers to %s %s, which is not in the manifests", binding, ref.Kind, ref.Name)
		return nil
	}
	for _, u := range objs.ofKind("ClusterRoleBinding") {
		b := convert[rbacv1.ClusterRoleBinding](t, u)
		add(b.Subjects, "cluster", resolve(b.RoleRef, "", "ClusterRoleBinding "+b.Name))
	}
	for _, u := range objs.ofKind("RoleBinding") {
		b := convert[rbacv1.RoleBinding](t, u)
		add(b.Subjects, scopeOf(b.Namespace), resolve(b.RoleRef, b.Namespace, "RoleBinding "+b.Namespace+"/"+b.Name))
	}
	return out
}

func sortedAtoms(set map[ruleAtom]bool) []string {
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a.String())
	}
	sort.Strings(out)
	return out
}

func scopeLabels(scopes map[string]string) func(string) string {
	return func(ns string) string {
		if label, ok := scopes[ns]; ok {
			return label
		}
		return "namespace " + ns
	}
}

type volumeFinding struct {
	deployment string
	detail     string
}

func (f volumeFinding) String() string { return f.deployment + ": " + f.detail }

func volumeFindings(t *testing.T, objs manifestSet) []volumeFinding {
	t.Helper()
	var out []volumeFinding
	for _, u := range objs.ofKind("Deployment") {
		spec, _, err := unstructured.NestedMap(u.Object, "spec", "template", "spec")
		if err != nil {
			t.Fatalf("Deployment %s/%s pod spec: %v", u.GetNamespace(), u.GetName(), err)
		}
		id := u.GetNamespace() + "/" + u.GetName()
		if v, ok := spec["volumes"]; ok {
			out = append(out, volumeFinding{id, fmt.Sprintf("volumes %v", v)})
		}
		for _, field := range []string{"initContainers", "containers"} {
			containers, _, _ := unstructured.NestedSlice(spec, field)
			for _, c := range containers {
				cm, _ := c.(map[string]any)
				if m, ok := cm["volumeMounts"]; ok {
					out = append(out, volumeFinding{id, fmt.Sprintf("%s %v volumeMounts %v", field, cm["name"], m)})
				}
			}
		}
	}
	return out
}
