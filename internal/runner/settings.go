package runner

import (
	"log/slog"
	"strconv"

	"github.com/Devcon4/skua/internal/contract/sandbox"
)

// settings is the agent container environment the runner reads.
type settings struct {
	apiURL, forgeGitURL, forgeAPIURL string
	maxResultBytes, maxRestarts      int
	environ                          []string
}

func settingsFromEnv(getenv func(string) string, environ []string) settings {
	return settings{
		apiURL:         getenv(sandbox.EnvAPIURL),
		forgeGitURL:    getenv(sandbox.EnvForgeGitURL),
		forgeAPIURL:    getenv(sandbox.EnvForgeAPIURL),
		maxResultBytes: intEnv(getenv, sandbox.EnvMaxResultBytes, sandbox.DefaultMaxResultBytes, 1),
		maxRestarts:    intEnv(getenv, sandbox.EnvMaxRestarts, sandbox.DefaultMaxRestarts, 0),
		environ:        environ,
	}
}

func intEnv(getenv func(string) string, name string, def, minimum int) int {
	v := getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < minimum {
		slog.Warn("runner setting invalid, using default", "name", name, "value", v, "default", def)
		return def
	}
	return n
}
