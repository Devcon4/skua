package pins_test

import (
	"slices"
	"testing"

	"github.com/Devcon4/skua/test/pins"
)

func check(t *testing.T, root string) []string {
	t.Helper()
	findings, err := pins.Check(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range findings {
		out = append(out, f.String())
	}
	return out
}

func TestRepoIsPinned(t *testing.T) {
	for _, f := range check(t, "../..") {
		t.Error(f)
	}
}

func TestGoodFixturesPass(t *testing.T) {
	for _, f := range check(t, "testdata/good") {
		t.Error(f)
	}
}

func TestBadFixturesFail(t *testing.T) {
	want := []string{
		"Dockerfile:1: FROM golang:1.26 is not pinned by @sha256: digest",
		"docs/install/examples/agentprofile.yaml:7: image: ghcr.io/devcon4/skua-agent-generic:<version> is not pinned by @sha256: digest",
		"mise.toml:3: oh-my-pi 18.15.0 differs from extension/omp-range.json min 18.16.0",
		"test/fakes/llm/Dockerfile:1: FROM debian:bookworm-slim is not pinned by @sha256: digest",
		"test/kindenv/manifests/app.yaml:3: image: busybox:1.37 is not pinned by @sha256: digest",
		"test/kindenv/manifests/dex-values.yaml:3: image.tag v2.46.1 has no image.digest",
		"test/kindenv/versions.go:4: image busybox:1.37 is not pinned by @sha256: digest",
	}
	if got := check(t, "testdata/bad"); !slices.Equal(got, want) {
		t.Errorf("findings:\n%q\nwant:\n%q", got, want)
	}
}
