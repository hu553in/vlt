.DEFAULT_GOAL := check

BUILD_DIR ?= ./dist

PRETTIER := bunx prettier -u
ACTIONLINT := bunx github-actionlint
TAPLO := bunx @taplo/cli
PREK ?= prek

.PHONY: ensure-build-dir
ensure-build-dir:
	mkdir -p -- "$(BUILD_DIR)"

.PHONY: install-deps
install-deps:
	go mod download

.PHONY: lint
lint:
	$(PRETTIER) -c .
	$(TAPLO) fmt --check
	golangci-lint fmt --diff
	golangci-lint run

.PHONY: lint-fix
lint-fix:
	$(PRETTIER) -w .
	$(TAPLO) fmt
	golangci-lint fmt
	golangci-lint run --fix

.PHONY: check-hooks
check-hooks:
	$(PREK) validate-config prek.toml

.PHONY: build
build: ensure-build-dir install-deps
	CGO_ENABLED=0 GOFLAGS="-buildvcs=false" \
	go build -trimpath -ldflags="-s -w" -o "$(BUILD_DIR)/vlt" ./cmd/vlt

.PHONY: check-deps
check-deps: install-deps
	go mod tidy -diff
	go mod verify

.PHONY: check-vulns
check-vulns: install-deps
	go tool govulncheck ./...

.PHONY: test
test: ensure-build-dir install-deps
	go test \
		-race \
		-coverprofile="$(BUILD_DIR)/coverage.out" \
		-covermode=atomic \
		-coverpkg=./... \
		./...

.PHONY: check-coverage
check-coverage: test
	go tool go-test-coverage --config=./.testcoverage.yml --profile="$(BUILD_DIR)/coverage.out"

.PHONY: check-renovate
check-renovate:
	bunx --package renovate renovate-config-validator --strict --no-global renovate.json

.PHONY: check-workflows
check-workflows:
	$(ACTIONLINT)

.PHONY: check
check: lint check-hooks build check-deps check-vulns check-coverage check-renovate check-workflows

.PHONY: check-fix
check-fix: lint-fix
	$(MAKE) check

.PHONY: test-integration
test-integration:
	@command -v vault >/dev/null
	VLT_INTEGRATION_VAULT_BINARY="$$(command -v vault)" $(MAKE) test

.PHONY: clean
clean:
	rm -rf -- "$(BUILD_DIR)"
