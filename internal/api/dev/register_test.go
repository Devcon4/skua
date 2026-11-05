package dev

import (
	"net/http"
	"testing"
	"time"

	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/config"
)

func TestRegisterRejectsIncompleteDeps(t *testing.T) {
	cfg := &config.Config{OIDC: config.OIDC{IssuerURL: "http://issuer", Audience: "skua-omp"}}
	h := hub.New(testingclock.NewFakeClock(time.Now()))
	var c client.Client = nilClient{}
	for name, deps := range map[string]Deps{
		"no config":   {Client: c, Hub: h, Clock: testingclock.NewFakeClock(time.Now())},
		"no client":   {Config: cfg, Hub: h, Clock: testingclock.NewFakeClock(time.Now())},
		"no hub":      {Config: cfg, Client: c, Clock: testingclock.NewFakeClock(time.Now())},
		"no clock":    {Config: cfg, Client: c, Hub: h},
		"passive":     {Config: cfg, Client: c, Hub: h, Clock: testingclock.NewFakePassiveClock(time.Now())},
		"no audience": {Config: &config.Config{OIDC: config.OIDC{IssuerURL: "http://issuer"}}, Client: c, Hub: h, Clock: testingclock.NewFakeClock(time.Now())},
	} {
		if err := Register(http.NewServeMux(), deps); err == nil {
			t.Errorf("%s: Register succeeded", name)
		}
	}
}

type nilClient struct{ client.Client }
