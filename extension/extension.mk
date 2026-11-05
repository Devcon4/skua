OMP_BIN ?= omp
OMP_VERSION := $(shell node -p "require('./extension/omp-range.json').min")
KUBECONFIG ?= /tmp/skua-dev.kubeconfig
OMP_TEST = OMP_BIN=$(OMP_BIN) BUN_BE_BUN=1 $(OMP_BIN) test --no-install
OMP_TEST_FAST = $(OMP_TEST) --parallel --concurrent
EXTENSION_DEPS_STAMP := extension/dev/node_modules/.package-lock.json

.PHONY: print-omp-version extension-deps extension-omp extension-typecheck extension-check test-extension test-extension-kind

print-omp-version:
	@echo $(OMP_VERSION)

$(EXTENSION_DEPS_STAMP): extension/dev/package.json extension/dev/package-lock.json
	npm --prefix extension/dev ci
	touch $@

extension-deps: $(EXTENSION_DEPS_STAMP)
	ln -sfn dev/node_modules extension/node_modules

extension-omp:
	@command -v $(OMP_BIN) >/dev/null 2>&1 || { echo "extension tests run under omp's bundled Bun, but OMP_BIN=$(OMP_BIN) is not an executable. Install omp $(OMP_VERSION) with 'mise install' from the repo root or pass OMP_BIN=<path to omp>." >&2; exit 1; }

extension-typecheck: extension-deps
	npx --prefix extension/dev tsgo --noEmit -p extension/tsconfig.json

extension-check: extension-typecheck extension-omp
	$(OMP_TEST_FAST) ./extension/test/unit/

test-extension: extension-typecheck extension-omp
	$(OMP_TEST_FAST) ./extension/test/unit/ ./extension/test/rpc/

test-extension-kind: extension-deps extension-omp
	KUBECONFIG=$(KUBECONFIG) $(OMP_TEST) ./extension/test/kind/
