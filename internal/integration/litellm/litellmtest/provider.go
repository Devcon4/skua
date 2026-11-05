package litellmtest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"k8s.io/utils/clock"

	"github.com/Devcon4/skua/internal/integration/litellm"
)

// Op names a Provider method for FailNext, Delay and the call log.
type Op string

const (
	OpMint      Op = "mint"
	OpRevoke    Op = "revoke"
	OpModelInfo Op = "modelInfo"
)

// ErrUnavailable is returned by every call while Unavailable(true) is set.
var ErrUnavailable = errors.New("litellmtest: gateway unavailable")

// Key is a minted key as LiteLLM stores it. Expired is computed from the fake
// clock when Keys is called; Revoked keys stay listed until the alias is minted again.
type Key struct {
	Alias     string
	Value     string
	Models    []string
	MaxBudget float64
	Duration  time.Duration
	ExpiresAt time.Time
	Metadata  map[string]string
	Revoked   bool
	Expired   bool
}

// Call is one Provider call. Target is the alias for mint and revoke and the
// model for modelInfo.
type Call struct {
	Op     Op
	Target string
	Err    error
}

type key struct {
	Key
	foreign bool
}

// Fake is an in-memory litellm.Provider that follows LiteLLM's key alias rules.
type Fake struct {
	clock clock.PassiveClock

	mu          sync.Mutex
	keys        map[string]*key
	models      map[string]litellm.ModelInfo
	failures    map[Op][]error
	delays      map[Op]time.Duration
	unavailable bool
	calls       []Call
}

var _ litellm.Provider = (*Fake)(nil)

func New(clock clock.PassiveClock) *Fake {
	return &Fake{
		clock:    clock,
		keys:     map[string]*key{},
		models:   map[string]litellm.ModelInfo{},
		failures: map[Op][]error{},
		delays:   map[Op]time.Duration{},
	}
}

// AddModel registers or replaces a model group.
func (f *Fake) AddModel(name string, priced bool, providers ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.models[name] = litellm.ModelInfo{Known: true, Priced: priced, Providers: slices.Clone(providers)}
}

func (f *Fake) RemoveModel(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.models, name)
}

// AddForeignKey stores a key owned by another LiteLLM user: revoking it
// returns ErrNotOwner and minting its alias with another value conflicts.
func (f *Fake) AddForeignKey(alias, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[alias] = &key{Key: Key{Alias: alias, Value: value}, foreign: true}
}

// FailNext queues err as the result of the next call of op; queued errors are
// returned in order, one per call.
func (f *Fake) FailNext(op Op, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[op] = append(f.failures[op], err)
}

// Unavailable makes every call return ErrUnavailable until it is cleared.
func (f *Fake) Unavailable(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unavailable = down
}

// Delay makes each call of op wait d of real time, or until its context ends.
// A zero d removes the delay.
func (f *Fake) Delay(op Op, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d <= 0 {
		delete(f.delays, op)
		return
	}
	f.delays[op] = d
}

// Keys returns a copy of every key the fake holds, by alias, foreign keys excluded.
func (f *Fake) Keys() map[string]Key {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.clock.Now()
	out := make(map[string]Key, len(f.keys))
	for alias, k := range f.keys {
		if k.foreign {
			continue
		}
		c := k.Key
		c.Models = slices.Clone(k.Models)
		c.Metadata = maps.Clone(k.Metadata)
		c.Expired = !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt)
		out[alias] = c
	}
	return out
}

// Calls returns the call log in order.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *Fake) Mint(ctx context.Context, r litellm.MintRequest) error {
	return f.call(ctx, OpMint, r.Alias, func() error {
		if err := r.Validate(); err != nil {
			return err
		}
		if existing, ok := f.keys[r.Alias]; ok && !existing.Revoked {
			switch {
			case existing.Value != r.Key:
				return fmt.Errorf("%w: %s", litellm.ErrAliasConflict, r.Alias)
			case existing.foreign:
				return &litellm.APIError{Path: "/key/info", Status: http.StatusForbidden, Type: "auth_error"}
			}
			return nil
		}
		for _, k := range f.keys {
			if k.Value == r.Key && !k.Revoked {
				return nil
			}
		}
		k := &key{Key: Key{
			Alias:     r.Alias,
			Value:     r.Key,
			Models:    slices.Clone(r.Models),
			MaxBudget: r.MaxBudget,
			Duration:  r.Duration,
			Metadata:  maps.Clone(r.Metadata),
		}}
		if r.Duration > 0 {
			k.ExpiresAt = f.clock.Now().Add((r.Duration + time.Second - 1) / time.Second * time.Second)
		}
		f.keys[r.Alias] = k
		return nil
	})
}

func (f *Fake) Revoke(ctx context.Context, alias string) error {
	return f.call(ctx, OpRevoke, alias, func() error {
		if alias == "" {
			return errors.New("litellm: key alias is empty")
		}
		k, ok := f.keys[alias]
		switch {
		case !ok || k.Revoked:
			return nil
		case k.foreign:
			return fmt.Errorf("%w: %s", litellm.ErrNotOwner, alias)
		}
		k.Revoked = true
		return nil
	})
}

func (f *Fake) ModelInfo(ctx context.Context, model string) (litellm.ModelInfo, error) {
	var info litellm.ModelInfo
	err := f.call(ctx, OpModelInfo, model, func() error {
		if m, ok := f.models[model]; ok {
			info = m
			info.Providers = slices.Clone(m.Providers)
		}
		return nil
	})
	if err != nil {
		return litellm.ModelInfo{}, err
	}
	return info, nil
}

func (f *Fake) call(ctx context.Context, op Op, target string, apply func() error) error {
	err := f.wait(ctx, op)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		err = f.result(op, apply)
	}
	f.calls = append(f.calls, Call{Op: op, Target: target, Err: err})
	return err
}

func (f *Fake) wait(ctx context.Context, op Op) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	d := f.delays[op]
	f.mu.Unlock()
	if d == 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *Fake) result(op Op, apply func() error) error {
	if f.unavailable {
		return ErrUnavailable
	}
	if q := f.failures[op]; len(q) > 0 {
		f.failures[op] = q[1:]
		return q[0]
	}
	return apply()
}
