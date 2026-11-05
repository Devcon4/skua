package operator_test

import (
	"bytes"
	"context"
	"os"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/operator"
	"github.com/Devcon4/skua/internal/operator/compat"
)

type cachedType struct {
	obj  client.Object
	list client.ObjectList
}

func managerCachedTypes() []cachedType {
	return []cachedType{
		{&v1alpha1.AgentProfile{}, &v1alpha1.AgentProfileList{}},
		{&v1alpha1.AgentRun{}, &v1alpha1.AgentRunList{}},
		{&corev1.Secret{}, &corev1.SecretList{}},
		{&extv1beta1.SandboxTemplate{}, &extv1beta1.SandboxTemplateList{}},
		{&extv1beta1.SandboxWarmPool{}, &extv1beta1.SandboxWarmPoolList{}},
		{&extv1beta1.SandboxClaim{}, &extv1beta1.SandboxClaimList{}},
		{&sandboxv1beta1.Sandbox{}, &sandboxv1beta1.SandboxList{}},
		{&corev1.Pod{}, &corev1.PodList{}},
	}
}

func TestManagerCacheWithOperatorRBAC(t *testing.T) {
	t.Parallel()
	cfg := operatorTestConfig()
	restCfg, admin := envtestClient(t)
	ctx := t.Context()
	roles := managerOperatorRoles(t)
	namespaces := operatorRoleNamespaces(roles, cfg.RunNamespace, cfg.SandboxNamespace)
	for _, ns := range namespaces {
		if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
			t.Fatalf("create namespace %s: %v", ns, err)
		}
	}

	const user = "skua-operator-cache-test"
	bindRolesToUser(ctx, t, admin, roles, user)

	userCfg := rest.CopyConfig(restCfg)
	userCfg.Impersonate = rest.ImpersonationConfig{UserName: user}
	userClient, err := client.New(userCfg, client.Options{Scheme: admin.Scheme()})
	if err != nil {
		t.Fatalf("create client as the operator: %v", err)
	}
	if got := compat.CheckInstalled(ctx, userClient, cfg.AgentSandbox.Namespace); got.State == compat.CompatUnknown {
		t.Errorf("agent-sandbox version check as the operator: %s", got.Message)
	}

	opts := operator.CacheOptions(cfg)
	opts.Scheme = admin.Scheme()
	c, err := cache.New(userCfg, opts)
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	cacheCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Start(cacheCtx) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("cache stopped: %v", err)
		}
	})

	syncCtx, stopSync := context.WithTimeout(ctx, 15*time.Second)
	defer stopSync()
	if !c.WaitForCacheSync(syncCtx) {
		t.Fatal("cache did not start within 15s")
	}

	requireOperatorInformers(ctx, t, c)
}

func operatorRoleNamespaces(roles []*unstructured.Unstructured, base ...string) []string {
	namespaces := base
	for _, role := range roles {
		if ns := role.GetNamespace(); ns != "" && !slices.Contains(namespaces, ns) {
			namespaces = append(namespaces, ns)
		}
	}
	return namespaces
}

func bindRolesToUser(ctx context.Context, t *testing.T, admin client.Client, roles []*unstructured.Unstructured, user string) {
	t.Helper()
	subjects := []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: user}}
	for _, role := range roles {
		if err := admin.Create(ctx, role); err != nil {
			t.Fatalf("create %s %s/%s: %v", role.GetKind(), role.GetNamespace(), role.GetName(), err)
		}
		if err := admin.Create(ctx, roleBinding(role, subjects)); err != nil {
			t.Fatalf("bind %s %s/%s: %v", role.GetKind(), role.GetNamespace(), role.GetName(), err)
		}
	}
}

func roleBinding(role *unstructured.Unstructured, subjects []rbacv1.Subject) client.Object {
	ref := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: role.GetKind(), Name: role.GetName()}
	if role.GetNamespace() != "" {
		return &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: role.GetNamespace(), Name: role.GetName()}, RoleRef: ref, Subjects: subjects}
	}
	return &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: role.GetName()}, RoleRef: ref, Subjects: subjects}
}

func requireOperatorInformers(ctx context.Context, t *testing.T, c cache.Cache) {
	t.Helper()
	for _, ct := range managerCachedTypes() {
		if err := listThroughInformer(ctx, c, ct); err != nil {
			t.Errorf("cached informer of %T as the operator: %v", ct.obj, err)
		}
	}
}

func listThroughInformer(ctx context.Context, c cache.Cache, ct cachedType) error {
	informerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := c.GetInformer(informerCtx, ct.obj); err != nil {
		return err
	}
	return c.List(informerCtx, ct.list)
}

func TestOperatorClusterRoleScope(t *testing.T) {
	cluster := operatorClusterRules(t)
	allowed := map[string]bool{"agentprofiles": true, "agentprofiles/status": true, "agentprofiles/finalizers": true}
	sandboxCRDs := []string{
		"sandboxclaims.extensions.agents.x-k8s.io",
		"sandboxes.agents.x-k8s.io",
		"sandboxtemplates.extensions.agents.x-k8s.io",
		"sandboxwarmpools.extensions.agents.x-k8s.io",
	}
	granted := map[string][]string{}
	for _, rule := range cluster {
		for _, res := range rule.Resources {
			if res == "customresourcedefinitions" {
				if !ruleGetsExactly(rule, sandboxCRDs) {
					t.Errorf("ClusterRole grants %v %v on %v customresourcedefinitions, want get on exactly %v", rule.APIGroups, rule.Verbs, rule.ResourceNames, sandboxCRDs)
				}
				continue
			}
			if !allowed[res] || !slices.Equal(rule.APIGroups, []string{v1alpha1.GroupVersion.Group}) {
				t.Errorf("ClusterRole grants %v %s cluster-wide", rule.APIGroups, res)
			}
			granted[res] = append(granted[res], rule.Verbs...)
		}
	}
	if !slices.Contains(granted["agentprofiles/finalizers"], "update") {
		t.Errorf("ClusterRole grants agentprofiles/finalizers %v, want update", granted["agentprofiles/finalizers"])
	}
}

func operatorClusterRules(t *testing.T) []rbacv1.PolicyRule {
	t.Helper()
	var cluster []rbacv1.PolicyRule
	for _, u := range managerOperatorRoles(t) {
		if u.GetKind() != "ClusterRole" {
			continue
		}
		var role rbacv1.ClusterRole
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &role); err != nil {
			t.Fatalf("decode ClusterRole: %v", err)
		}
		cluster = append(cluster, role.Rules...)
	}
	return cluster
}

func ruleGetsExactly(rule rbacv1.PolicyRule, crds []string) bool {
	return slices.Equal(rule.APIGroups, []string{"apiextensions.k8s.io"}) &&
		slices.Equal(rule.Verbs, []string{"get"}) &&
		slices.Equal(slices.Sorted(slices.Values(rule.ResourceNames)), crds)
}

func managerOperatorRoles(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	data, err := os.ReadFile("../../../config/rbac/role.yaml")
	if err != nil {
		t.Fatalf("read operator RBAC: %v", err)
	}
	var roles []*unstructured.Unstructured
	for doc := range bytes.SplitSeq(data, []byte("\n---\n")) {
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		var u unstructured.Unstructured
		if err := yaml.Unmarshal(doc, &u.Object); err != nil {
			t.Fatalf("parse operator RBAC: %v", err)
		}
		if u.Object == nil {
			continue
		}
		roles = append(roles, &u)
	}
	if len(roles) == 0 {
		t.Fatal("config/rbac/role.yaml holds no roles")
	}
	return roles
}
