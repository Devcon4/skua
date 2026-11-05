VERSION ?= 0.1.0-dev
IMG ?= skua:dev

KIND_CLUSTER ?= skua-dev
KIND_KUBECONFIG ?= /tmp/skua-dev.kubeconfig

E2E_KEEP ?=
E2E_ARGS ?=

LDFLAGS = -s -w -X github.com/Devcon4/skua/internal/version.Version=$(VERSION)

E2E_IMAGES = $(IMG) skua-generic:dev skua-e2e-fakeagent:dev skua-e2e-forge:dev skua-e2e-fakellm:dev

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: generate
generate: ## Generate DeepCopy methods for the API types.
	controller-gen object paths="./api/..."

.PHONY: manifests
manifests: ## Generate CRDs into config/crd/bases, the operator RBAC into config/rbac and the skua-api RBAC into config/rbac/api.
	controller-gen rbac:roleName=skua-operator crd output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac paths="./api/..." $(if $(wildcard internal/operator),paths="./internal/operator/...")
	$(if $(wildcard internal/api),controller-gen rbac:roleName=skua-api output:rbac:artifacts:config=config/rbac/api paths="./internal/api/...")

.PHONY: fmt
fmt: ## Fail when gofmt would change a file.
	@files="$$(gofmt -l .)"; if [ -n "$$files" ]; then echo "gofmt needed:"; echo "$$files"; exit 1; fi

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint against code.
	golangci-lint run ./...

RACE = $(if $(filter 1,$(shell go env CGO_ENABLED)),-race,)
TEST_BUDGET ?= 60

.PHONY: test
test: envtest ## Run unit, envtest and in-process integration tests; fails when they take longer than TEST_BUDGET seconds (default 60).
	KUBEBUILDER_ASSETS="$$(go tool setup-envtest use -p path $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN))" GORACE=atexit_sleep_ms=0 go run ./tools/testbudget -budget $(TEST_BUDGET) -- $(RACE) ./...

##@ Build

.PHONY: build
build: ## Build the static skua binary into bin/skua.
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/skua ./cmd/skua

.PHONY: docker-build
docker-build: ## Build the skua image.
	docker build --build-arg VERSION=$(VERSION) -t $(IMG) .

.PHONY: docker-build-images
docker-build-images: docker-build ## Build the skua, generic agent and e2e fixture images.
	docker build -t skua-generic:dev -f images/generic/Dockerfile .
	docker build -t skua-e2e-fakeagent:dev -f test/fakes/omp/Dockerfile .
	docker build -t skua-e2e-forge:dev -f test/fakes/forge/Dockerfile .
	docker build -t skua-e2e-fakellm:dev -f test/fakes/llm/Dockerfile .

##@ Kind / E2E

.PHONY: kind-up
kind-up: ## Create or update the kind test environment.
	go run ./tools/kindenv up -name $(KIND_CLUSTER) -kubeconfig $(KIND_KUBECONFIG)

.PHONY: kind-down
kind-down: ## Delete the kind test environment.
	go run ./tools/kindenv down -name $(KIND_CLUSTER) -kubeconfig $(KIND_KUBECONFIG)

.PHONY: kind-load
kind-load: ## Load the skua, generic agent and e2e fixture images into the kind cluster.
	go run ./tools/kindenv load -name $(KIND_CLUSTER) $(E2E_IMAGES)

.PHONY: deploy
deploy: SKUA_DEPLOY ?= kustomize
deploy: ## Deploy skua into the kind cluster with the SKUA_DEPLOY method (kustomize, the default here, or helm).
	@case "$(SKUA_DEPLOY)" in \
	helm) $(MAKE) deploy-chart ;; \
	kustomize) $(MAKE) deploy-kustomize ;; \
	*) echo "SKUA_DEPLOY=$(SKUA_DEPLOY) is not supported; allowed values: helm, kustomize" >&2; exit 1 ;; \
	esac

.PHONY: deploy-chart
deploy-chart: crds-bundle ## Deploy skua with the Helm chart and the CRD bundle, then the e2e fixtures, into the kind cluster.
	kubectl --kubeconfig $(KIND_KUBECONFIG) apply --server-side -f dist/skua-crds.yaml
	kubectl --kubeconfig $(KIND_KUBECONFIG) wait --for=condition=Established --timeout=60s crd/agentprofiles.skua.dev crd/agentruns.skua.dev
	set -a; . /tmp/$(KIND_CLUSTER).env; set +a; img='$(IMG)'; \
	$(HELM) --kubeconfig $(KIND_KUBECONFIG) upgrade --install skua charts/skua --namespace skua \
		-f test/kindenv/manifests/skua-values.yaml \
		--set-string image.repository="$${img%:*}" \
		--set-string image.tag="$${img##*:}" \
		--set-string "config.network.podCIDRs[0]=$$SKUA_POD_CIDR" \
		--set-string "config.network.serviceCIDRs[0]=$$SKUA_SERVICE_CIDR" \
		--set-string "config.network.nodeCIDRs[0]=$$SKUA_NODE_CIDR" \
		--wait --timeout 5m
	kubectl --kubeconfig $(KIND_KUBECONFIG) -n skua rollout restart deployment/skua-operator deployment/skua-api
	kubectl --kubeconfig $(KIND_KUBECONFIG) -n skua rollout status deployment/skua-operator --timeout=180s
	kubectl --kubeconfig $(KIND_KUBECONFIG) -n skua rollout status deployment/skua-api --timeout=180s
	kubectl --kubeconfig $(KIND_KUBECONFIG) apply -f config/kind/generic-profile.yaml
	kubectl --kubeconfig $(KIND_KUBECONFIG) apply -k test/e2e/fixtures
	kubectl --kubeconfig $(KIND_KUBECONFIG) -n skua-e2e rollout status deployment/skua-e2e-forge --timeout=180s
	kubectl --kubeconfig $(KIND_KUBECONFIG) -n gateway rollout status deployment/fakellm --timeout=180s

.PHONY: deploy-kustomize
deploy-kustomize: ## Deploy skua and the e2e fixtures into the kind cluster with kustomize.
	set -a; . /tmp/$(KIND_CLUSTER).env; set +a; envsubst < config/kind/config.yaml.tmpl > config/kind/config.yaml
	kubectl --kubeconfig $(KIND_KUBECONFIG) apply --server-side -k config/crd
	kubectl --kubeconfig $(KIND_KUBECONFIG) wait --for=condition=Established --timeout=60s crd/agentprofiles.skua.dev crd/agentruns.skua.dev
	kubectl --kubeconfig $(KIND_KUBECONFIG) apply --server-side -k config/kind
	kubectl --kubeconfig $(KIND_KUBECONFIG) apply -k test/e2e/fixtures
	kubectl --kubeconfig $(KIND_KUBECONFIG) -n skua rollout status deployment/skua-operator --timeout=180s
	kubectl --kubeconfig $(KIND_KUBECONFIG) -n skua rollout status deployment/skua-api --timeout=180s
	kubectl --kubeconfig $(KIND_KUBECONFIG) -n skua-e2e rollout status deployment/skua-e2e-forge --timeout=180s
	kubectl --kubeconfig $(KIND_KUBECONFIG) -n gateway rollout status deployment/fakellm --timeout=180s

.PHONY: test-e2e
test-e2e: SKUA_DEPLOY ?= helm
test-e2e: E2E_PARALLEL ?= 4
test-e2e: ## Run the e2e tests in a fresh kind cluster; SKUA_DEPLOY picks helm (default) or kustomize, E2E_PARALLEL caps concurrent tests (default 4), E2E_KEEP=1 leaves it up, E2E_ARGS adds go test flags.
	@if [ "$(E2E_KEEP)" != 1 ]; then trap '$(MAKE) kind-down' EXIT; fi; \
	$(MAKE) kind-up; \
	$(MAKE) docker-build-images; \
	$(MAKE) kind-load; \
	$(MAKE) deploy SKUA_DEPLOY=$(SKUA_DEPLOY); \
	KUBECONFIG=$(KIND_KUBECONFIG) go test -tags e2e -count=1 -timeout 20m -parallel $(E2E_PARALLEL) $(E2E_ARGS) ./test/e2e/...

.PHONY: check-agent-sandbox-upgrade
check-agent-sandbox-upgrade: ## Check the agent-sandbox CRD-first upgrade path in its own kind cluster.
	HELM=$(HELM) go test -tags install ./test/install -run '^TestAgentSandboxUpgrade$$' -count=1 -v -timeout 30m

.PHONY: check-flux-example
check-flux-example: ## Check the Flux worked example in its own kind cluster.
	FLUX=$(FLUX) go test -tags install ./test/install -run '^TestFluxExample$$' -count=1 -v -timeout 30m

.PHONY: check-ingress-stream
check-ingress-stream: ## Check an SSE stream through a TLS ingress in its own kind cluster.
	HELM=$(HELM) go test -tags install ./test/install -run '^TestIngressStream$$' -count=1 -v -timeout 30m

##@ Chart and CI

.PHONY: helm-lint
helm-lint: ## Lint the Helm chart strictly with the CI lint and full values.
	$(HELM) lint charts/skua --strict -f charts/skua/ci/lint-values.yaml
	$(HELM) lint charts/skua --strict -f charts/skua/ci/full-values.yaml

.PHONY: test-chart
test-chart: ## Run the chart render tests.
	HELM=$(HELM) go test ./test/chart/... -count=1

.PHONY: crds-bundle
crds-bundle: ## Write the CRDs as one manifest to dist/skua-crds.yaml.
	mkdir -p dist
	kustomize build config/crd > dist/skua-crds.yaml

.PHONY: lint-ci
lint-ci: ## Audit the workflows and actions with zizmor and actionlint, then check image pins and the omp version.
	zizmor --offline --config .github/zizmor.yml .github
	go tool actionlint -shellcheck=shellcheck
	go test -count=1 ./test/pins

##@ Dependencies

LOCALBIN ?= $(shell pwd)/bin
ENVTEST_K8S_VERSION ?= 1.37.0
HELM ?= helm
FLUX ?= flux

.PHONY: envtest
envtest: ## Download the ENVTEST_K8S_VERSION assets into bin/.
	go tool setup-envtest use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path >/dev/null

-include extension/extension.mk
