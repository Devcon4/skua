package sandbox

const (
	DefaultMaxResultBytes = 262144
	DefaultMaxRestarts    = 3

	ServiceAccountName = "skua-runner"
	InitContainerName  = "install-runner"
	AgentContainerName = "agent"

	VolumeBin       = "skua-bin"
	VolumeRun       = "skua-run"
	VolumeWorkspace = "workspace"
	VolumeToken     = "skua-token"

	BinDir        = "/skua/bin"
	RunDir        = "/skua/run"
	WorkDir       = "/work"
	TokenDir      = "/var/run/secrets/skua"
	TokenFile     = "token"
	RunnerLink    = "skua-runner"
	RunnerPath    = BinDir + "/" + RunnerLink
	TokenAudience = "skua-api"

	EnvAPIURL         = "SKUA_API_URL"
	EnvForgeGitURL    = "SKUA_FORGE_GIT_URL"
	EnvForgeAPIURL    = "SKUA_FORGE_API_URL"
	EnvProfile        = "SKUA_PROFILE"
	EnvAdapter        = "SKUA_ADAPTER"
	EnvMaxResultBytes = "SKUA_MAX_RESULT_BYTES"
	EnvMaxRestarts    = "SKUA_MAX_RESTARTS"

	EnvRunnerSock       = "SKUA_RUNNER_SOCK"
	EnvResultSchemaFile = "SKUA_RESULT_SCHEMA_FILE"
	EnvForgeTokenFile   = "SKUA_FORGE_TOKEN_FILE"
	EnvRun              = "SKUA_RUN"
	EnvRepo             = "SKUA_REPO"
	EnvGatewayKey       = "SKUA_LITELLM_KEY"
)
