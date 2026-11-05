package authtest_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/Devcon4/skua/internal/api/auth/authtest"
)

const audience = "skua-omp"

func verifier(t *testing.T, iss *authtest.Issuer) *oidc.IDTokenVerifier {
	t.Helper()
	provider, err := oidc.NewProvider(t.Context(), iss.URL())
	if err != nil {
		t.Fatalf("discover issuer: %v", err)
	}
	return provider.Verifier(&oidc.Config{ClientID: audience})
}

func jwksKeyIDs(t *testing.T, iss *authtest.Issuer) []string {
	t.Helper()
	resp, err := http.Get(iss.URL() + "/keys")
	if err != nil {
		t.Fatalf("get JWKS: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Alg string `json:"alg"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		t.Fatalf("decode JWKS: %v", err)
	}
	var ids []string
	for _, k := range set.Keys {
		if k.Alg != "RS256" {
			t.Errorf("JWKS key %s alg = %q, want RS256", k.Kid, k.Alg)
		}
		ids = append(ids, k.Kid)
	}
	return ids
}

func TestTokenVerifies(t *testing.T) {
	iss := authtest.New(t)
	tok, err := verifier(t, iss).Verify(t.Context(), iss.Token("alice", audience))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if tok.Issuer != iss.URL() || tok.Subject != "alice" || !slices.Equal(tok.Audience, []string{audience}) {
		t.Errorf("token iss=%q sub=%q aud=%v", tok.Issuer, tok.Subject, tok.Audience)
	}
}

func TestBadTokensFail(t *testing.T) {
	iss := authtest.New(t)
	v := verifier(t, iss)
	for name, raw := range map[string]string{
		"expired":      iss.Token("alice", audience, authtest.Expired()),
		"other key":    iss.Token("alice", audience, authtest.OtherKey()),
		"other issuer": iss.Token("alice", audience, authtest.OtherIssuer("https://evil.example")),
		"other aud":    iss.Token("alice", "someone-else"),
	} {
		if _, err := v.Verify(t.Context(), raw); err == nil {
			t.Errorf("%s token verified", name)
		}
	}
}

func tokenKeyID(t *testing.T, raw string) string {
	t.Helper()
	header, _, _ := strings.Cut(raw, ".")
	b, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		t.Fatalf("decode token header: %v", err)
	}
	var h struct {
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(b, &h); err != nil {
		t.Fatalf("parse token header %s: %v", b, err)
	}
	return h.Kid
}

func TestRotateKey(t *testing.T) {
	iss := authtest.New(t)
	v := verifier(t, iss)
	before := jwksKeyIDs(t, iss)
	old := iss.Token("alice", audience)
	if _, err := v.Verify(t.Context(), old); err != nil {
		t.Fatalf("verify before rotation: %v", err)
	}
	iss.RotateKey()
	after := jwksKeyIDs(t, iss)
	if slices.Equal(before, after) || len(after) != len(before)+1 || slices.Contains(before, after[0]) {
		t.Errorf("JWKS kids before %v after %v, want a new first key", before, after)
	}
	rotated := iss.Token("alice", audience)
	if kid := tokenKeyID(t, rotated); kid != after[0] {
		t.Errorf("token after rotation signed with kid %q, want the new key %q", kid, after[0])
	}
	if _, err := v.Verify(t.Context(), rotated); err != nil {
		t.Errorf("verify token with the rotated key: %v", err)
	}
	if _, err := v.Verify(t.Context(), old); err != nil {
		t.Errorf("verify token signed before rotation: %v", err)
	}
}
