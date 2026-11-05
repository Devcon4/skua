package runner

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/runner/agent"
)

type fakeAdapter struct {
	mu       sync.Mutex
	starts   []agent.StartParams
	prompts  []string
	steers   []string
	aborts   int
	stops    int
	last     string
	exited   chan error
	events   chan wire.Event
	onPrompt func(f *fakeAdapter, ctx context.Context, n int, text string) (agent.TurnResult, error)
}

func (f *fakeAdapter) Start(_ context.Context, p agent.StartParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, p)
	f.exited = make(chan error, 1)
	return nil
}

func (f *fakeAdapter) Prompt(ctx context.Context, text string) (agent.TurnResult, error) {
	f.mu.Lock()
	n := len(f.prompts)
	f.prompts = append(f.prompts, text)
	fn := f.onPrompt
	f.mu.Unlock()
	if fn == nil {
		return agent.TurnResult{Status: "completed"}, nil
	}
	return fn(f, ctx, n, text)
}

func (f *fakeAdapter) crash() (agent.TurnResult, error) {
	f.mu.Lock()
	ch := f.exited
	f.mu.Unlock()
	ch <- errors.New("agent exited with status 1")
	close(ch)
	return agent.TurnResult{}, errors.New("agent exited")
}

func (f *fakeAdapter) setLast(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = s
}

func (f *fakeAdapter) Steer(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steers = append(f.steers, text)
	return nil
}

func (f *fakeAdapter) Abort(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborts++
	return nil
}

func (f *fakeAdapter) LastText(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last, nil
}

func (f *fakeAdapter) Events() <-chan wire.Event { return f.events }

func (f *fakeAdapter) Exited() <-chan error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exited
}

func (f *fakeAdapter) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	return nil
}

func (f *fakeAdapter) snapshot() fakeAdapter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeAdapter{
		starts:  slices.Clone(f.starts),
		prompts: slices.Clone(f.prompts),
		steers:  slices.Clone(f.steers),
		aborts:  f.aborts,
		stops:   f.stops,
	}
}
