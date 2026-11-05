package runnerapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/Masterminds/semver/v3"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/agentrun"
	"github.com/Devcon4/skua/internal/api/auth"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
)

// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups="",namespace=skua-sandboxes,resources=pods,verbs=get
// +kubebuilder:rbac:groups=extensions.agents.x-k8s.io,namespace=skua-sandboxes,resources=sandboxclaims,verbs=get

func (rr *Routes) authenticate(w http.ResponseWriter, r *http.Request) (*corev1.Pod, bool) {
	pod, err := rr.reviewPod(r)
	if err == nil {
		err = rr.checkVersion(r, pod)
	}
	if err != nil {
		var ref *refusal
		if !errors.As(err, &ref) {
			ref = &refusal{http.StatusInternalServerError, wire.RefusalInternalError, err.Error()}
		}
		refuse(w, ref)
		return nil, false
	}
	return pod, true
}

func (rr *Routes) reviewPod(r *http.Request) (*corev1.Pod, error) {
	token, ok := auth.BearerToken(r)
	if !ok {
		return nil, &refusal{http.StatusUnauthorized, wire.RefusalTokenInvalid, "no bearer token"}
	}
	review, err := rr.deps.TokenReviews.Create(r.Context(), &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token, Audiences: []string{sandbox.TokenAudience}},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, &refusal{http.StatusServiceUnavailable, wire.RefusalTokenReviewUnavailable, err.Error()}
	}
	st := review.Status
	switch {
	case !st.Authenticated:
		return nil, &refusal{http.StatusUnauthorized, wire.RefusalTokenInvalid, "token not authenticated: " + st.Error}
	case !slices.Contains(st.Audiences, sandbox.TokenAudience):
		return nil, &refusal{http.StatusUnauthorized, wire.RefusalTokenInvalid, "token audience is not " + sandbox.TokenAudience}
	}
	podName, podUID := auth.Extra(st.User, auth.ExtraPodName), auth.Extra(st.User, auth.ExtraPodUID)
	if podName == "" || podUID == "" {
		return nil, &refusal{http.StatusUnauthorized, wire.RefusalTokenInvalid, "token is not bound to a pod"}
	}
	ns := auth.ServiceAccountNamespace(st.User.Username)
	if ns != rr.deps.Config.SandboxNamespace {
		return nil, &refusal{http.StatusForbidden, wire.RefusalPodMismatch, "pod is not in namespace " + rr.deps.Config.SandboxNamespace}
	}
	var pod corev1.Pod
	key := client.ObjectKey{Namespace: rr.deps.Config.SandboxNamespace, Name: podName}
	if err := rr.deps.APIReader.Get(r.Context(), key, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &refusal{http.StatusUnauthorized, wire.RefusalTokenInvalid, "pod " + podName + " no longer exists"}
		}
		return nil, fmt.Errorf("get pod %s: %w", key, err)
	}
	if string(pod.UID) != podUID {
		return nil, &refusal{http.StatusForbidden, wire.RefusalPodMismatch, "pod " + podName + " has another uid"}
	}
	return &pod, nil
}

func (rr *Routes) checkVersion(r *http.Request, pod *corev1.Pod) error {
	detail := rr.unsupportedRunnerVersion(r.Header.Get(wire.HeaderRunnerVersion))
	if detail == "" {
		return nil
	}
	run, err := rr.runForPod(r.Context(), pod)
	if err != nil {
		return err
	}
	if run != nil {
		err := agentrun.Finish(r.Context(), rr.deps.Client, rr.deps.APIReader, rr.deps.Clock, run, func(st *v1alpha1.AgentRunStatus) {
			st.Phase, st.Reason, st.Message = v1alpha1.PhaseFailed, v1alpha1.ReasonRunnerUnsupported, detail
		})
		if err != nil {
			return err
		}
	}
	return &refusal{http.StatusBadRequest, v1alpha1.ReasonRunnerUnsupported, detail}
}

func (rr *Routes) unsupportedRunnerVersion(header string) string {
	if header == "" {
		return wire.HeaderRunnerVersion + " header missing"
	}
	v, err := semver.NewVersion(header)
	if err != nil {
		return fmt.Sprintf("runner version %q is not semver", header)
	}
	if !rr.versions.Check(v) {
		return fmt.Sprintf("runner version %s is outside %s", header, rr.deps.Config.Runner.SupportedVersions)
	}
	return ""
}

func (rr *Routes) callerRun(ctx context.Context, w http.ResponseWriter, pod *corev1.Pod, want string) (*v1alpha1.AgentRun, bool) {
	run, err := rr.runForPod(ctx, pod)
	if err != nil {
		refuse(w, &refusal{http.StatusInternalServerError, wire.RefusalInternalError, err.Error()})
		return nil, false
	}
	if run == nil || want == "" || run.Name != want {
		refuse(w, &refusal{http.StatusForbidden, wire.RefusalRunMismatch, fmt.Sprintf("run %q is not the run of pod %s", want, pod.Name)})
		return nil, false
	}
	return run, true
}

func (rr *Routes) runForPod(ctx context.Context, pod *corev1.Pod) (*v1alpha1.AgentRun, error) {
	cfg := rr.deps.Config
	return sandbox.RunForPod(ctx, rr.deps.APIReader, rr.deps.Client, cfg.SandboxNamespace, cfg.RunNamespace, pod)
}
