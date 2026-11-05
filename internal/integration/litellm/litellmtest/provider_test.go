package litellmtest_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/Devcon4/skua/internal/integration/litellm"
	"github.com/Devcon4/skua/internal/integration/litellm/litellmtest"
)

var start = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func request() litellm.MintRequest {
	return litellm.MintRequest{
		Key:       "sk-skua-0123456789abcdef",
		Alias:     "skua-uid-1",
		Models:    []string{"primary", "helper"},
		MaxBudget: 10,
		Duration:  4*time.Hour + 5*time.Minute,
		Metadata:  map[string]string{"run": "p-1", "profile": "p"},
	}
}

func TestKeysReportMintedState(t *testing.T) {
	clock := clocktesting.NewFakePassiveClock(start)
	f := litellmtest.New(clock)
	if err := f.Mint(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	want := litellmtest.Key{
		Alias:     "skua-uid-1",
		Value:     "sk-skua-0123456789abcdef",
		Models:    []string{"primary", "helper"},
		MaxBudget: 10,
		Duration:  4*time.Hour + 5*time.Minute,
		ExpiresAt: start.Add(4*time.Hour + 5*time.Minute),
		Metadata:  map[string]string{"run": "p-1", "profile": "p"},
	}
	keys := f.Keys()
	if !reflect.DeepEqual(keys, map[string]litellmtest.Key{"skua-uid-1": want}) {
		t.Fatalf("Keys() = %+v", keys)
	}
	keys["skua-uid-1"].Models[0] = "changed"
	if got := f.Keys()["skua-uid-1"].Models[0]; got != "primary" {
		t.Fatalf("Keys() shares state: %q", got)
	}
}

func TestRetryKeepsOneKey(t *testing.T) {
	f := litellmtest.New(clocktesting.NewFakePassiveClock(start))
	for range 3 {
		if err := f.Mint(context.Background(), request()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.Keys()); n != 1 {
		t.Fatalf("%d keys, want 1", n)
	}
}

func TestValueUnderAnotherAliasChangesNothing(t *testing.T) {
	f := litellmtest.New(clocktesting.NewFakePassiveClock(start))
	if err := f.Mint(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	r := request()
	r.Alias = "skua-uid-2"
	if err := f.Mint(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Keys()["skua-uid-2"]; ok || len(f.Keys()) != 1 {
		t.Fatalf("Keys() = %+v", f.Keys())
	}
}

func TestRevokeMarksKeyRevoked(t *testing.T) {
	f := litellmtest.New(clocktesting.NewFakePassiveClock(start))
	if err := f.Mint(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	if err := f.Revoke(context.Background(), "skua-uid-1"); err != nil {
		t.Fatal(err)
	}
	if !f.Keys()["skua-uid-1"].Revoked {
		t.Fatal("key not revoked")
	}
	if err := f.Mint(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	if f.Keys()["skua-uid-1"].Revoked {
		t.Fatal("re-minted key still revoked")
	}
}

func TestExpiryOnFakeClock(t *testing.T) {
	clock := clocktesting.NewFakePassiveClock(start)
	f := litellmtest.New(clock)
	r := request()
	r.Duration = 90*time.Second + time.Millisecond
	if err := f.Mint(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if got := f.Keys()["skua-uid-1"].ExpiresAt; !got.Equal(start.Add(91 * time.Second)) {
		t.Fatalf("ExpiresAt = %v", got)
	}
	clock.SetTime(start.Add(90 * time.Second))
	if f.Keys()["skua-uid-1"].Expired {
		t.Fatal("expired early")
	}
	clock.SetTime(start.Add(91 * time.Second))
	if !f.Keys()["skua-uid-1"].Expired {
		t.Fatal("not expired")
	}
	if err := f.Mint(context.Background(), r); err != nil {
		t.Fatalf("retry on expired key: %v", err)
	}
	if err := f.Revoke(context.Background(), "skua-uid-1"); err != nil {
		t.Fatal(err)
	}
	k := f.Keys()["skua-uid-1"]
	if !k.Revoked || !k.Expired {
		t.Fatalf("key = %+v", k)
	}
}

func TestFailNextQueuesPerOp(t *testing.T) {
	f := litellmtest.New(clocktesting.NewFakePassiveClock(start))
	f.AddModel("primary", true)
	fail := &litellm.APIError{Status: http.StatusInternalServerError}
	f.FailNext(litellmtest.OpMint, fail)
	f.FailNext(litellmtest.OpMint, fail)

	if _, err := f.ModelInfo(context.Background(), "primary"); err != nil {
		t.Fatalf("ModelInfo: %v", err)
	}
	for i := range 2 {
		if err := f.Mint(context.Background(), request()); !errors.Is(err, fail) {
			t.Fatalf("mint %d: err = %v", i, err)
		}
		if len(f.Keys()) != 0 {
			t.Fatalf("mint %d stored a key", i)
		}
	}
	if err := f.Mint(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	if len(f.Keys()) != 1 {
		t.Fatalf("Keys() = %+v", f.Keys())
	}

	want := []litellmtest.Call{
		{Op: litellmtest.OpModelInfo, Target: "primary"},
		{Op: litellmtest.OpMint, Target: "skua-uid-1", Err: fail},
		{Op: litellmtest.OpMint, Target: "skua-uid-1", Err: fail},
		{Op: litellmtest.OpMint, Target: "skua-uid-1"},
	}
	if got := f.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Calls() = %+v", got)
	}
}

func TestUnavailable(t *testing.T) {
	f := litellmtest.New(clocktesting.NewFakePassiveClock(start))
	f.AddModel("primary", true, "openai")
	f.Unavailable(true)
	if _, err := f.ModelInfo(context.Background(), "primary"); !errors.Is(err, litellmtest.ErrUnavailable) {
		t.Fatalf("ModelInfo: %v", err)
	}
	if err := f.Mint(context.Background(), request()); !errors.Is(err, litellmtest.ErrUnavailable) {
		t.Fatalf("Mint: %v", err)
	}
	if err := f.Revoke(context.Background(), "skua-uid-1"); !errors.Is(err, litellmtest.ErrUnavailable) {
		t.Fatalf("Revoke: %v", err)
	}
	if len(f.Keys()) != 0 {
		t.Fatalf("Keys() = %+v", f.Keys())
	}
	f.Unavailable(false)
	info, err := f.ModelInfo(context.Background(), "primary")
	if err != nil || !info.Known || !info.Priced {
		t.Fatalf("ModelInfo = %+v, %v", info, err)
	}
}

func TestDelayHonoursContext(t *testing.T) {
	f := litellmtest.New(clocktesting.NewFakePassiveClock(start))
	f.Delay(litellmtest.OpRevoke, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := f.Revoke(ctx, "skua-uid-1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Revoke: %v", err)
	}
	if err := f.Mint(context.Background(), request()); err != nil {
		t.Fatalf("Mint is delayed: %v", err)
	}
	f.Delay(litellmtest.OpRevoke, 0)
	if err := f.Revoke(context.Background(), "skua-uid-1"); err != nil {
		t.Fatal(err)
	}
}

func TestDelayThenSucceeds(t *testing.T) {
	f := litellmtest.New(clocktesting.NewFakePassiveClock(start))
	f.Delay(litellmtest.OpMint, 10*time.Millisecond)
	began := time.Now()
	if err := f.Mint(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	if time.Since(began) < 10*time.Millisecond {
		t.Fatal("mint was not delayed")
	}
}

func TestModelChanges(t *testing.T) {
	f := litellmtest.New(clocktesting.NewFakePassiveClock(start))
	f.AddModel("primary", false, "openai")
	info, _ := f.ModelInfo(context.Background(), "primary")
	if !info.Known || info.Priced {
		t.Fatalf("ModelInfo = %+v", info)
	}
	f.AddModel("primary", true, "openai")
	info, _ = f.ModelInfo(context.Background(), "primary")
	if !info.Priced {
		t.Fatalf("ModelInfo = %+v", info)
	}
	f.RemoveModel("primary")
	info, _ = f.ModelInfo(context.Background(), "primary")
	if info.Known {
		t.Fatalf("ModelInfo = %+v", info)
	}
}

func TestForeignKeyHiddenFromKeys(t *testing.T) {
	f := litellmtest.New(clocktesting.NewFakePassiveClock(start))
	f.AddForeignKey("skua-uid-1", "sk-other-0123456789")
	if len(f.Keys()) != 0 {
		t.Fatalf("Keys() = %+v", f.Keys())
	}
	if err := f.Revoke(context.Background(), "skua-uid-1"); !errors.Is(err, litellm.ErrNotOwner) {
		t.Fatalf("Revoke: %v", err)
	}
}
