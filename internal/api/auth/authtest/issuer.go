// Package authtest is an OpenID Connect issuer for tests: discovery and JWKS over httptest and
// RS256 ID tokens with options for the failures a verifier must reject.
package authtest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

const (
	discoveryPath = "/.well-known/openid-configuration"
	jwksPath      = "/keys"
	tokenTTL      = time.Hour
)

// Issuer serves an OIDC discovery document and JWKS and signs ID tokens whose iss is URL().
type Issuer struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	nextKey  atomic.Int64
	signing  jose.JSONWebKey
	verified []jose.JSONWebKey
}

type tokenOptions struct {
	issuer   string
	expired  bool
	otherKey bool
}

// TokenOption changes one property of a token from Token.
type TokenOption func(*tokenOptions)

// Expired makes the token's exp an hour in the past.
func Expired() TokenOption {
	return func(o *tokenOptions) { o.expired = true }
}

// OtherIssuer sets the token's iss to issuer instead of the Issuer's URL.
func OtherIssuer(issuer string) TokenOption {
	return func(o *tokenOptions) { o.issuer = issuer }
}

// OtherKey signs the token with a fresh RS256 key the JWKS never publishes.
func OtherKey() TokenOption {
	return func(o *tokenOptions) { o.otherKey = true }
}

// New starts an Issuer with one RS256 signing key; it stops on t.Cleanup.
func New(t *testing.T) *Issuer {
	t.Helper()
	iss := &Issuer{t: t}
	iss.signing = iss.newKey()
	iss.verified = []jose.JSONWebKey{iss.signing.Public()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+discoveryPath, iss.serveDiscovery)
	mux.HandleFunc("GET "+jwksPath, iss.serveJWKS)
	iss.server = httptest.NewServer(mux)
	t.Cleanup(iss.server.Close)
	return iss
}

// URL is the issuer URL: the discovery base and the iss of every token not using OtherIssuer.
func (i *Issuer) URL() string {
	return i.server.URL
}

// Token returns a signed compact ID token with iss, sub, aud, iat and exp one hour after issue.
func (i *Issuer) Token(sub, aud string, opts ...TokenOption) string {
	i.t.Helper()
	o := tokenOptions{issuer: i.URL()}
	for _, opt := range opts {
		opt(&o)
	}
	now := time.Now()
	if o.expired {
		now = now.Add(-2 * tokenTTL)
	}
	payload, err := json.Marshal(map[string]any{
		"iss": o.issuer,
		"sub": sub,
		"aud": aud,
		"iat": now.Unix(),
		"exp": now.Add(tokenTTL).Unix(),
	})
	if err != nil {
		i.t.Fatalf("authtest: encode claims: %v", err)
	}

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: i.tokenKey(o.otherKey)},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		i.t.Fatalf("authtest: create signer: %v", err)
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		i.t.Fatalf("authtest: sign token: %v", err)
	}
	token, err := jws.CompactSerialize()
	if err != nil {
		i.t.Fatalf("authtest: serialize token: %v", err)
	}
	return token
}

func (i *Issuer) tokenKey(unpublished bool) jose.JSONWebKey {
	if unpublished {
		return i.newKey()
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.signing
}

// RotateKey replaces the signing key with a new one. The JWKS lists the new key first and keeps
// earlier keys, as Dex does, so tokens signed before the rotation still verify.
func (i *Issuer) RotateKey() {
	i.t.Helper()
	key := i.newKey()
	i.mu.Lock()
	defer i.mu.Unlock()
	i.signing = key
	i.verified = append([]jose.JSONWebKey{key.Public()}, i.verified...)
}

func (i *Issuer) newKey() jose.JSONWebKey {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		i.t.Fatalf("authtest: generate RSA key: %v", err)
	}
	return jose.JSONWebKey{Key: priv, KeyID: "key-" + strconv.FormatInt(i.nextKey.Add(1), 10), Algorithm: string(jose.RS256), Use: "sig"}
}

func (i *Issuer) serveDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                i.URL(),
		"jwks_uri":                              i.URL() + jwksPath,
		"authorization_endpoint":                i.URL() + "/auth",
		"token_endpoint":                        i.URL() + "/token",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{string(jose.RS256)},
	})
}

func (i *Issuer) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	i.mu.Lock()
	set := jose.JSONWebKeySet{Keys: append([]jose.JSONWebKey(nil), i.verified...)}
	i.mu.Unlock()
	writeJSON(w, set)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
