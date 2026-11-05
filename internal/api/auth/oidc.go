package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"k8s.io/utils/clock"

	"github.com/Devcon4/skua/internal/contract/config"
)

var errIssuerUnavailable = errors.New("api: OIDC issuer unavailable")

const discoveryRetry = 5 * time.Second

type ownerKey struct{}

// Verifier discovers the issuer on first use and retries per request, so skua-api starts while the issuer is down.
type Verifier struct {
	issuerURL  string
	audience   string
	httpClient *http.Client
	clock      clock.PassiveClock

	mu        sync.Mutex
	verifier  *oidc.IDTokenVerifier
	failure   error
	failedAt  time.Time
	discovery chan struct{}
}

// NewVerifier returns a Verifier for cfg's issuer and audience; a nil httpClient uses http.DefaultClient for
// discovery. It fails when the issuer URL or the audience is empty.
func NewVerifier(cfg config.OIDC, httpClient *http.Client, clk clock.PassiveClock) (*Verifier, error) {
	if cfg.IssuerURL == "" {
		return nil, errors.New("api: oidc.issuerURL is empty")
	}
	if cfg.Audience == "" {
		return nil, errors.New("api: oidc.audience is empty")
	}
	return &Verifier{issuerURL: cfg.IssuerURL, audience: cfg.Audience, httpClient: httpClient, clock: clk}, nil
}

func (v *Verifier) idTokenVerifier(ctx context.Context) (*oidc.IDTokenVerifier, error) {
	for {
		v.mu.Lock()
		verifier, failure, failedAt, inflight := v.verifier, v.failure, v.failedAt, v.discovery
		start := verifier == nil && inflight == nil && (failure == nil || v.clock.Since(failedAt) >= discoveryRetry)
		if start {
			v.discovery = make(chan struct{})
			inflight = v.discovery
		}
		v.mu.Unlock()
		switch {
		case verifier != nil:
			return verifier, nil
		case start:
			v.discover(ctx, inflight)
		case inflight == nil:
			return nil, failure
		default:
			select {
			case <-inflight:
			case <-ctx.Done():
				return nil, fmt.Errorf("%w: %w", errIssuerUnavailable, ctx.Err())
			}
		}
	}
}

func (v *Verifier) discover(ctx context.Context, done chan struct{}) {
	ctx = context.WithoutCancel(ctx)
	if v.httpClient != nil {
		ctx = oidc.ClientContext(ctx, v.httpClient)
	}
	provider, err := oidc.NewProvider(ctx, v.issuerURL)
	v.mu.Lock()
	defer v.mu.Unlock()
	if err != nil {
		v.failure, v.failedAt = fmt.Errorf("%w: %w", errIssuerUnavailable, err), v.clock.Now()
	} else {
		v.verifier = provider.Verifier(&oidc.Config{ClientID: v.audience, Now: v.clock.Now})
	}
	v.discovery = nil
	close(done)
}

func (v *Verifier) owner(ctx context.Context, raw string) (string, error) {
	verifier, err := v.idTokenVerifier(ctx)
	if err != nil {
		return "", err
	}
	tok, err := verifier.Verify(ctx, raw)
	if err != nil {
		return "", err
	}
	if tok.Subject == "" {
		return "", errors.New("api: token has no sub")
	}
	return Owner(tok.Issuer, tok.Subject), nil
}

// Authenticate serves next with the caller's owner in the request context, read back by OwnerFrom. A missing or
// invalid bearer token gets 401, and an unreachable issuer 503.
func (v *Verifier) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := BearerToken(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="skua-api"`)
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		owner, err := v.owner(r.Context(), raw)
		if errors.Is(err, errIssuerUnavailable) {
			http.Error(w, "OIDC issuer unavailable", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="skua-api", error="invalid_token"`)
			http.Error(w, "invalid bearer token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ownerKey{}, owner)))
	})
}

// OwnerFrom returns the owner Authenticate put in ctx, or "" when there is none.
func OwnerFrom(ctx context.Context) string {
	owner, _ := ctx.Value(ownerKey{}).(string)
	return owner
}

// Owner is the owner label value of the dev with OIDC subject sub at issuer.
func Owner(issuer, sub string) string {
	sum := sha256.Sum256([]byte(issuer + "\n" + sub))
	return hex.EncodeToString(sum[:])[:40]
}
