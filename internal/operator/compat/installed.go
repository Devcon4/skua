// Package compat checks that the installed agent-sandbox controller is a supported version.
package compat

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The supported agent-sandbox controller range: MinAgentSandboxVersion inclusive, MaxAgentSandboxVersion exclusive.
const (
	MinAgentSandboxVersion = "v1.0.9"
	MaxAgentSandboxVersion = "v1.1.0"
)

type CompatState string

const (
	CompatSupported   CompatState = "Supported"
	CompatUnsupported CompatState = "Unsupported"
	CompatUnknown     CompatState = "Unknown"
)

// Compat is the result of CheckInstalled. Versions holds the controller image tags found, sorted and
// deduplicated; Message says what was found and, when not Supported, what to fix.
type Compat struct {
	State    CompatState
	Versions []string
	Message  string
}

const (
	agentSandboxServedVersion   = "v1beta1"
	agentSandboxControllerImage = "agent-sandbox-controller"
	agentSandboxExtensionsGroup = "extensions.agents.x-k8s.io"
)

var (
	agentSandboxCRDs = []string{
		"sandboxes.agents.x-k8s.io",
		"sandboxtemplates." + agentSandboxExtensionsGroup,
		"sandboxwarmpools." + agentSandboxExtensionsGroup,
		"sandboxclaims." + agentSandboxExtensionsGroup,
	}
	crdGVK = schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}

	minAgentSandbox = semver.MustParse(MinAgentSandboxVersion)
	maxAgentSandbox = semver.MustParse(MaxAgentSandboxVersion)
)

// CheckInstalled reports whether the agent-sandbox installed on the cluster is in the supported range. It
// reads the four agent-sandbox CRDs and the controller Deployments in namespace through r. A missing or
// unserved CRD, no controller, or a controller version outside the range is CompatUnsupported; a read
// error or a controller image without a semver tag is CompatUnknown.
func CheckInstalled(ctx context.Context, r client.Reader, namespace string) Compat {
	if c, done := checkCRDs(ctx, r); done {
		return c
	}
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments, client.InNamespace(namespace)); err != nil {
		return Compat{State: CompatUnknown, Message: fmt.Sprintf("list Deployments in namespace %s: %v", namespace, err)}
	}
	var images []string
	for _, d := range deployments.Items {
		for _, c := range d.Spec.Template.Spec.Containers {
			if repo, _ := splitImage(c.Image); repo[strings.LastIndex(repo, "/")+1:] == agentSandboxControllerImage {
				images = append(images, c.Image)
			}
		}
	}
	if len(images) == 0 {
		return Compat{State: CompatUnsupported, Message: "no agent-sandbox controller Deployment in namespace " + namespace}
	}
	return checkVersions(images)
}

func checkCRDs(ctx context.Context, r client.Reader) (Compat, bool) {
	var problems []string
	missingExtensions := false
	for _, name := range agentSandboxCRDs {
		crd := &unstructured.Unstructured{}
		crd.SetGroupVersionKind(crdGVK)
		if err := r.Get(ctx, client.ObjectKey{Name: name}, crd); err != nil {
			if !apierrors.IsNotFound(err) {
				return Compat{State: CompatUnknown, Message: fmt.Sprintf("get CRD %s: %v", name, err)}, true
			}
			problems = append(problems, "CRD "+name+" is not installed")
			missingExtensions = missingExtensions || strings.HasSuffix(name, "."+agentSandboxExtensionsGroup)
			continue
		}
		if !servesVersion(crd, agentSandboxServedVersion) {
			problems = append(problems, fmt.Sprintf("CRD %s does not serve %s", name, agentSandboxServedVersion))
		}
	}
	if len(problems) == 0 {
		return Compat{}, false
	}
	msg := "agent-sandbox " + strings.Join(problems, "; ")
	if missingExtensions {
		msg += "; install agent-sandbox with controller.extensions=true"
	}
	return Compat{State: CompatUnsupported, Message: msg}, true
}

func servesVersion(crd *unstructured.Unstructured, version string) bool {
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	for _, v := range versions {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if name, _, _ := unstructured.NestedString(m, "name"); name != version {
			continue
		}
		served, _, _ := unstructured.NestedBool(m, "served")
		return served
	}
	return false
}

func checkVersions(images []string) Compat {
	var versions, outside, unparsable []string
	for _, image := range images {
		_, tag := splitImage(image)
		v, err := semver.NewVersion(tag)
		if tag == "" || err != nil {
			unparsable = append(unparsable, image)
			continue
		}
		versions = append(versions, tag)
		if v.LessThan(minAgentSandbox) || !v.LessThan(maxAgentSandbox) {
			outside = append(outside, tag)
		}
	}
	slices.Sort(versions)
	versions = slices.Compact(versions)
	supported := fmt.Sprintf(">= %s and < %s", MinAgentSandboxVersion, MaxAgentSandboxVersion)
	switch {
	case len(outside) > 0:
		return Compat{
			State:    CompatUnsupported,
			Versions: versions,
			Message:  fmt.Sprintf("agent-sandbox controller versions %s found; skua supports %s", strings.Join(versions, ", "), supported),
		}
	case len(unparsable) > 0:
		return Compat{
			State:    CompatUnknown,
			Versions: versions,
			Message:  "agent-sandbox controller image has no semver tag: " + strings.Join(unparsable, ", "),
		}
	}
	return Compat{
		State:    CompatSupported,
		Versions: versions,
		Message:  fmt.Sprintf("agent-sandbox controller versions %s are in the supported range %s", strings.Join(versions, ", "), supported),
	}
}

func splitImage(image string) (repo, tag string) {
	repo, _, _ = strings.Cut(image, "@")
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo, tag = repo[:i], repo[i+1:]
	}
	return repo, tag
}
