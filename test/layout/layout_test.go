package layout_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/Devcon4/skua"

var (
	contractRoots  = []string{module + "/internal/contract", module + "/api/v1alpha1"}
	componentRoots = []string{module + "/internal/operator", module + "/internal/api", module + "/internal/runner"}
	sliced         = map[string][]string{
		module + "/internal/operator": {"patch", "condition"},
		module + "/internal/api":      {"auth", "hub", "agentrun"},
	}
	testenvPath     = module + "/internal/testenv"
	integrationRoot = module + "/test/integration"
)

type pkg struct {
	ImportPath string
	Name       string
	Imports    []string
	test       bool
}

func TestLayout(t *testing.T) {
	var violations []string
	for _, p := range listPackages(t) {
		violations = append(violations, check(p)...)
	}
	slices.Sort(violations)
	for _, v := range slices.Compact(violations) {
		t.Error(v)
	}
}

func listPackages(t *testing.T) []pkg {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-test", "-json", "./...")
	cmd.Dir = "../.."
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	var pkgs []pkg
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p pkg
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		p.test = strings.Contains(p.ImportPath, " ") || strings.HasSuffix(p.ImportPath, "_test")
		p.ImportPath = basePath(p.ImportPath)
		if !within(p.ImportPath, module) || strings.HasSuffix(p.ImportPath, ".test") {
			continue
		}
		for i, imp := range p.Imports {
			p.Imports[i] = basePath(imp)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

func basePath(importPath string) string {
	p, _, _ := strings.Cut(importPath, " ")
	return strings.TrimSuffix(p, "_test")
}

func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+"/")
}

func withinAny(p string, roots []string) bool {
	return slices.ContainsFunc(roots, func(r string) bool { return within(p, r) })
}

func check(p pkg) []string {
	var out []string
	for _, imp := range p.Imports {
		if imp == p.ImportPath || !within(imp, module) {
			continue
		}
		if withinAny(p.ImportPath, contractRoots) && withinAny(imp, componentRoots) {
			out = append(out, "contract imports a component: "+p.ImportPath+" imports "+imp)
		}
		if imp == testenvPath && p.ImportPath != testenvPath && !within(p.ImportPath, integrationRoot) {
			out = append(out, "testenv imported outside test/integration: "+p.ImportPath)
		}
		if v := sliceImport(p.ImportPath, imp); v != "" {
			out = append(out, v)
		}
	}
	if v := fakeName(p); v != "" {
		out = append(out, v)
	}
	return out
}

func sliceImport(from, imp string) string {
	for root, support := range sliced {
		slice := sliceOf(root, support, from)
		if slice == "" || within(imp, slice) {
			continue
		}
		switch {
		case imp == root:
			return "slice imports its root: " + from + " imports " + imp
		case sliceOf(root, support, imp) != "":
			return "slice imports a sibling slice: " + from + " imports " + imp
		case withinAny(imp, componentRoots) && !slices.ContainsFunc(support, func(s string) bool { return within(imp, root+"/"+s) }):
			return "slice imports a package outside its support list: " + from + " imports " + imp
		}
	}
	return ""
}

func sliceOf(root string, support []string, p string) string {
	rest, ok := strings.CutPrefix(p, root+"/")
	if !ok {
		return ""
	}
	first, _, _ := strings.Cut(rest, "/")
	if slices.Contains(support, first) {
		return ""
	}
	return root + "/" + first
}

func fakeName(p pkg) string {
	if p.test || p.Name == "main" || p.ImportPath == testenvPath || !within(p.ImportPath, module+"/internal") {
		return ""
	}
	if !strings.Contains(p.Name, "fake") && !strings.Contains(p.ImportPath, "/testing/") && !slices.Contains(p.Imports, "testing") {
		return ""
	}
	if !strings.HasSuffix(p.Name, "test") {
		return "fake package name does not end in test: " + p.ImportPath + " (package " + p.Name + ")"
	}
	if parent := path.Base(path.Dir(p.ImportPath)); p.Name != parent+"test" {
		return "fake package is not <pkg>/<pkg>test: " + p.ImportPath + " (package " + p.Name + ")"
	}
	return ""
}
