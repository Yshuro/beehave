# GO_TEST_P is the number of test packages that run at once. Empty keeps the Go default (one for each CPU).
# A small machine passes GO_TEST_P=2.
GO_TEST_P ?=
# The race detector turns on pointer checks (checkptr) for every package. The SQLite driver is C code that a translator turned into Go,
# and the workflow store runs it on every query. The pointer checks inside that driver cost a quarter to a third of the CPU of the test
# run. Upstream tests the driver with the pointer checks on; the main run skips them for speed. The race detector stays on for all packages.
# GO_TEST_DRIVER_PKGS are the fast packages that use the driver directly. The second run in "test" runs them with the pointer checks
# on for the driver, so a driver upgrade is still checked for pointer misuse on every gate.
GO_TEST_DRIVER_PKGS := ./internal/store/local
# "test" starts one go test run for each flow shard, one for the rest of the flow package and one for the other packages. Each run gets a
# share of the cores (at least one) as GOMAXPROCS, which also sets the -p and -parallel defaults, so one gate uses about the core
# count and does not oversubscribe the host. GO_TEST_PROCS overrides the share.
GO_CORES := $(shell nproc 2>/dev/null || sysctl -n hw.ncpu)
# The run of the other packages and the shard in FLOW_HEAVY_SHARD take about twice the CPU of the others, so each counts as two shares.
FLOW_HEAVY_SHARD := E
GO_TEST_UNITS = $(shell echo $$(( $(words $(FLOW_SHARDS)) + 4 )))
GO_TEST_PROCS ?= $(shell n=$$(( $(GO_CORES) / $(GO_TEST_UNITS) )); [ $$n -ge 1 ] || n=1; echo $$n)
GO_TEST_PROCS_HEAVY = $(shell echo $$(( 2 * $(GO_TEST_PROCS) )))
ifneq ($(shell printf '%s' '$(GO_TEST_PROCS)' | grep -Ec '^[1-9][0-9]*$$'),1)
$(error GO_TEST_PROCS must be a whole number of at least 1, not "$(GO_TEST_PROCS)")
endif
# The CPU work of a run does not change with its share, so a smaller share takes longer. The Go default timeout of 10 minutes is too short for
# the run of the other packages at a share of 2 (a 2-core CI runner). Each run gets GO_TEST_TIMEOUT, which stays under the 30 minute limit of the CI test job.
GO_TEST_TIMEOUT ?= 18m
GO_TEST_FLAGS := -race -gcflags='modernc.org/...=-d=checkptr=0' -count=1 -timeout $(GO_TEST_TIMEOUT) $(if $(GO_TEST_P),-p $(GO_TEST_P))
# The flow package has over 1000 tests, and under the race detector one run of it takes about 300 s on an idle host. A test binary has its own
# 10 minute timeout, so "test" runs the package in shards, one go test run for each shard. A shard is a letter class for the first letter
# after "Test" (a bracket range such as A-D). The last run takes every test that no shard names (a name that does not start with an
# upper case letter, or an Example), through -skip, so no test is left out of every shard. Each shard writes its own coverage profile,
# and "coverage" merges them with the profile of the other packages, so the gate still measures the whole package. "test" starts the
# shards and the run of the other packages together, so the cores are shared (GO_TEST_P applies to each run), writes each output to its
# own test-*.log file, prints the files in order, and fails if any run fails.
comma := ,
space := $() $()
FLOW_PKG := ./internal/flow
FLOW_SHARDS := A-D E F-L M-Q
FLOW_LETTERS := $(subst $(space),,$(FLOW_SHARDS))
FLOW_PROFILES := $(subst $(space),,coverage.out$(foreach s,$(FLOW_SHARDS),$(comma)coverage-flow-$(s).out)$(comma)coverage-flow-rest.out)
GOLANGCI_LINT_VERSION := v2.15.0
GOTESTSUM_VERSION := v1.13.0
GO_TEST_COVERAGE_VERSION := v2.20.1
GOVULNCHECK_VERSION := v1.9.0
# GO_INSTALL_DIR is the folder where go install writes a binary: GOBIN when set, else the bin folder of the first GOPATH entry.
GO_INSTALL_DIR = $(or $(shell go env GOBIN),$(firstword $(subst :, ,$(shell go env GOPATH)))/bin)
# GO_TOOL_AT succeeds when the binary that go install writes for $(1) was built from module $(2) at version $(3). make setup reinstalls a tool
# when it fails. It reads the file in GO_INSTALL_DIR, not the first copy on PATH, so an older copy earlier on PATH does not force a reinstall.
GO_TOOL_AT = go version -m "$(GO_INSTALL_DIR)/$(1)" 2>/dev/null | grep -Eq '^[[:space:]]+mod[[:space:]]+$(subst .,\.,$(2))[[:space:]]+$(subst .,\.,$(3))[[:space:]]'
# Each run of "test" goes through gotestsum, which writes one JUnit file in REPORT_DIR (the glob reports/*.xml matches them all).
REPORT_DIR := reports
GOTESTSUM = gotestsum --format standard-quiet --junitfile $(REPORT_DIR)
AI_RULEZ_VERSION := v5.9.2
# The secret scan of the candidate gate job runs gitleaks. The "git" subcommand exists from 8.19 on.
GITLEAKS_VERSION := v8.30.1
# make mutation and make mutation-pr run gremlins. scripts/mutation-pr.sh reads the JSON report of this version.
GREMLINS_VERSION := v0.6.0
BIN := bin/beehave
REMEDY := bin/remedy

DEADCODE_VERSION := golang.org/x/tools/cmd/deadcode@v0.51.0

.PHONY: setup lint-version ai-rulez-version ai-rulez-install contract-tools-install deps deadcode build release-snapshot format format-check lint vet vuln test coverage smoke smoke-agent smoke-hook smoke-sandbox mutation mutation-pr \
	governance-generate governance-validate governance-verify clean check check-darwin

setup:  ## Install the Go tools and generate the agent files
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(MAKE) ai-rulez-install
	$(call GO_TOOL_AT,go-test-coverage,github.com/vladopajic/go-test-coverage/v2,$(GO_TEST_COVERAGE_VERSION)) || go install github.com/vladopajic/go-test-coverage/v2@$(GO_TEST_COVERAGE_VERSION)
	$(call GO_TOOL_AT,govulncheck,golang.org/x/vuln,$(GOVULNCHECK_VERSION)) || go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(call GO_TOOL_AT,gotestsum,gotest.tools/gotestsum,$(GOTESTSUM_VERSION)) || go install gotest.tools/gotestsum@$(GOTESTSUM_VERSION)
	$(call GO_TOOL_AT,deadcode,golang.org/x/tools,$(word 2,$(subst @, ,$(DEADCODE_VERSION)))) || go install $(DEADCODE_VERSION)
	$(call GO_TOOL_AT,gitleaks,github.com/zricethezav/gitleaks/v8,$(GITLEAKS_VERSION)) || go install github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION)
	go install github.com/go-gremlins/gremlins/cmd/gremlins@$(GREMLINS_VERSION)
	$(MAKE) governance-generate

build:  ## Build bin/beehave and bin/remedy
	go build -trimpath -o $(BIN) ./cmd/beehave
	go build -trimpath -o $(REMEDY) ./examples/remediation/tools/cmd/remedy

release-snapshot:  ## Build release archives without publishing
	goreleaser release --snapshot --clean --skip=publish

lint-version:  ## Fail unless golangci-lint is the version CI runs
	@golangci-lint version | grep -q "version $(patsubst v%,%,$(GOLANGCI_LINT_VERSION)) " || { echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required: run make setup"; exit 1; }

ai-rulez-install:  ## Install ai-rulez $(AI_RULEZ_VERSION) into the Go bin folder
	sh scripts/install-ai-rulez.sh $(AI_RULEZ_VERSION) "$$(go env GOPATH)/bin"

CONTRACT_TOOLS_DIR ?= $(shell go env GOPATH)/bin

contract-tools-install:  ## Install dolt and bd 1.3.2 (linux amd64) into CONTRACT_TOOLS_DIR for the contract tests against a real bd
	sh scripts/install-contract-tools.sh "$(CONTRACT_TOOLS_DIR)"

ai-rulez-version:  ## Fail unless ai-rulez is the version the gate expects
	@ai-rulez --version | grep -qx "ai-rulez version $(patsubst v%,%,$(AI_RULEZ_VERSION))" || { echo "ai-rulez $(AI_RULEZ_VERSION) is required, found: $$(ai-rulez --version). Run make ai-rulez-install. A new version can add rules: change AI_RULEZ_VERSION in its own pull request."; exit 1; }

format: lint-version  ## Format the Go code (gofumpt and goimports)
	golangci-lint fmt ./...

format-check: lint-version  ## Fail if the Go code is not formatted
	golangci-lint fmt --diff ./...

lint: lint-version  ## Lint the Go code
	golangci-lint run ./...

vet:
	go vet ./...

# No package of the module may link DBOS Conductor, the enterprise module, viper or the shell interpreter. net/http and crypto/tls are allowed in dependencies: DBOS links both, and TestRun_OpensNoNetworkSocket proves beehave opens no connection. The depguard rule no-http-client keeps net/http out of beehave's own code.
deps:  ## Fail if the binary links a forbidden dependency
	@if go list -deps ./... | grep -E '^(go\.dbos\.dev|github\.com/spf13/viper|mvdan\.cc/sh/v3/interp)'; then echo "forbidden dependency in the module"; exit 1; fi

# deadcode -test counts the tests as entry points, so it reports a function that nothing calls, not even a test.
deadcode:  ## Fail if a function is unreachable from main and from the tests
	@out="$$(deadcode -test ./...)" || exit 1; if [ -n "$$out" ]; then echo "$$out"; echo "unreachable functions: delete them"; exit 1; fi

vuln:  ## Fail on a known vulnerability the code can reach
	GOTELEMETRY=off govulncheck ./...

test:  ## Run the Go tests with the race detector and write coverage.out, the flow profiles and the JUnit reports in reports/
	@mkdir -p $(REPORT_DIR); rm -f $(REPORT_DIR)/junit-*.xml coverage.out coverage-flow-*.out test-*.log; \
	logs=test-other.log; \
	GOMAXPROCS=$(GO_TEST_PROCS_HEAVY) $(GOTESTSUM)/junit-other.xml -- $(GO_TEST_FLAGS) -covermode=atomic -coverprofile=coverage.out $$(go list ./... | grep -v '/internal/flow$$') > test-other.log 2>&1 & pids=$$!; \
	for s in $(FLOW_SHARDS); do \
		procs=$(GO_TEST_PROCS); [ "$$s" != "$(FLOW_HEAVY_SHARD)" ] || procs=$(GO_TEST_PROCS_HEAVY); \
		GOMAXPROCS=$$procs $(GOTESTSUM)/junit-flow-$$s.xml -- $(GO_TEST_FLAGS) -covermode=atomic -coverprofile=coverage-flow-$$s.out -run "^Test[$$s]" $(FLOW_PKG) > test-flow-$$s.log 2>&1 & pids="$$pids $$!"; \
		logs="$$logs test-flow-$$s.log"; \
	done; \
	GOMAXPROCS=$(GO_TEST_PROCS) $(GOTESTSUM)/junit-flow-rest.xml -- $(GO_TEST_FLAGS) -covermode=atomic -coverprofile=coverage-flow-rest.out -skip "^Test[$(FLOW_LETTERS)]" $(FLOW_PKG) > test-flow-rest.log 2>&1 & pids="$$pids $$!"; \
	logs="$$logs test-flow-rest.log"; \
	rc=0; for p in $$pids; do wait $$p || rc=1; done; \
	for l in $$logs; do echo "== $$l"; cat $$l; done; \
	exit $$rc
	go test -race -count=1 $(GO_TEST_DRIVER_PKGS)

coverage:  ## Enforce the coverage gate on coverage.out and the flow profiles (run make test first)
	go-test-coverage --config=.go-test-coverage.yml --profile=$(FLOW_PROFILES)

smoke: build  ## Run the built binary: --version prints and exits 0
	$(BIN) --version | grep -q '^beehave '
	$(REMEDY) --help | grep -q '^usage: remedy '

smoke-agent: build  ## Run examples/local once with the real claude CLI (costs an agent run; not part of check)
	sh scripts/smoke-agent.sh

smoke-hook: build  ## Run one step with the real claude CLI and check that the command hook blocks git add -A (costs an agent run; not part of check)
	sh scripts/smoke-hook.sh

smoke-sandbox: build  ## Run one step with the real claude CLI and check what the sandbox denies and allows (costs an agent run, at most 0.30 USD; not part of check)
	sh scripts/smoke-sandbox.sh

check-darwin:  ## On a Mac: run the Seatbelt enforcer tests and the contract suite under the real sandbox-exec (not part of check)
	@test "$$(uname -s)" = Darwin || { echo "check-darwin needs a Mac: the Seatbelt code builds on darwin only"; exit 1; }
	go test -count=1 -v -run 'TestEnforcer_PassesTheContractSuite' ./internal/sandbox/seatbelt | grep -v '^=== ' | grep -q -e '--- PASS: TestEnforcer_PassesTheContractSuite' || { echo "the contract suite did not pass under the real enforcer (skipped or failed)"; exit 1; }
	go test -count=1 ./internal/sandbox/...

# Both mutation targets start one gremlins worker for each four cores. MUTATION_WORKERS sets another positive whole number.
# Run on one package: make mutation PKG=./internal/cli
# PKG is a folder, never a "..." pattern: gremlins reads "./internal/..." as a folder name, finds no file and passes. The target refuses such a PKG.
mutation:
	@case "$(PKG)" in *...*) echo 'PKG=$(PKG) holds "...": gremlins reads it as a folder name and mutates nothing. Name one folder, such as PKG=./internal/cli'; exit 2;; esac
	gremlins unleash --workers $$(sh scripts/mutation-pr.sh --print-workers) $(or $(PKG),.)

# Per-PR run: mutate only the Go lines that differ from BASE, the branch the pull request targets. BASE is required: make mutation-pr BASE=origin/integration/r1
# Needs git, go, gremlins, jq and timeout (or gtimeout) on the path; MUTATION_TIMEOUT sets the limit, a positive whole number of seconds (default 1200).
# Fails when no mutant applies or one lives. A diff with no Go source change is not applicable and passes. A diff whose changed lines hold no operator that gremlins mutates is "no candidate" and passes.
mutation-pr:
	sh scripts/mutation-pr.sh "$(BASE)"

governance-generate: ai-rulez-version  ## Generate the agent files from .ai-rulez/
	ai-rulez generate --no-self-mcp

governance-validate: ai-rulez-version
	ai-rulez validate

governance-verify: governance-validate  ## Validate the config and render it without writing files
	ai-rulez generate --dry-run --no-self-mcp --quiet

clean:
	go clean -cache -testcache

check: format-check lint vet deps deadcode vuln test coverage smoke governance-verify  ## The full gate (same as CI)
