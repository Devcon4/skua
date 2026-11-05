// Package dev serves skua-api's dev-facing /v1alpha1 routes.
package dev

import (
	"errors"
	"fmt"
	"net/http"

	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/internal/api/auth"
	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/config"
)

// +kubebuilder:rbac:groups=skua.dev,namespace=skua,resources=agentruns,verbs=get;list;watch;create;patch

// Mux is where Register adds the routes.
type Mux interface {
	Handle(pattern string, h http.Handler)
}

// Deps are the dependencies of the dev-facing routes. Register builds the OIDC verifier from Config.OIDC with
// HTTPClient.
type Deps struct {
	Config     *config.Config
	Client     client.Client
	Hub        *hub.Hub
	Clock      clock.PassiveClock
	HTTPClient *http.Client
}

type routes struct {
	cfg    *config.Config
	client client.Client
	hub    *hub.Hub
	clock  clock.WithTicker
}

// Register registers the dev-facing /v1alpha1 routes on m. Every route needs an OIDC ID token from
// Config.OIDC's issuer whose aud contains Config.OIDC.Audience. d.Clock must implement clock.WithTicker for
// event-stream heartbeats and the abort grace timer; a nil HTTPClient uses http.DefaultClient for OIDC discovery.
func Register(m Mux, d Deps) error {
	switch {
	case m == nil:
		return errors.New("api: dev routes: server is nil")
	case d.Config == nil:
		return errors.New("api: dev routes: Config is nil")
	case d.Client == nil:
		return errors.New("api: dev routes: Client is nil")
	case d.Hub == nil:
		return errors.New("api: dev routes: Hub is nil")
	}
	clk, ok := d.Clock.(clock.WithTicker)
	if !ok {
		return fmt.Errorf("api: dev routes: Clock %T does not implement clock.WithTicker", d.Clock)
	}
	verifier, err := auth.NewVerifier(d.Config.OIDC, d.HTTPClient, clk)
	if err != nil {
		return err
	}
	rt := &routes{cfg: d.Config, client: d.Client, hub: d.Hub, clock: clk}
	handlers := map[string]http.HandlerFunc{
		"GET /v1alpha1/profiles":           rt.listProfiles,
		"POST /v1alpha1/runs":              rt.createRun,
		"GET /v1alpha1/runs":               rt.listRuns,
		"GET /v1alpha1/runs/{name}":        rt.getRun,
		"GET /v1alpha1/runs/{name}/events": rt.events,
		"POST /v1alpha1/runs/{name}/steer": rt.steer,
		"POST /v1alpha1/runs/{name}/abort": rt.abort,
	}
	for pattern, h := range handlers {
		m.Handle(pattern, verifier.Authenticate(h))
	}
	return nil
}
