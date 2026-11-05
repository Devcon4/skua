package compat

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	compatNamespace  = "agent-sandbox-system"
	compatRepo       = "registry.k8s.io/agent-sandbox/agent-sandbox-controller"
	compatDigest     = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	compatClaimsCRD  = "sandboxclaims.extensions.agents.x-k8s.io"
	compatSandboxCRD = "sandboxes.agents.x-k8s.io"
)

type installedCase struct {
	name         string
	images       []string
	skipCRD      string
	alphaOnlyCRD string
	forbidList   bool
	want         CompatState
	wantInMsg    []string
}

func TestCheckInstalled(t *testing.T) {
	tests := []installedCase{
		{name: "v1.0.9", images: []string{compatRepo + ":v1.0.9"}, want: CompatSupported, wantInMsg: []string{"v1.0.9"}},
		{name: "v1.0.12", images: []string{compatRepo + ":v1.0.12"}, want: CompatSupported, wantInMsg: []string{"v1.0.12"}},
		{name: "v1.0.8", images: []string{compatRepo + ":v1.0.8"}, want: CompatUnsupported, wantInMsg: []string{"v1.0.8", MinAgentSandboxVersion, MaxAgentSandboxVersion}},
		{name: "v1.1.0", images: []string{compatRepo + ":v1.1.0"}, want: CompatUnsupported, wantInMsg: []string{"v1.1.0"}},
		{name: "tag without v", images: []string{compatRepo + ":1.0.9"}, want: CompatSupported},
		{name: "tag and digest", images: []string{compatRepo + ":v1.0.9" + compatDigest}, want: CompatSupported, wantInMsg: []string{"v1.0.9"}},
		{name: "digest only", images: []string{compatRepo + compatDigest}, want: CompatUnknown, wantInMsg: []string{compatRepo + compatDigest}},
		{name: "unparsable tag", images: []string{compatRepo + ":latest"}, want: CompatUnknown, wantInMsg: []string{":latest"}},
		{name: "registry with port", images: []string{"localhost:5000/agent-sandbox-controller:v1.0.9"}, want: CompatSupported},
		{name: "two controllers, one too old", images: []string{compatRepo + ":v1.0.9", compatRepo + ":v1.0.8"}, want: CompatUnsupported, wantInMsg: []string{"v1.0.8", "v1.0.9"}},
		{name: "no controller", images: []string{"registry.k8s.io/other-controller:v1.0.9"}, want: CompatUnsupported, wantInMsg: []string{"no agent-sandbox controller Deployment in namespace " + compatNamespace}},
		{name: "sandboxclaims CRD missing", images: []string{compatRepo + ":v1.0.9"}, skipCRD: compatClaimsCRD, want: CompatUnsupported, wantInMsg: []string{compatClaimsCRD, "controller.extensions=true"}},
		{name: "CRD serves only v1alpha1", images: []string{compatRepo + ":v1.0.9"}, alphaOnlyCRD: compatSandboxCRD, want: CompatUnsupported, wantInMsg: []string{compatSandboxCRD, "v1beta1"}},
		{name: "deployment list forbidden", images: []string{compatRepo + ":v1.0.9"}, forbidList: true, want: CompatUnknown, wantInMsg: []string{"forbidden"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(compatScheme(t)).WithObjects(tt.objects()...)
			if tt.forbidList {
				b = b.WithInterceptorFuncs(interceptor.Funcs{List: forbidDeploymentList})
			}

			got := CheckInstalled(t.Context(), b.Build(), compatNamespace)

			if got.State != tt.want {
				t.Fatalf("state = %s, want %s (message %q)", got.State, tt.want, got.Message)
			}
			for _, s := range tt.wantInMsg {
				if !strings.Contains(got.Message, s) {
					t.Errorf("message %q does not contain %q", got.Message, s)
				}
			}
		})
	}
}

func (tt installedCase) objects() []client.Object {
	var objs []client.Object
	for _, name := range agentSandboxCRDs {
		if name == tt.skipCRD {
			continue
		}
		version := agentSandboxServedVersion
		if name == tt.alphaOnlyCRD {
			version = "v1alpha1"
		}
		objs = append(objs, compatCRD(name, version))
	}
	for i, image := range tt.images {
		objs = append(objs, compatDeployment(i, image))
	}
	return objs
}

func forbidDeploymentList(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*appsv1.DeploymentList); ok {
		return apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "", nil)
	}
	return c.List(ctx, list, opts...)
}

func compatScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("build scheme: %v", err)
	}
	scheme.AddKnownTypeWithName(crdGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(crdGVK.GroupVersion().WithKind(crdGVK.Kind+"List"), &unstructured.UnstructuredList{})
	return scheme
}

func compatCRD(name, version string) *unstructured.Unstructured {
	crd := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"versions": []any{map[string]any{"name": version, "served": true, "storage": true}},
		},
	}}
	crd.SetGroupVersionKind(crdGVK)
	crd.SetName(name)
	return crd
}

func compatDeployment(i int, image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "controller-" + string(rune('a'+i)), Namespace: compatNamespace},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "controller", Image: image}}}},
		},
	}
}
