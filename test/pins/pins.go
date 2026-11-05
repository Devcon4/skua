// Package pins finds container images that are not pinned by digest and an omp version that
// drifts from the extension's tested range.
package pins

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Finding is one unpinned reference, at a slash-separated path relative to the checked root.
type Finding struct {
	File    string
	Line    int
	Message string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s", f.File, f.Line, f.Message)
}

const localTag = "dev"

var (
	digestRe            = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	placeholderDigestRe = regexp.MustCompile(`@sha256:<[^<>]+>$`)
	envsubstRe          = regexp.MustCompile(`\$\{?[A-Za-z_]`)
	imageRefRe          = regexp.MustCompile(`^([a-z0-9.-]+(:[0-9]+)?/)?[a-z0-9]+([._/-][a-z0-9]+)*:[A-Za-z0-9_][A-Za-z0-9._-]*(@sha256:[0-9a-f]{64})?$`)
	commentRe           = regexp.MustCompile(`\s#`)
	fromRe              = regexp.MustCompile(`^\s*[Ff][Rr][Oo][Mm]\s+(.*)$`)
	contentRe           = regexp.MustCompile(`^(\s*)[^\s#]`)
	imageKeyRe          = regexp.MustCompile(`^(\s*(-\s+)?)image:\s*(.*)$`)
	tagKeyRe            = regexp.MustCompile(`^\s*tag:\s*(.*)$`)
	digestKeyRe         = regexp.MustCompile(`^\s*digest:\s*(.*)$`)
	ompPinRe            = regexp.MustCompile(`^\s*"?oh-my-pi"?\s*=\s*"([^"]*)"`)
)

type mode int

const (
	dockerfile mode = iota + 1
	goConstants
	values
	docs
)

var dirs = []string{"images", "test/fakes", "test/kindenv", "test/install", "charts/skua/ci", "docs/install/examples"}

// Check reports every unpinned image in the Dockerfiles, the Helm values, the kind environment's
// and the install checks' manifests and Go pins, and the install docs examples under root, plus a mise.toml omp pin that
// differs from extension/omp-range.json min. Findings are sorted by file and line. An error means
// a file could not be read or parsed, or mise.toml has no omp pin.
func Check(root string) ([]Finding, error) {
	files := map[string]mode{}
	if _, err := os.Stat(filepath.Join(root, "Dockerfile")); err == nil {
		files["Dockerfile"] = dockerfile
	}
	fsys := os.DirFS(root)
	for _, dir := range dirs {
		err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if m := classify(p); !d.IsDir() && m != 0 {
				files[p] = m
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}

	var out []Finding
	for file, m := range files {
		raw, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, err
		}
		switch m {
		case dockerfile:
			out = append(out, checkDockerfile(file, lines(raw))...)
		case goConstants:
			found, err := checkGoConstants(file, raw)
			if err != nil {
				return nil, err
			}
			out = append(out, found...)
		case values, docs:
			out = append(out, checkYAML(file, lines(raw), m)...)
		}
	}
	omp, err := checkOmpVersion(root)
	if err != nil {
		return nil, err
	}
	out = append(out, omp...)
	slices.SortFunc(out, func(a, b Finding) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line))
	})
	return out, nil
}

func classify(p string) mode {
	match := func(pattern string) bool {
		ok, _ := path.Match(pattern, p)
		return ok
	}
	yaml := path.Ext(p) == ".yaml" || path.Ext(p) == ".yml"
	switch {
	case match("images/*/Dockerfile"), match("test/fakes/*/Dockerfile"):
		return dockerfile
	case p == "test/kindenv/versions.go", p == "test/install/versions_test.go":
		return goConstants
	case yaml && (match("test/kindenv/manifests/*") || match("test/install/testdata/*") || match("charts/skua/ci/*")):
		return values
	case yaml && strings.HasPrefix(p, "docs/install/examples/"):
		return docs
	}
	return 0
}

func lines(raw []byte) []string {
	return strings.Split(string(raw), "\n")
}

func scalar(v string) string {
	if loc := commentRe.FindStringIndex(v); loc != nil {
		v = v[:loc[0]]
	}
	v = strings.TrimRightFunc(v, unicode.IsSpace)
	v = strings.TrimSuffix(strings.TrimPrefix(v, `"`), `"`)
	return strings.TrimSuffix(strings.TrimPrefix(v, "'"), "'")
}

func checkDockerfile(file string, lines []string) []Finding {
	var out []Finding
	stages := map[string]bool{}
	for i, line := range lines {
		m := fromRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		words := strings.Fields(m[1])
		w := 0
		for w < len(words) && strings.HasPrefix(words[w], "--") {
			w++
		}
		if w >= len(words) {
			continue
		}
		ref := words[w]
		if w+2 < len(words) && strings.EqualFold(words[w+1], "as") {
			stages[strings.ToLower(words[w+2])] = true
		}
		if strings.EqualFold(ref, "scratch") || stages[strings.ToLower(ref)] {
			continue
		}
		if !digestRe.MatchString(ref) {
			out = append(out, Finding{file, i + 1, fmt.Sprintf("FROM %s is not pinned by @sha256: digest", ref)})
		}
	}
	return out
}

func checkGoConstants(file string, raw []byte) ([]Finding, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, raw, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		for _, spec := range gen.Specs {
			for _, value := range spec.(*ast.ValueSpec).Values {
				lit, ok := value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					return nil, err
				}
				if strings.HasPrefix(v, "sha256:") || !imageRefRe.MatchString(v) || digestRe.MatchString(v) {
					continue
				}
				out = append(out, Finding{file, fset.Position(lit.Pos()).Line, fmt.Sprintf("image %s is not pinned by @sha256: digest", v)})
			}
		}
	}
	return out, nil
}

func checkYAML(file string, lines []string, m mode) []Finding {
	var out []Finding
	block, child := -1, -1
	tagLine, tag, digest := 0, "", ""
	flush := func() {
		if tag == "" || digest != "" || tag == localTag || envsubstRe.MatchString(tag) {
			return
		}
		out = append(out, Finding{file, tagLine, fmt.Sprintf("image.tag %s has no image.digest", tag)})
	}
	for i, line := range lines {
		n := i + 1
		if block >= 0 {
			if c := contentRe.FindStringSubmatch(line); c != nil {
				indent := len(c[1])
				if indent > block {
					if child < 0 {
						child = indent
					}
					if indent == child {
						if t := tagKeyRe.FindStringSubmatch(line); t != nil {
							tagLine, tag = n, scalar(t[1])
						} else if d := digestKeyRe.FindStringSubmatch(line); d != nil {
							digest = scalar(d[1])
						}
					}
					continue
				}
				flush()
				block = -1
			}
		}
		img := imageKeyRe.FindStringSubmatch(line)
		if img == nil {
			continue
		}
		v := scalar(img[3])
		if v == "" {
			if m == values {
				block, child = len(img[1]), -1
				tagLine, tag, digest = 0, "", ""
			}
			continue
		}
		if envsubstRe.MatchString(v) || (m == docs && placeholderDigestRe.MatchString(v)) {
			continue
		}
		if !digestRe.MatchString(v) {
			out = append(out, Finding{file, n, fmt.Sprintf("image: %s is not pinned by @sha256: digest", v)})
		}
	}
	if block >= 0 {
		flush()
	}
	return out
}

func checkOmpVersion(root string) ([]Finding, error) {
	raw, err := os.ReadFile(filepath.Join(root, "extension", "omp-range.json"))
	if err != nil {
		return nil, err
	}
	var ompRange struct {
		Min string `json:"min"`
	}
	if err := json.Unmarshal(raw, &ompRange); err != nil {
		return nil, fmt.Errorf("extension/omp-range.json: %w", err)
	}
	mise, err := os.ReadFile(filepath.Join(root, "mise.toml"))
	if err != nil {
		return nil, err
	}
	for i, line := range lines(mise) {
		m := ompPinRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] == ompRange.Min {
			return nil, nil
		}
		return []Finding{{"mise.toml", i + 1, fmt.Sprintf("oh-my-pi %s differs from extension/omp-range.json min %s", m[1], ompRange.Min)}}, nil
	}
	return nil, errors.New("mise.toml pins no oh-my-pi version")
}
