package manifests

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/Devcon4/skua/internal/contract/config"
)

const (
	repoRoot          = "../.."
	appSecretName     = "skua-test-github-app"
	appSecretNS       = "flux-system"
	controlPlaneNS    = "skua"
	sandboxNS         = "skua-sandboxes"
	operatorKeyNS     = "skua-system"
	operatorKeySecret = "skua-litellm-operator"
	runnerAccount     = "skua-runner"
	operatorAccount   = "skua-operator"
	apiAccount        = "skua-api"
)

var kindEnv = map[string]string{
	"SKUA_POD_CIDR":     "10.244.0.0/16",
	"SKUA_SERVICE_CIDR": "10.96.0.0/16",
	"SKUA_NODE_CIDR":    "172.18.0.2/32",
}

type manifestSet []unstructured.Unstructured

func kustomize(t *testing.T, dir string) manifestSet {
	t.Helper()
	out, err := exec.Command("kubectl", "kustomize", dir).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("kubectl kustomize %s: %v\n%s", dir, err, exitErr.Stderr)
		}
		t.Fatalf("kubectl kustomize %s: %v", dir, err)
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
	var objs manifestSet
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return objs
			}
			t.Fatalf("decode kustomize output of %s: %v", dir, err)
		}
		if m != nil {
			objs = append(objs, unstructured.Unstructured{Object: m})
		}
	}
}

func defaultManifests(t *testing.T) manifestSet {
	t.Helper()
	return kustomize(t, filepath.Join(repoRoot, "config", "default"))
}

func kindManifests(t *testing.T) manifestSet {
	t.Helper()
	root := t.TempDir()
	cfgDir := filepath.Join(root, "config")
	if err := os.CopyFS(cfgDir, os.DirFS(filepath.Join(repoRoot, "config"))); err != nil {
		t.Fatalf("copy config: %v", err)
	}
	tmpl, err := os.ReadFile(filepath.Join(cfgDir, "kind", "config.yaml.tmpl"))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	rendered := os.Expand(string(tmpl), func(name string) string {
		v, ok := kindEnv[name]
		if !ok {
			t.Errorf("config.yaml.tmpl uses %s, which test/kindenv does not record", name)
		}
		return v
	})
	if err := os.WriteFile(filepath.Join(cfgDir, "kind", "config.yaml"), []byte(rendered), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	return kustomize(t, filepath.Join(cfgDir, "kind"))
}

func bothManifests(t *testing.T) map[string]manifestSet {
	t.Helper()
	return map[string]manifestSet{"default": defaultManifests(t), "kind": kindManifests(t)}
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

func convert[T any](t *testing.T, u unstructured.Unstructured) T {
	t.Helper()
	var v T
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &v); err != nil {
		t.Fatalf("convert %s %s/%s: %v", u.GetKind(), u.GetNamespace(), u.GetName(), err)
	}
	return v
}

func grantsSecrets(r rbacv1.PolicyRule) bool {
	coreGroup := slices.Contains(r.APIGroups, "") || slices.Contains(r.APIGroups, "*")
	return coreGroup && (slices.Contains(r.Resources, "secrets") || slices.Contains(r.Resources, "*"))
}

func bindsRunner(subjects []rbacv1.Subject) bool {
	return slices.ContainsFunc(subjects, func(s rbacv1.Subject) bool {
		return s.Kind == rbacv1.ServiceAccountKind && s.Name == runnerAccount && s.Namespace == sandboxNS
	})
}

func bindsControlPlane(subjects []rbacv1.Subject, account string) bool {
	return slices.ContainsFunc(subjects, func(s rbacv1.Subject) bool {
		return s.Kind == rbacv1.ServiceAccountKind && s.Name == account && s.Namespace == controlPlaneNS
	})
}

func TestDeploymentsDeclareNoVolumes(t *testing.T) {
	for dir, objs := range bothManifests(t) {
		deployments := objs.ofKind("Deployment")
		if len(deployments) == 0 {
			t.Errorf("%s: no Deployments", dir)
		}
		for _, u := range deployments {
			d := convert[appsv1.Deployment](t, u)
			if len(d.Spec.Template.Spec.Volumes) != 0 {
				t.Errorf("%s: Deployment %s/%s declares volumes %v", dir, d.Namespace, d.Name, d.Spec.Template.Spec.Volumes)
			}
		}
	}
}

func TestControlPlaneHasOneReplica(t *testing.T) {
	objs := defaultManifests(t)
	for _, name := range []string{"skua-api", "skua-operator"} {
		u, ok := objs.find("Deployment", controlPlaneNS, name)
		if !ok {
			t.Errorf("Deployment %s/%s missing", controlPlaneNS, name)
			continue
		}
		d := convert[appsv1.Deployment](t, u)
		if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 {
			t.Errorf("Deployment %s replicas = %v, want 1", name, d.Spec.Replicas)
		}
	}
}

func TestNoClusterRoleGrantsSecrets(t *testing.T) {
	for dir, objs := range bothManifests(t) {
		for _, u := range objs.ofKind("ClusterRole") {
			cr := convert[rbacv1.ClusterRole](t, u)
			for _, r := range cr.Rules {
				if grantsSecrets(r) {
					t.Errorf("%s: ClusterRole %s grants Secrets: %+v", dir, cr.Name, r)
				}
			}
		}
	}
}

func TestAppSecretRoleIsGetOnly(t *testing.T) {
	objs := kindManifests(t)
	roles := map[string]bool{}
	for _, u := range objs.ofKind("Role") {
		role := convert[rbacv1.Role](t, u)
		if role.Namespace != appSecretNS {
			continue
		}
		for _, r := range role.Rules {
			if !grantsSecrets(r) {
				continue
			}
			roles[role.Name] = true
			if !slices.Equal(r.ResourceNames, []string{appSecretName}) || !slices.Equal(r.Verbs, []string{"get"}) {
				t.Errorf("Role %s/%s rule %+v, want resourceNames [%s] and verbs [get]", appSecretNS, role.Name, r, appSecretName)
			}
		}
	}
	if len(roles) != 1 {
		t.Fatalf("Roles granting Secrets in %s = %v, want exactly one", appSecretNS, roles)
	}
	bound := false
	for _, u := range objs.ofKind("RoleBinding") {
		rb := convert[rbacv1.RoleBinding](t, u)
		if rb.Namespace != appSecretNS || rb.RoleRef.Kind != "Role" || !roles[rb.RoleRef.Name] {
			continue
		}
		bound = bound || slices.ContainsFunc(rb.Subjects, func(s rbacv1.Subject) bool {
			return s.Kind == rbacv1.ServiceAccountKind && s.Name == operatorAccount && s.Namespace == controlPlaneNS
		})
	}
	if !bound {
		t.Errorf("no RoleBinding in %s binds the App Secret Role to %s/%s", appSecretNS, controlPlaneNS, operatorAccount)
	}
}

func TestOperatorKeyNamespaceGrantsOnlyOperatorGet(t *testing.T) {
	for dir, objs := range bothManifests(t) {
		if _, ok := objs.find("Namespace", "", operatorKeyNS); !ok {
			t.Errorf("%s: Namespace %s missing", dir, operatorKeyNS)
		}
		roles := map[string]bool{}
		for _, u := range objs.ofKind("Role") {
			role := convert[rbacv1.Role](t, u)
			if role.Namespace != operatorKeyNS {
				continue
			}
			roles[role.Name] = true
			for _, r := range role.Rules {
				if !grantsSecrets(r) || !slices.Equal(r.ResourceNames, []string{operatorKeySecret}) || !slices.Equal(r.Verbs, []string{"get"}) {
					t.Errorf("%s: Role %s/%s rule %+v, want only get on Secret %s", dir, operatorKeyNS, role.Name, r, operatorKeySecret)
				}
			}
		}
		operatorBound := false
		for _, u := range objs.ofKind("RoleBinding") {
			rb := convert[rbacv1.RoleBinding](t, u)
			if rb.Namespace != operatorKeyNS {
				continue
			}
			if bindsControlPlane(rb.Subjects, apiAccount) {
				t.Errorf("%s: RoleBinding %s/%s binds %s", dir, operatorKeyNS, rb.Name, apiAccount)
			}
			if rb.RoleRef.Kind != "Role" || !roles[rb.RoleRef.Name] {
				t.Errorf("%s: RoleBinding %s/%s refers to %s %s, want a Role in %s", dir, operatorKeyNS, rb.Name, rb.RoleRef.Kind, rb.RoleRef.Name, operatorKeyNS)
			}
			operatorBound = operatorBound || bindsControlPlane(rb.Subjects, operatorAccount)
		}
		if !operatorBound {
			t.Errorf("%s: no RoleBinding in %s binds %s", dir, operatorKeyNS, operatorAccount)
		}
	}
}

func TestRunnerHasNoRoleBinding(t *testing.T) {
	for dir, objs := range bothManifests(t) {
		u, ok := objs.find("ServiceAccount", sandboxNS, runnerAccount)
		if !ok {
			t.Errorf("%s: ServiceAccount %s/%s missing", dir, sandboxNS, runnerAccount)
		} else if sa := convert[corev1.ServiceAccount](t, u); sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
			t.Errorf("%s: ServiceAccount %s automountServiceAccountToken = %v, want false", dir, runnerAccount, sa.AutomountServiceAccountToken)
		}
		for _, u := range objs.ofKind("RoleBinding") {
			if rb := convert[rbacv1.RoleBinding](t, u); bindsRunner(rb.Subjects) {
				t.Errorf("%s: RoleBinding %s/%s binds %s", dir, rb.Namespace, rb.Name, runnerAccount)
			}
		}
		for _, u := range objs.ofKind("ClusterRoleBinding") {
			if crb := convert[rbacv1.ClusterRoleBinding](t, u); bindsRunner(crb.Subjects) {
				t.Errorf("%s: ClusterRoleBinding %s binds %s", dir, crb.Name, runnerAccount)
			}
		}
	}
}

func TestDefaultContainsCRDs(t *testing.T) {
	objs := defaultManifests(t)
	for _, name := range []string{"agentprofiles.skua.dev", "agentruns.skua.dev"} {
		if _, ok := objs.find("CustomResourceDefinition", "", name); !ok {
			t.Errorf("CustomResourceDefinition %s missing from config/default", name)
		}
	}
}

func TestKindConfigParses(t *testing.T) {
	u, ok := kindManifests(t).find("ConfigMap", controlPlaneNS, "skua-config")
	if !ok {
		t.Fatalf("ConfigMap %s/skua-config missing", controlPlaneNS)
	}
	cm := convert[corev1.ConfigMap](t, u)
	cfg, err := config.Parse([]byte(cm.Data["config.yaml"]))
	if err != nil {
		t.Fatalf("parse config.yaml: %v", err)
	}
	if cfg.GitHub == nil || cfg.GitHub.AppSecret.Name != appSecretName || cfg.GitHub.AppSecret.Namespace != appSecretNS {
		t.Errorf("github.appSecret = %+v, want %s/%s", cfg.GitHub, appSecretNS, appSecretName)
	}
	if ref := cfg.Gateway.OperatorKeySecret; ref.Namespace != operatorKeyNS || ref.Name != operatorKeySecret {
		t.Errorf("gateway.operatorKeySecret = %+v, want %s/%s", ref, operatorKeyNS, operatorKeySecret)
	}
}
