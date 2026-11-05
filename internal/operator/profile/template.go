package profile

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/sandbox"
)

const (
	// installDir differs from BinDir because the skua image holds its binary at the file /skua.
	installDir = "/skua-bin"

	nonRootID              = int64(65532)
	tokenExpirationSeconds = int64(600)
)

var (
	runVolumeSize        = resource.MustParse("64Mi")
	defaultWorkspaceSize = resource.MustParse("10Gi")
)

// Template renders the SandboxTemplate of p in cfg.SandboxNamespace, owned by p.
func Template(p *v1alpha1.AgentProfile, cfg *config.Config) *extv1beta1.SandboxTemplate {
	return &extv1beta1.SandboxTemplate{
		TypeMeta:   metav1.TypeMeta{APIVersion: extv1beta1.GroupVersion.String(), Kind: "SandboxTemplate"},
		ObjectMeta: profileObjectMeta(p, cfg),
		Spec: extv1beta1.SandboxTemplateSpec{
			SandboxBlueprint: sandboxv1beta1.SandboxBlueprint{
				PodTemplate: sandboxv1beta1.PodTemplate{
					ObjectMeta: sandboxv1beta1.PodMetadata{Labels: map[string]string{sandbox.LabelProfile: p.Name}},
					Spec:       podSpec(p, cfg),
				},
			},
			NetworkPolicy:              networkPolicy(cfg),
			NetworkPolicyManagement:    extv1beta1.NetworkPolicyManagementManaged,
			EnvVarsInjectionPolicy:     extv1beta1.EnvVarsInjectionPolicyDisallowed,
			VolumeClaimTemplatesPolicy: extv1beta1.VolumeClaimTemplatesPolicyDisallowed,
		},
	}
}

func profileObjectMeta(p *v1alpha1.AgentProfile, cfg *config.Config) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:            sandbox.Name(p.Name),
		Namespace:       cfg.SandboxNamespace,
		Labels:          map[string]string{sandbox.LabelProfile: p.Name},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(p, v1alpha1.GroupVersion.WithKind("AgentProfile"))},
	}
}

func podSpec(p *v1alpha1.AgentProfile, cfg *config.Config) corev1.PodSpec {
	runtimeClass := ptr.Deref(p.Spec.RuntimeClassName, cfg.Defaults.RuntimeClassName)
	var runtimeClassName *string
	if runtimeClass != "" {
		runtimeClassName = &runtimeClass
	}
	workspace, emptyDirWorkspace := workspaceVolume(p)
	resources := *p.Spec.Resources.DeepCopy()
	if emptyDirWorkspace {
		size := workspaceSize(p)
		resources.Requests = withQuantity(resources.Requests, corev1.ResourceEphemeralStorage, size)
		resources.Limits = withQuantity(resources.Limits, corev1.ResourceEphemeralStorage, size)
	}
	return corev1.PodSpec{
		ServiceAccountName:           sandbox.ServiceAccountName,
		AutomountServiceAccountToken: ptr.To(false),
		RuntimeClassName:             runtimeClassName,
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   ptr.To(true),
			RunAsUser:      ptr.To(nonRootID),
			RunAsGroup:     ptr.To(nonRootID),
			FSGroup:        ptr.To(nonRootID),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		InitContainers: []corev1.Container{{
			Name:            sandbox.InitContainerName,
			Image:           cfg.Runner.Image,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Args:            []string{"runner", "install", installDir},
			VolumeMounts:    []corev1.VolumeMount{{Name: sandbox.VolumeBin, MountPath: installDir}},
			SecurityContext: containerSecurityContext(),
		}},
		Containers: []corev1.Container{{
			Name:      sandbox.AgentContainerName,
			Image:     p.Spec.Image,
			Command:   append([]string{sandbox.RunnerPath, "run", "--"}, p.Spec.Command...),
			Env:       agentEnv(p, cfg),
			Resources: resources,
			VolumeMounts: []corev1.VolumeMount{
				{Name: sandbox.VolumeBin, MountPath: sandbox.BinDir, ReadOnly: true},
				{Name: sandbox.VolumeRun, MountPath: sandbox.RunDir},
				{Name: sandbox.VolumeWorkspace, MountPath: sandbox.WorkDir},
				{Name: sandbox.VolumeToken, MountPath: sandbox.TokenDir, ReadOnly: true},
			},
			SecurityContext: containerSecurityContext(),
		}},
		Volumes: []corev1.Volume{
			{Name: sandbox.VolumeBin, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			{Name: sandbox.VolumeRun, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
				Medium:    corev1.StorageMediumMemory,
				SizeLimit: ptr.To(runVolumeSize.DeepCopy()),
			}}},
			workspace,
			{Name: sandbox.VolumeToken, VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
					Audience:          sandbox.TokenAudience,
					ExpirationSeconds: ptr.To(tokenExpirationSeconds),
					Path:              sandbox.TokenFile,
				}}},
			}}},
		},
	}
}

func containerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

func workspaceSize(p *v1alpha1.AgentProfile) resource.Quantity {
	if p.Spec.Workspace.Size.IsZero() {
		return defaultWorkspaceSize.DeepCopy()
	}
	return p.Spec.Workspace.Size.DeepCopy()
}

func workspaceVolume(p *v1alpha1.AgentProfile) (corev1.Volume, bool) {
	size := workspaceSize(p)
	if p.Spec.Workspace.StorageClassName == nil {
		return corev1.Volume{Name: sandbox.VolumeWorkspace, VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &size},
		}}, true
	}
	return corev1.Volume{Name: sandbox.VolumeWorkspace, VolumeSource: corev1.VolumeSource{
		Ephemeral: &corev1.EphemeralVolumeSource{VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: ptr.To(*p.Spec.Workspace.StorageClassName),
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: size},
				},
			},
		}},
	}}, false
}

func withQuantity(list corev1.ResourceList, name corev1.ResourceName, q resource.Quantity) corev1.ResourceList {
	if list == nil {
		list = corev1.ResourceList{}
	}
	list[name] = q.DeepCopy()
	return list
}

func agentEnv(p *v1alpha1.AgentProfile, cfg *config.Config) []corev1.EnvVar {
	adapter := p.Spec.Adapter
	if adapter == "" {
		adapter = v1alpha1.AdapterOmpRPC
	}
	maxResultBytes := p.Spec.Limits.MaxResultBytes
	if maxResultBytes == 0 {
		maxResultBytes = sandbox.DefaultMaxResultBytes
	}
	env := []corev1.EnvVar{
		{Name: sandbox.EnvAPIURL, Value: cfg.API.RunnerURL},
	}
	if cfg.GitHub != nil {
		env = append(env,
			corev1.EnvVar{Name: sandbox.EnvForgeGitURL, Value: cfg.GitHub.GitURL},
			corev1.EnvVar{Name: sandbox.EnvForgeAPIURL, Value: cfg.GitHub.APIURL},
		)
	}
	return append(env,
		corev1.EnvVar{Name: sandbox.EnvProfile, Value: p.Name},
		corev1.EnvVar{Name: sandbox.EnvAdapter, Value: string(adapter)},
		corev1.EnvVar{Name: sandbox.EnvMaxResultBytes, Value: strconv.FormatInt(maxResultBytes, 10)},
		corev1.EnvVar{Name: sandbox.EnvMaxRestarts, Value: strconv.FormatInt(int64(ptr.Deref(p.Spec.Limits.MaxRestarts, sandbox.DefaultMaxRestarts)), 10)},
	)
}
