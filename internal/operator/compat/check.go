package compat

import (
	"context"
	"slices"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,resourceNames=sandboxes.agents.x-k8s.io;sandboxtemplates.extensions.agents.x-k8s.io;sandboxwarmpools.extensions.agents.x-k8s.io;sandboxclaims.extensions.agents.x-k8s.io,verbs=get
// +kubebuilder:rbac:groups=apps,namespace=agent-sandbox-system,resources=deployments,verbs=get;list

// Check is a manager runnable that runs CheckInstalled every interval and keeps the latest result.
type Check struct {
	reader    client.Reader
	namespace string
	interval  time.Duration
	events    chan event.TypedGenericEvent[struct{}]

	mu     sync.Mutex
	latest Compat
}

// NewCheck returns a Check that reads the agent-sandbox install through reader, looking for the controller
// Deployment in namespace.
func NewCheck(reader client.Reader, namespace string, interval time.Duration) *Check {
	return &Check{
		reader:    reader,
		namespace: namespace,
		interval:  interval,
		events:    make(chan event.TypedGenericEvent[struct{}], 1),
	}
}

// Start runs the check at once and then every interval until ctx ends.
func (c *Check) Start(ctx context.Context) error {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		c.Observe(ctx, CheckInstalled(ctx, c.reader, c.namespace))
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Observe records got as the latest result. A change of state or versions is logged and signalled on
// Events. Nothing is recorded once ctx has ended.
func (c *Check) Observe(ctx context.Context, got Compat) {
	if ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	prev := c.latest
	c.latest = got
	c.mu.Unlock()
	if prev.State == got.State && slices.Equal(prev.Versions, got.Versions) {
		return
	}
	logger := log.FromContext(ctx).WithValues("state", got.State, "versions", got.Versions, "namespace", c.namespace)
	switch got.State {
	case CompatSupported:
		logger.Info("agent-sandbox version check: " + got.Message)
	case CompatUnsupported:
		logger.Error(nil, "agent-sandbox version check: "+got.Message)
	default:
		logger.Info("agent-sandbox version check: "+got.Message, "severity", "warning")
	}
	// A pending event already makes the reconciler read Unsupported, so a full buffer drops this one.
	select {
	case c.events <- event.TypedGenericEvent[struct{}]{}:
	default:
	}
}

// Unsupported returns the latest result's message and true when the latest result is CompatUnsupported.
func (c *Check) Unsupported() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest.Message, c.latest.State == CompatUnsupported
}

// Events receives one event after each change of the result.
func (c *Check) Events() <-chan event.TypedGenericEvent[struct{}] {
	return c.events
}
