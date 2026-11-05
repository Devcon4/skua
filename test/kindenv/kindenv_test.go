package kindenv

import (
	"strings"
	"testing"
)

func TestAddDexRewrite(t *testing.T) {
	in := ".:53 {\n    errors\n    health {\n       lameduck 5s\n    }\n    ready\n    kubernetes cluster.local\n}\n"
	want := ".:53 {\n    errors\n    health {\n       lameduck 5s\n    }\n    ready\n    rewrite name dex.127.0.0.1.nip.io dex.dex.svc.cluster.local\n    kubernetes cluster.local\n}\n"
	if got := addDexRewrite(in); got != want {
		t.Errorf("addDexRewrite:\n%s\nwant:\n%s", got, want)
	}
}

func TestParseMiB(t *testing.T) {
	for in, want := range map[string]float64{"512KiB": 0.5, "1.5MiB": 1.5, "2GiB": 2048, "1048576B": 1} {
		if got, ok := parseMiB(in); !ok || got != want {
			t.Errorf("parseMiB(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "MiB", "12XB"} {
		if _, ok := parseMiB(in); ok {
			t.Errorf("parseMiB(%q) parsed", in)
		}
	}
}

func TestDeploymentImagesReadsLiteLLMManifest(t *testing.T) {
	raw, err := manifests.ReadFile("manifests/litellm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	images, err := deploymentImages(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 || !strings.HasPrefix(images[0], "postgres:") || !strings.HasPrefix(images[1], "ghcr.io/berriai/litellm-database:") {
		t.Errorf("images = %v; want the postgres image, then the litellm image", images)
	}
}

func TestDeploymentImagesSkipsEmptyDocuments(t *testing.T) {
	images, err := deploymentImages([]byte("---\n# Source: chart/templates/empty.yaml\n---\napiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n        - image: busybox:1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0] != "busybox:1" {
		t.Errorf("images = %v; want [busybox:1]", images)
	}
}
