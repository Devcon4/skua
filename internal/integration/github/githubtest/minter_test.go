package githubtest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/Devcon4/skua/internal/integration/github"
	"github.com/Devcon4/skua/internal/integration/github/githubtest"
)

var start = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func TestTokenExpiresAfterTTL(t *testing.T) {
	clk := clocktesting.NewFakeClock(start)
	f := githubtest.New(clk, time.Hour)
	tok, err := f.Mint(context.Background(), "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if !tok.IssuedAt.Equal(start) || !tok.ExpiresAt.Equal(start.Add(time.Hour)) {
		t.Fatalf("token = %+v", tok)
	}
	clk.Step(59 * time.Minute)
	if !f.Valid(tok.Value, "acme/widgets") {
		t.Fatal("invalid before TTL")
	}
	clk.Step(time.Minute)
	if f.Valid(tok.Value, "acme/widgets") {
		t.Fatal("valid after TTL")
	}
}

func TestRevokedTokenIsInvalid(t *testing.T) {
	f := githubtest.New(clocktesting.NewFakeClock(start), time.Hour)
	tok, err := f.Mint(context.Background(), "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Revoke(context.Background(), tok.Value); err != nil {
		t.Fatal(err)
	}
	if f.Valid(tok.Value, "acme/widgets") {
		t.Fatal("valid after revoke")
	}
	if err := f.Revoke(context.Background(), tok.Value); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if err := f.Revoke(context.Background(), "ghs_unknown"); err != nil {
		t.Fatalf("unknown revoke: %v", err)
	}
	tokens := f.Tokens()
	if len(tokens) != 1 || !tokens[0].Revoked || tokens[0].Repo != "acme/widgets" || !tokens[0].IssuedAt.Equal(start) || !tokens[0].ExpiresAt.Equal(start.Add(time.Hour)) {
		t.Fatalf("tokens = %+v", tokens)
	}
}

func TestValidRefusesOtherRepo(t *testing.T) {
	f := githubtest.New(clocktesting.NewFakeClock(start), time.Hour)
	tok, err := f.Mint(context.Background(), "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if f.Valid(tok.Value, "acme/gadgets") {
		t.Fatal("valid for another repo")
	}
	if f.Valid("ghs_unknown", "acme/widgets") {
		t.Fatal("unknown token valid")
	}
}

func TestEachMintIsDistinct(t *testing.T) {
	f := githubtest.New(clocktesting.NewFakeClock(start), time.Hour)
	a, _ := f.Mint(context.Background(), "acme/widgets")
	b, _ := f.Mint(context.Background(), "acme/widgets")
	if a.Value == b.Value {
		t.Fatal("tokens repeat")
	}
	if len(f.Tokens()) != 2 {
		t.Fatalf("tokens = %d", len(f.Tokens()))
	}
}

func TestRateLimitUntilTime(t *testing.T) {
	clk := clocktesting.NewFakeClock(start)
	f := githubtest.New(clk, time.Hour)
	f.RateLimit(start.Add(2 * time.Minute))

	_, err := f.Mint(context.Background(), "acme/widgets")
	var rl *github.RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 2*time.Minute {
		t.Fatalf("err = %v", err)
	}
	clk.Step(90 * time.Second)
	_, err = f.Mint(context.Background(), "acme/widgets")
	if !errors.As(err, &rl) || rl.RetryAfter != 30*time.Second {
		t.Fatalf("err = %v", err)
	}
	clk.Step(30 * time.Second)
	if _, err := f.Mint(context.Background(), "acme/widgets"); err != nil {
		t.Fatalf("mint after window: %v", err)
	}
}

func TestFailNextFailsOnce(t *testing.T) {
	f := githubtest.New(clocktesting.NewFakeClock(start), time.Hour)
	boom := errors.New("boom")
	f.FailNext(boom)
	if _, err := f.Mint(context.Background(), "acme/widgets"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	tok, err := f.Mint(context.Background(), "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	f.FailNext(boom)
	if err := f.Revoke(context.Background(), tok.Value); !errors.Is(err, boom) {
		t.Fatalf("revoke err = %v", err)
	}
	if !f.Valid(tok.Value, "acme/widgets") {
		t.Fatal("failed revoke revoked the token")
	}
	if len(f.Tokens()) != 1 {
		t.Fatalf("tokens = %d", len(f.Tokens()))
	}
}

func TestMintRejectsRepoWithoutOwner(t *testing.T) {
	f := githubtest.New(clocktesting.NewFakeClock(start), time.Hour)
	if _, err := f.Mint(context.Background(), "widgets"); err == nil {
		t.Fatal("mint succeeded")
	}
}
