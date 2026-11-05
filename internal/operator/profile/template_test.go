package profile

import (
	"reflect"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/sandbox"
)

func testConfig() *config.Config {
	return &config.Config{
		RunNamespace:     "skua",
		SandboxNamespace: "skua-sandboxes",
		Gateway: config.Gateway{
			BaseURL: "http://litellm.gateway.svc:4000",
			Egress:  config.EgressTarget{Namespace: "gateway", PodLabels: map[string]string{"app": "litellm"}, Port: 4000},
		},
		API: config.API{
			RunnerURL: "http://skua-api.skua.svc",
			Egress:    config.EgressTarget{Namespace: "skua", PodLabels: map[string]string{"app.kubernetes.io/name": "skua-api"}, Port: 8080},
		},
		GitHub: &config.GitHub{APIURL: "https://api.github.com", GitURL: "https://github.com"},
		Network: config.Network{
			PodCIDRs:     []string{"10.244.0.0/16"},
			ServiceCIDRs: []string{"10.96.0.0/16"},
			NodeCIDRs:    []string{"172.18.0.0/16", "172.19.0.2/32"},
			ExtraEgress: []config.EgressTarget{
				{Namespace: "skua-e2e", PodLabels: map[string]string{"app": "skua-e2e-forge"}, Port: 8080},
			},
		},
		Defaults: config.Defaults{RuntimeClassName: "gvisor"},
		Runner:   config.Runner{Image: "skua:dev", SupportedVersions: ">=0.1.0 <0.2.0"},
	}
}

func testProfile() *v1alpha1.AgentProfile {
	return &v1alpha1.AgentProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "generic", UID: types.UID("0b6f3c1e-8a52-4d43-9b1a-6a3f7e0c2d11")},
		Spec: v1alpha1.AgentProfileSpec{
			Image:   "skua-generic:dev",
			Adapter: v1alpha1.AdapterOmpRPC,
			Command: []string{"omp", "--mode", "rpc", "--no-ui"},
			Models:  []string{"e2e-primary"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
			},
			Workspace: v1alpha1.ProfileWorkspace{Size: resource.MustParse("5Gi")},
			WarmPool:  v1alpha1.WarmPool{Replicas: ptr.To(int32(2))},
			Forge:     v1alpha1.ForgeGitHub,
			Limits: v1alpha1.Limits{
				BudgetUSD:      resource.MustParse("10"),
				MaxRuntime:     metav1.Duration{Duration: 4 * time.Hour},
				MaxRestarts:    ptr.To(int32(5)),
				MaxResultBytes: 4096,
			},
		},
	}
}

func storageClassProfile() *v1alpha1.AgentProfile {
	p := testProfile()
	p.Name = "pvc"
	p.Spec.Workspace.StorageClassName = ptr.To("standard")
	return p
}

func agentContainer(t *testing.T, tpl *extv1beta1.SandboxTemplate) corev1.Container {
	t.Helper()
	cs := tpl.Spec.PodTemplate.Spec.Containers
	if len(cs) != 1 || cs[0].Name != sandbox.AgentContainerName {
		t.Fatalf("containers = %+v, want one %q", cs, sandbox.AgentContainerName)
	}
	return cs[0]
}

func volume(t *testing.T, tpl *extv1beta1.SandboxTemplate, name string) corev1.Volume {
	t.Helper()
	for _, v := range tpl.Spec.PodTemplate.Spec.Volumes {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("volume %q missing", name)
	return corev1.Volume{}
}

func envValue(c corev1.Container, name string) (string, bool) {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func TestPodSecurity(t *testing.T) {
	spec := Template(testProfile(), testConfig()).Spec.PodTemplate.Spec
	if spec.ServiceAccountName != "skua-runner" || ptr.Deref(spec.AutomountServiceAccountToken, true) {
		t.Errorf("service account = %q automount %v", spec.ServiceAccountName, spec.AutomountServiceAccountToken)
	}
	if ptr.Deref(spec.RuntimeClassName, "") != "gvisor" {
		t.Errorf("runtimeClassName = %v, want gvisor", spec.RuntimeClassName)
	}
	sc := spec.SecurityContext
	if !ptr.Deref(sc.RunAsNonRoot, false) || ptr.Deref(sc.RunAsUser, 0) != 65532 || ptr.Deref(sc.RunAsGroup, 0) != 65532 ||
		ptr.Deref(sc.FSGroup, 0) != 65532 || sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("pod securityContext = %+v", sc)
	}
	for _, c := range slices.Concat(spec.InitContainers, spec.Containers) {
		if c.SecurityContext == nil || ptr.Deref(c.SecurityContext.AllowPrivilegeEscalation, true) ||
			c.SecurityContext.Capabilities == nil || !reflect.DeepEqual(c.SecurityContext.Capabilities.Drop, []corev1.Capability{"ALL"}) {
			t.Errorf("container %s securityContext = %+v", c.Name, c.SecurityContext)
		}
	}
	p := testProfile()
	p.Spec.RuntimeClassName = ptr.To("kata")
	if got := Template(p, testConfig()).Spec.PodTemplate.Spec.RuntimeClassName; ptr.Deref(got, "") != "kata" {
		t.Errorf("profile runtimeClassName = %v, want kata", got)
	}
}

func TestInitContainerInstallsRunner(t *testing.T) {
	spec := Template(testProfile(), testConfig()).Spec.PodTemplate.Spec
	if len(spec.InitContainers) != 1 {
		t.Fatalf("init containers = %+v", spec.InitContainers)
	}
	ic := spec.InitContainers[0]
	if ic.Name != "install-runner" || ic.Image != "skua:dev" || ic.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("init container = %+v", ic)
	}
	if !reflect.DeepEqual(ic.Args, []string{"runner", "install", "/skua-bin"}) || len(ic.Command) != 0 {
		t.Errorf("init command %v args %v", ic.Command, ic.Args)
	}
	if !reflect.DeepEqual(ic.VolumeMounts, []corev1.VolumeMount{{Name: sandbox.VolumeBin, MountPath: "/skua-bin"}}) {
		t.Errorf("init mounts = %+v", ic.VolumeMounts)
	}
}

func TestAgentCommandWrapsProfileCommand(t *testing.T) {
	c := agentContainer(t, Template(testProfile(), testConfig()))
	want := []string{"/skua/bin/skua-runner", "run", "--", "omp", "--mode", "rpc", "--no-ui"}
	if !reflect.DeepEqual(c.Command, want) {
		t.Errorf("command = %v, want %v", c.Command, want)
	}
	if c.Image != "skua-generic:dev" {
		t.Errorf("image = %q", c.Image)
	}
}

func TestAgentEnv(t *testing.T) {
	c := agentContainer(t, Template(testProfile(), testConfig()))
	want := map[string]string{
		"SKUA_API_URL":          "http://skua-api.skua.svc",
		"SKUA_FORGE_GIT_URL":    "https://github.com",
		"SKUA_FORGE_API_URL":    "https://api.github.com",
		"SKUA_PROFILE":          "generic",
		"SKUA_ADAPTER":          "omp-rpc",
		"SKUA_MAX_RESULT_BYTES": "4096",
		"SKUA_MAX_RESTARTS":     "5",
	}
	for name, value := range want {
		if got, ok := envValue(c, name); !ok || got != value {
			t.Errorf("%s = %q (set %v), want %q", name, got, ok, value)
		}
	}
	if len(c.Env) != len(want) {
		t.Errorf("env = %+v, want exactly %v", c.Env, want)
	}
}

func TestAgentMounts(t *testing.T) {
	tpl := Template(testProfile(), testConfig())
	c := agentContainer(t, tpl)
	want := []corev1.VolumeMount{
		{Name: sandbox.VolumeBin, MountPath: "/skua/bin", ReadOnly: true},
		{Name: sandbox.VolumeRun, MountPath: "/skua/run"},
		{Name: sandbox.VolumeWorkspace, MountPath: "/work"},
		{Name: sandbox.VolumeToken, MountPath: "/var/run/secrets/skua", ReadOnly: true},
	}
	if !reflect.DeepEqual(c.VolumeMounts, want) {
		t.Errorf("mounts = %+v, want %+v", c.VolumeMounts, want)
	}
	run := volume(t, tpl, sandbox.VolumeRun)
	if run.EmptyDir == nil || run.EmptyDir.Medium != corev1.StorageMediumMemory || run.EmptyDir.SizeLimit.Cmp(resource.MustParse("64Mi")) != 0 {
		t.Errorf("skua-run = %+v", run.VolumeSource)
	}
	if bin := volume(t, tpl, sandbox.VolumeBin); bin.EmptyDir == nil {
		t.Errorf("skua-bin = %+v", bin.VolumeSource)
	}
	token := volume(t, tpl, sandbox.VolumeToken)
	wantToken := &corev1.ServiceAccountTokenProjection{Audience: "skua-api", ExpirationSeconds: ptr.To(int64(600)), Path: "token"}
	if token.Projected == nil || len(token.Projected.Sources) != 1 || !reflect.DeepEqual(token.Projected.Sources[0].ServiceAccountToken, wantToken) {
		t.Errorf("token volume = %+v", token.VolumeSource)
	}
}

func TestWorkspaceEmptyDir(t *testing.T) {
	tpl := Template(testProfile(), testConfig())
	size := resource.MustParse("5Gi")
	ws := volume(t, tpl, sandbox.VolumeWorkspace)
	if ws.EmptyDir == nil || ws.EmptyDir.SizeLimit == nil || ws.EmptyDir.SizeLimit.Cmp(size) != 0 || ws.Ephemeral != nil {
		t.Fatalf("workspace = %+v", ws.VolumeSource)
	}
	r := agentContainer(t, tpl).Resources
	for name, list := range map[string]corev1.ResourceList{"requests": r.Requests, "limits": r.Limits} {
		if q, ok := list[corev1.ResourceEphemeralStorage]; !ok || q.Cmp(size) != 0 {
			t.Errorf("%s ephemeral-storage = %v, want 5Gi", name, list)
		}
	}
	if q := r.Requests[corev1.ResourceCPU]; q.Cmp(resource.MustParse("500m")) != 0 {
		t.Errorf("profile resources lost: %v", r)
	}
}

func TestWorkspaceStorageClass(t *testing.T) {
	tpl := Template(storageClassProfile(), testConfig())
	ws := volume(t, tpl, sandbox.VolumeWorkspace)
	if ws.EmptyDir != nil || ws.Ephemeral == nil || ws.Ephemeral.VolumeClaimTemplate == nil {
		t.Fatalf("workspace = %+v", ws.VolumeSource)
	}
	vct := ws.Ephemeral.VolumeClaimTemplate
	if len(vct.Labels) != 0 || len(vct.Annotations) != 0 {
		t.Errorf("volumeClaimTemplate metadata = %+v, want none", vct.ObjectMeta)
	}
	if ptr.Deref(vct.Spec.StorageClassName, "") != "standard" || vct.Spec.Resources.Requests.Storage().Cmp(resource.MustParse("5Gi")) != 0 ||
		!reflect.DeepEqual(vct.Spec.AccessModes, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}) {
		t.Errorf("volumeClaimTemplate spec = %+v", vct.Spec)
	}
	r := agentContainer(t, tpl).Resources
	if _, ok := r.Requests[corev1.ResourceEphemeralStorage]; ok {
		t.Errorf("requests = %v, want no ephemeral-storage", r.Requests)
	}
	if _, ok := r.Limits[corev1.ResourceEphemeralStorage]; ok {
		t.Errorf("limits = %v, want no ephemeral-storage", r.Limits)
	}
}

func TestNoSecretReferences(t *testing.T) {
	for _, p := range []*v1alpha1.AgentProfile{testProfile(), storageClassProfile()} {
		spec := Template(p, testConfig()).Spec.PodTemplate.Spec
		for _, v := range spec.Volumes {
			if v.Secret != nil {
				t.Errorf("volume %s references Secret %s", v.Name, v.Secret.SecretName)
			}
			if v.Projected != nil {
				for _, s := range v.Projected.Sources {
					if s.Secret != nil {
						t.Errorf("volume %s projects Secret %s", v.Name, s.Secret.Name)
					}
				}
			}
		}
		for _, c := range slices.Concat(spec.InitContainers, spec.Containers) {
			if len(c.EnvFrom) != 0 {
				t.Errorf("container %s envFrom = %+v", c.Name, c.EnvFrom)
			}
			for _, e := range c.Env {
				if e.ValueFrom != nil {
					t.Errorf("container %s env %s uses valueFrom", c.Name, e.Name)
				}
			}
		}
	}
}
