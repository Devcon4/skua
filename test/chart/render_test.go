package chart

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"

	"github.com/Devcon4/skua/internal/contract/config"
)

var controlPlane = []string{operatorAccount, apiAccount}

func deployment(t *testing.T, objs manifestSet, name string) appsv1.Deployment {
	t.Helper()
	return mustFind[appsv1.Deployment](t, objs, "Deployment", releaseNS, name)
}

func skuaContainer(t *testing.T, d appsv1.Deployment) corev1.Container {
	t.Helper()
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == "skua" {
			return c
		}
	}
	t.Fatalf("Deployment %s has no container skua", d.Name)
	return corev1.Container{}
}

func TestDeploymentsDeclareNoVolumes(t *testing.T) {
	for _, vs := range valuesSets {
		t.Run(vs.name, func(t *testing.T) {
			objs := render(t, vs.path)
			for _, name := range controlPlane {
				deployment(t, objs, name)
			}
			for _, f := range volumeFindings(t, objs) {
				t.Errorf("%s", f)
			}
		})
	}
	t.Run("fixture", func(t *testing.T) {
		findings := volumeFindings(t, readManifests(t, filepath.Join("testdata", "with-volume.yaml")))
		var details []string
		for _, f := range findings {
			details = append(details, f.String())
		}
		joined := strings.Join(details, "\n")
		for _, want := range []string{"volumes", "initContainers", "containers"} {
			if !slices.ContainsFunc(details, func(d string) bool { return strings.Contains(d, ": "+want+" ") }) {
				t.Errorf("helper missed %s in testdata/with-volume.yaml; findings:\n%s", want, joined)
			}
		}
		if !strings.Contains(joined, "scratch") {
			t.Errorf("findings do not name volume scratch:\n%s", joined)
		}
	})
}

func TestChartShipsNoCRDs(t *testing.T) {
	for _, vs := range valuesSets {
		t.Run(vs.name, func(t *testing.T) {
			for _, u := range render(t, vs.path).ofKind("CustomResourceDefinition") {
				t.Errorf("render contains CustomResourceDefinition %s", u.GetName())
			}
		})
	}
	if _, err := os.Stat(filepath.Join(chartDir, "crds")); !os.IsNotExist(err) {
		t.Errorf("charts/skua/crds exists or is unreadable: %v", err)
	}
}

func TestAppSecretAccessIsSingleGet(t *testing.T) {
	singleGet := func(name string) []rbacv1.PolicyRule {
		return []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{name}, Verbs: []string{"get"}}}
	}
	for _, vs := range valuesSets {
		t.Run(vs.name, func(t *testing.T) {
			objs := render(t, vs.path)
			cfg := renderedConfig(t, objs)
			if vs.path == fullValues && cfg.GitHub == nil {
				t.Fatalf("full-values.yaml renders no github block")
			}
			if vs.path == forgeNone && cfg.GitHub != nil {
				t.Fatalf("forge-none.yaml renders a github block")
			}

			type grant struct {
				namespace string
				rules     []rbacv1.PolicyRule
			}
			want := map[string]grant{
				"LiteLLM operator key": {cfg.Gateway.OperatorKeySecret.Namespace, singleGet(cfg.Gateway.OperatorKeySecret.Name)},
			}
			if cfg.GitHub != nil {
				want["GitHub App Secret"] = grant{cfg.GitHub.AppSecret.Namespace, singleGet(cfg.GitHub.AppSecret.Name)}
			}

			var outside []rbacv1.Role
			for _, u := range objs.ofKind("Role") {
				r := convert[rbacv1.Role](t, u)
				if r.Namespace != releaseNS && slices.ContainsFunc(r.Rules, grantsSecrets) {
					outside = append(outside, r)
				}
			}
			if len(outside) != len(want) {
				t.Errorf("%d Roles mention secrets outside namespace %s, want %d (%v)", len(outside), releaseNS, len(want), roleIDs(outside))
			}
			for purpose, g := range want {
				idx := slices.IndexFunc(outside, func(r rbacv1.Role) bool {
					return r.Namespace == g.namespace && reflect.DeepEqual(r.Rules, g.rules)
				})
				if idx < 0 {
					t.Errorf("no Role for the %s in namespace %s with rules %+v; Roles mentioning secrets outside %s: %v", purpose, g.namespace, g.rules, releaseNS, roleIDs(outside))
					continue
				}
				assertOnlyOperatorBound(t, objs, outside[idx])
			}

			for _, u := range objs.ofKind("ClusterRole") {
				cr := convert[rbacv1.ClusterRole](t, u)
				for _, r := range cr.Rules {
					if grantsSecrets(r) {
						t.Errorf("ClusterRole %s grants %v on secrets", cr.Name, r.Verbs)
					}
				}
			}
		})
	}
}

func roleIDs(roles []rbacv1.Role) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, r.Namespace+"/"+r.Name)
	}
	return out
}

func assertOnlyOperatorBound(t *testing.T, objs manifestSet, role rbacv1.Role) {
	t.Helper()
	bound := false
	for _, u := range objs.ofKind("RoleBinding") {
		b := convert[rbacv1.RoleBinding](t, u)
		if b.Namespace != role.Namespace || b.RoleRef.Kind != "Role" || b.RoleRef.Name != role.Name {
			continue
		}
		for _, s := range b.Subjects {
			if isAccount(s, operatorAccount) {
				bound = true
				continue
			}
			t.Errorf("RoleBinding %s/%s binds Role %s to %s %s/%s; only %s/%s may hold it", b.Namespace, b.Name, role.Name, s.Kind, s.Namespace, s.Name, releaseNS, operatorAccount)
		}
	}
	if !bound {
		t.Errorf("Role %s/%s is not bound to %s/%s", role.Namespace, role.Name, releaseNS, operatorAccount)
	}
}

func TestAPIRunnerAuthRBAC(t *testing.T) {
	want := []string{
		ruleAtom{group: "", resource: "pods", verb: "get"}.String(),
		ruleAtom{group: "extensions.agents.x-k8s.io", resource: "sandboxclaims", verb: "get"}.String(),
	}
	for _, vs := range valuesSets {
		t.Run(vs.name, func(t *testing.T) {
			objs := render(t, vs.path)
			delegated := false
			for _, u := range objs.ofKind("ClusterRoleBinding") {
				b := convert[rbacv1.ClusterRoleBinding](t, u)
				if b.RoleRef.Kind == "ClusterRole" && b.RoleRef.Name == "system:auth-delegator" &&
					slices.ContainsFunc(b.Subjects, func(s rbacv1.Subject) bool { return isAccount(s, apiAccount) }) {
					delegated = true
				}
			}
			if !delegated {
				t.Errorf("no ClusterRoleBinding to system:auth-delegator with subject %s/%s", releaseNS, apiAccount)
			}

			cfg := renderedConfig(t, objs)
			sandbox := grants(t, objs, scopeLabels(map[string]string{cfg.SandboxNamespace: "sandbox"}))[grantKey{apiAccount, "sandbox"}]
			if got := sortedAtoms(sandbox); !slices.Equal(got, want) {
				t.Errorf("%s rules in sandbox namespace %s = %v, want %v", apiAccount, cfg.SandboxNamespace, got, want)
			}
		})
	}
}

func TestRBACMatchesKustomize(t *testing.T) {
	kustomized := kustomizeDefault(t)
	want := grants(t, kustomized, scopeLabels(map[string]string{
		"skua":                 "run",
		"skua-sandboxes":       "sandbox",
		"agent-sandbox-system": "agent-sandbox",
		"skua-system":          "operator-key",
	}))

	objs := render(t, lintValues)
	cfg := renderedConfig(t, objs)
	got := grants(t, objs, scopeLabels(map[string]string{
		cfg.RunNamespace:                        "run",
		cfg.SandboxNamespace:                    "sandbox",
		cfg.AgentSandbox.Namespace:              "agent-sandbox",
		cfg.Gateway.OperatorKeySecret.Namespace: "operator-key",
	}))

	for _, scope := range []string{"cluster", "run", "sandbox", "agent-sandbox"} {
		if len(want[grantKey{operatorAccount, scope}]) == 0 {
			t.Errorf("config/default binds no rules to %s in scope %s", operatorAccount, scope)
		}
	}

	keys := map[grantKey]bool{}
	for k := range want {
		keys[k] = true
	}
	for k := range got {
		keys[k] = true
	}
	for k := range keys {
		if w, g := sortedAtoms(want[k]), sortedAtoms(got[k]); !slices.Equal(w, g) {
			t.Errorf("%s in scope %s:\nconfig/default: %v\nchart:          %v", k.account, k.scope, w, g)
		}
	}
}

func TestRenderedConfigLoads(t *testing.T) {
	for _, vs := range valuesSets {
		t.Run(vs.name, func(t *testing.T) {
			objs := render(t, vs.path)
			cfg := renderedConfig(t, objs)
			if cfg.RunNamespace != releaseNS {
				t.Errorf("runNamespace = %q, want release namespace %q", cfg.RunNamespace, releaseNS)
			}
			for _, name := range controlPlane {
				if img := skuaContainer(t, deployment(t, objs, name)).Image; cfg.Runner.Image != img {
					t.Errorf("runner.image = %q, Deployment %s image = %q", cfg.Runner.Image, name, img)
				}
			}
		})
	}
}

func chartAppVersion(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(chartDir, "Chart.yaml"))
	if err != nil {
		t.Fatalf("read Chart.yaml: %v", err)
	}
	var chart struct {
		AppVersion string `json:"appVersion"`
	}
	if err := yaml.Unmarshal(data, &chart); err != nil {
		t.Fatalf("parse Chart.yaml: %v", err)
	}
	if chart.AppVersion == "" {
		t.Fatalf("Chart.yaml has no appVersion")
	}
	return chart.AppVersion
}

func TestImageReference(t *testing.T) {
	const (
		repo   = "registry.example.com/team/skua"
		digest = "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	)
	cases := []struct {
		name string
		set  []string
		want string
	}{
		{"digest", []string{"image.repository=" + repo, "image.tag=1.2.3", "image.digest=" + digest}, repo + "@" + digest},
		{"tag", []string{"image.repository=" + repo, "image.tag=1.2.3"}, repo + ":1.2.3"},
		{"appVersion", []string{"image.repository=" + repo}, repo + ":" + chartAppVersion(t)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := render(t, lintValues, tc.set...)
			for _, name := range controlPlane {
				if got := skuaContainer(t, deployment(t, objs, name)).Image; got != tc.want {
					t.Errorf("Deployment %s image = %q, want %q", name, got, tc.want)
				}
			}
			if got := renderedConfig(t, objs).Runner.Image; got != tc.want {
				t.Errorf("runner.image = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAPISingleReplicaRecreate(t *testing.T) {
	for _, vs := range valuesSets {
		t.Run(vs.name, func(t *testing.T) {
			d := deployment(t, render(t, vs.path), apiAccount)
			if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 {
				t.Errorf("replicas = %v, want 1", d.Spec.Replicas)
			}
			if d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
				t.Errorf("strategy.type = %q, want Recreate", d.Spec.Strategy.Type)
			}
		})
	}
}

func TestConfigChangeRollsPods(t *testing.T) {
	checksums := func(objs manifestSet) map[string]string {
		out := map[string]string{}
		for _, name := range controlPlane {
			sum := deployment(t, objs, name).Spec.Template.Annotations["checksum/config"]
			if sum == "" {
				t.Fatalf("Deployment %s has no checksum/config annotation", name)
			}
			out[name] = sum
		}
		return out
	}
	first, _, err := helmTemplate(t, lintValues)
	if err != nil {
		t.Fatalf("helm template: %v", err)
	}
	second, _, err := helmTemplate(t, lintValues)
	if err != nil {
		t.Fatalf("helm template: %v", err)
	}
	base := checksums(decode(t, first, "first render"))
	again := checksums(decode(t, second, "second render"))
	changed := checksums(render(t, lintValues, "config.defaults.runtimeClassName=kata"))
	for _, name := range controlPlane {
		if base[name] != again[name] {
			t.Errorf("Deployment %s checksum/config differs between identical renders: %s, %s", name, base[name], again[name])
		}
		if base[name] == changed[name] {
			t.Errorf("Deployment %s checksum/config unchanged after a config change: %s", name, base[name])
		}
	}
}

func TestRestrictedPods(t *testing.T) {
	for _, vs := range valuesSets {
		t.Run(vs.name, func(t *testing.T) {
			objs := render(t, vs.path)
			for _, name := range controlPlane {
				d := deployment(t, objs, name)
				pod := d.Spec.Template.Spec.SecurityContext
				if pod == nil {
					pod = &corev1.PodSecurityContext{}
				}
				for _, c := range slices.Concat(d.Spec.Template.Spec.InitContainers, d.Spec.Template.Spec.Containers) {
					assertRestricted(t, name, c, pod)
				}
			}

			cfg := renderedConfig(t, objs)
			ns := mustFind[corev1.Namespace](t, objs, "Namespace", "", cfg.SandboxNamespace)
			for _, mode := range []string{"enforce", "warn", "audit"} {
				if got := ns.Labels["pod-security.kubernetes.io/"+mode]; got != "restricted" {
					t.Errorf("Namespace %s label pod-security.kubernetes.io/%s = %q, want restricted", ns.Name, mode, got)
				}
			}

			sa := mustFind[corev1.ServiceAccount](t, objs, "ServiceAccount", cfg.SandboxNamespace, runnerAccount)
			if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
				t.Errorf("ServiceAccount %s/%s automountServiceAccountToken = %v, want false", cfg.SandboxNamespace, runnerAccount, sa.AutomountServiceAccountToken)
			}
		})
	}
}

func assertRestricted(t *testing.T, deployment string, c corev1.Container, pod *corev1.PodSecurityContext) {
	t.Helper()
	sc := c.SecurityContext
	if sc == nil {
		sc = &corev1.SecurityContext{}
	}
	where := deployment + " container " + c.Name
	runAsNonRoot := sc.RunAsNonRoot
	if runAsNonRoot == nil {
		runAsNonRoot = pod.RunAsNonRoot
	}
	if runAsNonRoot == nil || !*runAsNonRoot {
		t.Errorf("%s: runAsNonRoot is not true", where)
	}
	runAsUser := sc.RunAsUser
	if runAsUser == nil {
		runAsUser = pod.RunAsUser
	}
	if runAsUser == nil || *runAsUser != 65532 {
		t.Errorf("%s: runAsUser = %v, want 65532", where, runAsUser)
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Errorf("%s: readOnlyRootFilesystem is not true", where)
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Errorf("%s: allowPrivilegeEscalation is not false", where)
	}
	if sc.Capabilities == nil || !slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) || len(sc.Capabilities.Add) != 0 {
		t.Errorf("%s: capabilities = %+v, want drop [ALL] and no add", where, sc.Capabilities)
	}
	seccomp := sc.SeccompProfile
	if seccomp == nil {
		seccomp = pod.SeccompProfile
	}
	if seccomp == nil || seccomp.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("%s: seccompProfile = %+v, want RuntimeDefault", where, seccomp)
	}
}

func TestSchemaRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		set  string
		key  string
	}{
		{"missing issuerURL", "config.oidc.issuerURL=null", "issuerURL"},
		{"bad CIDR", "config.network.podCIDRs[0]=10.0.0.0/33", "podCIDRs"},
		{"unknown top-level key", "bogusTopLevel=1", "bogusTopLevel"},
		{"unknown config key", "config.bogusConfigKey=1", "bogusConfigKey"},
		{"malformed digest", "image.digest=sha256:abc", "digest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := helmTemplate(t, lintValues, tc.set)
			if err == nil {
				t.Fatalf("helm template --set %s succeeded, want a schema failure", tc.set)
			}
			if !strings.Contains(string(stderr), tc.key) {
				t.Errorf("helm template --set %s stderr does not name %s:\n%s", tc.set, tc.key, stderr)
			}
		})
	}
}

func TestSchemaCoversConfigKeys(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(chartDir, "values.schema.json"))
	if err != nil {
		t.Fatalf("read values.schema.json: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("parse values.schema.json: %v", err)
	}
	s := schemaDoc{root: schema}
	configNode, ok := s.properties(schema)["config"]
	if !ok {
		t.Fatalf("values.schema.json has no config property")
	}
	var missing []string
	s.walk(reflect.TypeFor[config.Config](), configNode, "config", &missing)
	for _, m := range missing {
		t.Errorf("values.schema.json lacks %s", m)
	}
}

type schemaDoc struct {
	root map[string]any
}

func (s schemaDoc) resolve(node any) map[string]any {
	m, _ := node.(map[string]any)
	if ref, ok := m["$ref"].(string); ok {
		var target any = s.root
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			next, _ := target.(map[string]any)
			target = next[part]
		}
		return s.resolve(target)
	}
	return m
}

func (s schemaDoc) properties(node any) map[string]any {
	m := s.resolve(node)
	out := map[string]any{}
	for k, v := range s.mapOf(m["properties"]) {
		out[k] = v
	}
	for _, part := range s.sliceOf(m["allOf"]) {
		for k, v := range s.properties(part) {
			out[k] = v
		}
	}
	return out
}

func (s schemaDoc) items(node any) any {
	m := s.resolve(node)
	if it, ok := m["items"]; ok {
		return it
	}
	for _, part := range s.sliceOf(m["allOf"]) {
		if it := s.items(part); it != nil {
			return it
		}
	}
	return nil
}

func (schemaDoc) mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func (schemaDoc) sliceOf(v any) []any {
	l, _ := v.([]any)
	return l
}

var jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()

func (s schemaDoc) walk(typ reflect.Type, node any, path string, missing *[]string) {
	props := s.properties(node)
	for f := range typ.Fields() {
		key, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if key == "" || key == "-" {
			continue
		}
		child, ok := props[key]
		keyPath := path + "." + key
		if !ok {
			*missing = append(*missing, keyPath)
			continue
		}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Slice {
			ft = ft.Elem()
			child = s.items(child)
			keyPath += "[]"
			if child == nil {
				if ft.Kind() == reflect.Struct {
					*missing = append(*missing, keyPath)
				}
				continue
			}
		}
		if ft.Kind() == reflect.Struct && !reflect.PointerTo(ft).Implements(jsonUnmarshaler) {
			s.walk(ft, child, keyPath, missing)
		}
	}
}

func TestRoutesOnlyWhenEnabled(t *testing.T) {
	defaults := render(t, lintValues)
	for _, kind := range []string{"Ingress", "HTTPRoute"} {
		if n := len(defaults.ofKind(kind)); n != 0 {
			t.Errorf("default render has %d %s, want none", n, kind)
		}
	}

	objs := render(t, fullValues)
	svc := mustFind[corev1.Service](t, objs, "Service", releaseNS, apiAccount)
	httpPort := slices.IndexFunc(svc.Spec.Ports, func(p corev1.ServicePort) bool { return p.Name == "http" })
	if httpPort < 0 {
		t.Fatalf("Service %s has no port named http", svc.Name)
	}
	servicePort := svc.Spec.Ports[httpPort].Port

	ingresses := objs.ofKind("Ingress")
	if len(ingresses) != 1 {
		t.Errorf("full-values render has %d Ingress, want 1", len(ingresses))
	}
	for _, u := range ingresses {
		ing := convert[networkingv1.Ingress](t, u)
		if len(ing.Spec.Rules) == 0 {
			t.Errorf("Ingress %s has no rules", ing.Name)
		}
		for _, r := range ing.Spec.Rules {
			if r.HTTP == nil || len(r.HTTP.Paths) == 0 {
				t.Errorf("Ingress %s rule for host %q has no paths", ing.Name, r.Host)
				continue
			}
			for _, p := range r.HTTP.Paths {
				if s := p.Backend.Service; s == nil || s.Name != svc.Name || s.Port.Name != "http" {
					t.Errorf("Ingress %s path %s backend = %+v, want Service %s port http", ing.Name, p.Path, p.Backend, svc.Name)
				}
			}
		}
	}

	routes := objs.ofKind("HTTPRoute")
	if len(routes) != 1 {
		t.Errorf("full-values render has %d HTTPRoute, want 1", len(routes))
	}
	for _, u := range routes {
		route := convert[httpRoute](t, u)
		if len(route.Spec.Rules) == 0 {
			t.Errorf("HTTPRoute %s has no rules", u.GetName())
		}
		for _, r := range route.Spec.Rules {
			if len(r.BackendRefs) == 0 {
				t.Errorf("HTTPRoute %s rule has no backendRefs", u.GetName())
			}
			for _, ref := range r.BackendRefs {
				if (ref.Kind != "" && ref.Kind != "Service") || ref.Name != svc.Name || ref.Port != servicePort {
					t.Errorf("HTTPRoute %s backendRef = %+v, want Service %s port %d (http)", u.GetName(), ref, svc.Name, servicePort)
				}
			}
		}
	}
}

type httpRoute struct {
	Spec struct {
		Rules []struct {
			BackendRefs []struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
				Port int32  `json:"port"`
			} `json:"backendRefs"`
		} `json:"rules"`
	} `json:"spec"`
}
