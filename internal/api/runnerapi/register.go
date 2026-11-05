// Package runnerapi serves skua-api's runner-facing routes: the command stream, events and the result.
package runnerapi

import (
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/Masterminds/semver/v3"
	authenticationv1client "k8s.io/client-go/kubernetes/typed/authentication/v1"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/wire"
)

var logger = ctrllog.Log.WithName("api").WithName("runner")

// Mux is where Register adds the routes.
type Mux interface {
	Handle(pattern string, h http.Handler)
}

// Deps are the dependencies of the runner-facing routes. Client is cached (AgentRuns and labelled per-run
// Secrets in runNamespace); APIReader reads pods and claims in sandboxNamespace uncached.
type Deps struct {
	Config       *config.Config
	Client       client.Client
	APIReader    client.Reader
	TokenReviews authenticationv1client.TokenReviewInterface
	Hub          *hub.Hub
	Clock        clock.PassiveClock
}

// Routes serve the runner-facing routes and push assignments and forge tokens to attached runners.
type Routes struct {
	deps     Deps
	versions *semver.Constraints

	mu   sync.Mutex
	pods map[string]*podState
}

type podState struct {
	uid      string
	conn     *hub.RunnerConn
	assigned *hub.RunnerConn
	run      string
	token    string
	done     bool
}

// Register registers the runner-facing routes on m and returns them, so AgentRun and Secret changes can be fed
// to Assign, PushToken and ForgetRun.
func Register(m Mux, deps Deps) (*Routes, error) {
	switch {
	case m == nil:
		return nil, errors.New("api: runner routes: server is nil")
	case deps.Config == nil, deps.Client == nil, deps.APIReader == nil, deps.TokenReviews == nil, deps.Hub == nil, deps.Clock == nil:
		return nil, errors.New("api: runner routes: Config, Client, APIReader, TokenReviews, Hub and Clock are required")
	}
	versions, err := semver.NewConstraint(deps.Config.Runner.SupportedVersions)
	if err != nil {
		return nil, fmt.Errorf("api: runner routes: runner.supportedVersions: %w", err)
	}
	rr := &Routes{deps: deps, versions: versions, pods: map[string]*podState{}}
	m.Handle("GET "+wire.RunnerStreamPath, http.HandlerFunc(rr.stream))
	m.Handle("POST "+wire.RunnerEventsPath, http.HandlerFunc(rr.events))
	m.Handle("POST "+wire.RunnerResultPath, http.HandlerFunc(rr.result))
	return rr, nil
}
