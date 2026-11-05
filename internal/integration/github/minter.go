package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	gogithub "github.com/google/go-github/v88/github"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/internal/contract/config"
)

// Minter issues and revokes GitHub App installation tokens scoped to one repository.
type Minter interface {
	// Mint returns a token for repo ("owner/name") with contents and pull_requests write.
	Mint(ctx context.Context, repo string) (Token, error)
	// Revoke invalidates token; a token that is already expired or revoked succeeds.
	Revoke(ctx context.Context, token string) error
}

// Token is an installation access token and its lifetime.
type Token struct {
	Value     string
	ExpiresAt time.Time
	IssuedAt  time.Time
}

// RateLimitError reports that GitHub refused the call until RetryAfter has passed. RetryAfter is at least
// one second.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("github rate limited, retry after %s", e.RetryAfter)
}

// RemintDue reports whether half of t's lifetime has passed at now.
func RemintDue(t Token, now time.Time) bool {
	half := t.ExpiresAt.Sub(t.IssuedAt) / 2
	return !now.Before(t.IssuedAt.Add(half))
}

const (
	minRateLimitWait = time.Second
	// GitHub's documented wait for a secondary rate limit that names no reset time.
	defaultRateLimitWait = time.Minute
)

type minter struct {
	reader client.Reader
	cfg    config.GitHub
	hc     *http.Client
	clock  clock.PassiveClock
}

// NewMinter returns a Minter that reads the App Secret through reader on every Mint.
// A nil hc uses http.DefaultClient.
func NewMinter(reader client.Reader, cfg config.GitHub, hc *http.Client, clock clock.PassiveClock) Minter {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &minter{reader: reader, cfg: cfg, hc: hc, clock: clock}
}

type appCredentials struct {
	appID          int64
	installationID int64
	keyPEM         []byte
}

func (m *minter) Mint(ctx context.Context, repo string) (Token, error) {
	name, err := repoName(repo)
	if err != nil {
		return Token{}, err
	}
	creds, err := m.readCredentials(ctx)
	if err != nil {
		return Token{}, err
	}
	base := m.hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	apps, err := ghinstallation.NewAppsTransport(base, creds.appID, creds.keyPEM)
	if err != nil {
		ref := m.cfg.AppSecret
		return Token{}, fmt.Errorf("github app secret %s/%s key %q is not a PEM-encoded PKCS#1 or PKCS#8 RSA private key", ref.Namespace, ref.Name, ref.PrivateKeyKey)
	}
	apps.BaseURL = m.apiURL()
	installation := ghinstallation.NewFromAppsTransport(apps, creds.installationID)
	installation.InstallationTokenOptions = &gogithub.InstallationTokenOptions{
		Repositories: []string{name},
		Permissions: &gogithub.InstallationPermissions{
			Contents:     gogithub.Ptr("write"),
			PullRequests: gogithub.Ptr("write"),
		},
	}

	if m.hc.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.hc.Timeout)
		defer cancel()
	}
	now := m.clock.Now()
	value, err := installation.Token(ctx)
	if err != nil {
		return Token{}, m.mintError(err, repo)
	}
	expiresAt, _, err := installation.Expiry()
	if err != nil || value == "" || expiresAt.IsZero() {
		return Token{}, fmt.Errorf("mint github token for %s: response lacks token or expires_at", repo)
	}
	return Token{Value: value, ExpiresAt: expiresAt, IssuedAt: now}, nil
}

func (m *minter) mintError(err error, repo string) error {
	op := "mint github token for " + repo
	var httpErr *ghinstallation.HTTPError
	if errors.As(err, &httpErr) && httpErr.Response != nil && httpErr.Response.StatusCode/100 != 2 {
		body := httpErr.Response.Body
		defer func() { _ = body.Close() }()
		return m.responseError(gogithub.CheckResponse(httpErr.Response), op)
	}
	return fmt.Errorf("%s: %w", op, err)
}

func (m *minter) Revoke(ctx context.Context, token string) error {
	const op = "revoke github token"
	base := m.apiURL()
	gh, err := gogithub.NewClient(gogithub.WithHTTPClient(m.hc), gogithub.WithAuthToken(token), gogithub.WithURLs(&base, nil))
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	_, err = gh.Apps.RevokeInstallationToken(ctx)
	var resp *gogithub.ErrorResponse
	if err == nil || errors.As(err, &resp) && resp.Response != nil && resp.Response.StatusCode == http.StatusUnauthorized {
		return nil
	}
	return m.responseError(err, op)
}

func (m *minter) readCredentials(ctx context.Context) (appCredentials, error) {
	ref := m.cfg.AppSecret
	var secret corev1.Secret
	if err := m.reader.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, &secret); err != nil {
		return appCredentials{}, fmt.Errorf("read github app secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	value := func(key string) (string, error) {
		v := strings.TrimSpace(string(secret.Data[key]))
		if v == "" {
			return "", fmt.Errorf("github app secret %s/%s has no key %q", ref.Namespace, ref.Name, key)
		}
		return v, nil
	}
	appID, err := value(ref.AppIDKey)
	if err != nil {
		return appCredentials{}, err
	}
	id := func(key, v string) (int64, error) {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("github app secret %s/%s key %q is not a numeric id", ref.Namespace, ref.Name, key)
		}
		return n, nil
	}
	appNum, err := id(ref.AppIDKey, appID)
	if err != nil {
		return appCredentials{}, err
	}
	installationID, err := value(ref.InstallationIDKey)
	if err != nil {
		return appCredentials{}, err
	}
	installationNum, err := id(ref.InstallationIDKey, installationID)
	if err != nil {
		return appCredentials{}, err
	}
	keyPEM, err := value(ref.PrivateKeyKey)
	if err != nil {
		return appCredentials{}, err
	}
	return appCredentials{appID: appNum, installationID: installationNum, keyPEM: []byte(keyPEM)}, nil
}

func (m *minter) apiURL() string {
	return strings.TrimSuffix(m.cfg.APIURL, "/")
}

func (m *minter) responseError(err error, op string) error {
	var primary *gogithub.RateLimitError
	if errors.As(err, &primary) {
		wait := defaultRateLimitWait
		if !primary.Rate.Reset.IsZero() {
			wait = primary.Rate.Reset.Sub(m.clock.Now())
		}
		return &RateLimitError{RetryAfter: max(wait, minRateLimitWait)}
	}
	var secondary *gogithub.AbuseRateLimitError
	if errors.As(err, &secondary) {
		wait := defaultRateLimitWait
		if secondary.RetryAfter != nil {
			wait = *secondary.RetryAfter
		}
		return &RateLimitError{RetryAfter: max(wait, minRateLimitWait)}
	}
	var resp *gogithub.ErrorResponse
	if errors.As(err, &resp) && resp.Response != nil {
		if resp.Message != "" {
			return fmt.Errorf("%s: status %d: %s", op, resp.Response.StatusCode, resp.Message)
		}
		return fmt.Errorf("%s: status %d", op, resp.Response.StatusCode)
	}
	return fmt.Errorf("%s: %w", op, err)
}

func repoName(repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("repository %q is not owner/name", repo)
	}
	return name, nil
}
