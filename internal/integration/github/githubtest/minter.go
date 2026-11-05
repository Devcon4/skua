// Package githubtest is an in-memory github.Minter whose tokens expire on a fake clock.
package githubtest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/utils/clock"

	"github.com/Devcon4/skua/internal/integration/github"
)

// TokenInfo is the fake's record of one minted token.
type TokenInfo struct {
	Value     string
	Repo      string
	IssuedAt  time.Time
	ExpiresAt time.Time
	Revoked   bool
}

// Fake implements github.Minter. It is safe for concurrent use.
type Fake struct {
	clock clock.PassiveClock
	ttl   time.Duration

	mu         sync.Mutex
	tokens     []*TokenInfo
	failNext   error
	limitUntil time.Time
}

var _ github.Minter = (*Fake)(nil)

// New returns a Fake whose tokens live for ttl on clock.
func New(clock clock.PassiveClock, ttl time.Duration) *Fake {
	return &Fake{clock: clock, ttl: ttl}
}

// Mint issues a token for repo ("owner/name"). It fails with the FailNext error,
// or with *github.RateLimitError while a RateLimit window is open.
func (f *Fake) Mint(_ context.Context, repo string) (github.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeFailure(); err != nil {
		return github.Token{}, err
	}
	now := f.clock.Now()
	if now.Before(f.limitUntil) {
		return github.Token{}, &github.RateLimitError{RetryAfter: f.limitUntil.Sub(now)}
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return github.Token{}, fmt.Errorf("repository %q is not owner/name", repo)
	}
	info := &TokenInfo{
		Value:     fmt.Sprintf("ghs_fake%032d", len(f.tokens)+1),
		Repo:      repo,
		IssuedAt:  now,
		ExpiresAt: now.Add(f.ttl),
	}
	f.tokens = append(f.tokens, info)
	return github.Token{Value: info.Value, IssuedAt: info.IssuedAt, ExpiresAt: info.ExpiresAt}, nil
}

// Revoke marks token revoked. Unknown, expired and already revoked tokens succeed, as on GitHub.
// Only FailNext makes it fail; the App rate limit does not apply to installation tokens.
func (f *Fake) Revoke(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeFailure(); err != nil {
		return err
	}
	if info := f.find(token); info != nil {
		info.Revoked = true
	}
	return nil
}

// Valid reports whether token is unrevoked, unexpired and was minted for repo.
func (f *Fake) Valid(token, repo string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	info := f.find(token)
	return info != nil && !info.Revoked && info.Repo == repo && f.clock.Now().Before(info.ExpiresAt)
}

// Tokens returns a copy of every token minted so far, in mint order.
func (f *Fake) Tokens() []TokenInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]TokenInfo, len(f.tokens))
	for i, info := range f.tokens {
		out[i] = *info
	}
	return out
}

// FailNext makes the next Mint or Revoke return err.
func (f *Fake) FailNext(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = err
}

// RateLimit makes Mint return *github.RateLimitError until the clock reaches until.
func (f *Fake) RateLimit(until time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limitUntil = until
}

func (f *Fake) takeFailure() error {
	err := f.failNext
	f.failNext = nil
	return err
}

func (f *Fake) find(token string) *TokenInfo {
	for _, info := range f.tokens {
		if info.Value == token {
			return info
		}
	}
	return nil
}
