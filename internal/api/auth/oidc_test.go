package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	testingclock "k8s.io/utils/clock/testing"

	"github.com/Devcon4/skua/internal/contract/config"
)

func TestOwner(t *testing.T) {
	sum := sha256.Sum256([]byte("https://issuer\nalice"))
	if got, want := Owner("https://issuer", "alice"), hex.EncodeToString(sum[:])[:40]; got != want {
		t.Fatalf("Owner = %q, want %q", got, want)
	}
}

func TestVerifierSharesAndRemembersDiscoveryFailure(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer issuer.Close()
	clk := testingclock.NewFakeClock(time.Now())
	v, err := NewVerifier(config.OIDC{IssuerURL: issuer.URL, Audience: "skua-omp"}, issuer.Client(), clk)
	if err != nil {
		t.Fatal(err)
	}

	const waiters = 4
	errs := make(chan error, waiters)
	for range waiters {
		go func() {
			_, err := v.idTokenVerifier(context.Background())
			errs <- err
		}()
	}
	eventually(t, func() bool { return calls.Load() == 1 })
	close(release)
	for range waiters {
		if err := <-errs; !errors.Is(err, errIssuerUnavailable) {
			t.Fatalf("err = %v, want errIssuerUnavailable", err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("discovery calls = %d, want 1 shared by every waiter", n)
	}

	clk.Step(discoveryRetry - time.Second)
	if _, err := v.idTokenVerifier(context.Background()); !errors.Is(err, errIssuerUnavailable) {
		t.Fatalf("err within the retry delay = %v, want errIssuerUnavailable", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("discovery calls within the retry delay = %d, want 1", n)
	}
	clk.Step(time.Second)
	if _, err := v.idTokenVerifier(context.Background()); !errors.Is(err, errIssuerUnavailable) {
		t.Fatalf("err after the retry delay = %v, want errIssuerUnavailable", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("discovery calls after the retry delay = %d, want 2", n)
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 10s")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
