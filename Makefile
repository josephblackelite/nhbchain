.PHONY: proto sdk tidy up down audit\:static audit\:tests audit\:determinism audit\:e2e audit\:chaos audit\:perf audit\:netsec audit\:ledger audit\:supply audit\:config audit\:docs audit\:endpoints audit\:english
.PHONY: bugcheck bugcheck-tools bugcheck-static bugcheck-race bugcheck-fuzz bugcheck-determinism bugcheck-chaos bugcheck-gateway bugcheck-network bugcheck-perf bugcheck-proto bugcheck-docs

bugcheck:
	@bash scripts/bugcheck.sh

bugcheck-tools:
	@bash -o errexit -o nounset -o pipefail -c ' \
command -v golangci-lint >/dev/null 2>&1 || GO111MODULE=on go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; \
command -v staticcheck >/dev/null 2>&1 || GO111MODULE=on go install honnef.co/go/tools/cmd/staticcheck@latest; \
command -v gosec >/dev/null 2>&1 || GO111MODULE=on go install github.com/securego/gosec/v2/cmd/gosec@latest; \
command -v govulncheck >/dev/null 2>&1 || GO111MODULE=on go install golang.org/x/vuln/cmd/govulncheck@latest; \
command -v buf >/dev/null 2>&1 || GO111MODULE=on go install github.com/bufbuild/buf/cmd/buf@latest'

bugcheck-static:
	@bash -o errexit -o nounset -o pipefail -c ' \
golangci-lint run ./... && \
go vet ./... && \
staticcheck ./... && \
gosec ./... && \
govulncheck ./...'

bugcheck-race:
	@go test -race ./...

bugcheck-fuzz:
	@go test -run ^$$ -fuzz=Fuzz -fuzztime=60s ./tests/... ./p2p

bugcheck-determinism:
	@$(MAKE) audit\:determinism

bugcheck-chaos:
	@$(MAKE) audit\:chaos

bugcheck-gateway:
	@go test -run TestEndToEndFinancialFlows ./tests/e2e

bugcheck-network:
	@$(MAKE) audit\:netsec

bugcheck-perf:
	@$(MAKE) audit\:perf

bugcheck-proto:
	@bash -o errexit -o nounset -o pipefail -c 'buf lint && buf breaking --against ".git#branch=main"'

bugcheck-docs:
	@$(MAKE) audit\:docs

STABLE_BASE ?= http://localhost:7074

proto:
	go run ./tools/proto/gen.go

sdk: proto
	cd sdk && go test ./...

# Convenience target to tidy both modules.
tidy:
	go mod tidy
	cd sdk && go mod tidy

up:
	docker compose -f deploy/compose/docker-compose.yml up -d --build

down:
	docker compose -f deploy/compose/docker-compose.yml down -v

.PHONY: docs
docs\:%:
	@if [ "$*" = "verify" ]; then \
	go run ./tools/docs/verify.go; \
	else \
	echo "unknown docs task $*"; exit 1; \
	fi

.PHONY: audit\:static
audit\:static:
	@mkdir -p logs
	@bash -o errexit -o nounset -o pipefail -c ' \
	go mod tidy 2>&1 | tee logs/go-mod-tidy.log && \
	golangci-lint run ./... 2>&1 | tee logs/golangci-lint.log && \
	govulncheck ./... 2>&1 | tee logs/govulncheck.log && \
	staticcheck ./... 2>&1 | tee logs/staticcheck.log && \
	buf lint 2>&1 | tee logs/buf-lint.log && \
	buf breaking --against ".git#branch=main" 2>&1 | tee logs/buf-breaking.log'

.PHONY: audit\:english
audit\:english:
	bash scripts/run_english_audit.sh

.PHONY: audit\:tests
audit\:tests:
	@mkdir -p logs
	@bash -o errexit -o nounset -o pipefail -c ' \
		go test ./... 2>&1 | tee logs/go-tests.log && \
		(cd sdk && go test ./... 2>&1 | tee ../logs/sdk-go-tests.log)'

.PHONY: audit\:determinism
audit\:determinism:
	@mkdir -p logs artifacts/determinism
	@bash -o errexit -o nounset -o pipefail -c ' \
		go test -json ./tests/determinism/... 2>&1 | tee artifacts/determinism/determinism-tests.json > /dev/null && \
		cp artifacts/determinism/determinism-tests.json logs/determinism-tests.json && \
		go test -json ./tests/consensus/... 2>&1 | tee artifacts/determinism/consensus-tests.json > /dev/null && \
		cp artifacts/determinism/consensus-tests.json logs/determinism-consensus-tests.json && \
		python3 scripts/audit/summarize_tests.py --phase determinism \
		--suite engagement=artifacts/determinism/determinism-tests.json \
		--suite consensus=artifacts/determinism/consensus-tests.json \
		--out artifacts/determinism/summary.json \
		--markdown artifacts/determinism/summary.md'
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh determinism ops/audit/determinism.yaml artifacts/determinism 2>&1 | tee logs/audit-determinism.log'

.PHONY: audit\:e2e
audit\:e2e:
	@mkdir -p logs artifacts/e2e
	@bash -o errexit -o nounset -o pipefail -c ' \
		go test -json -run TestAuditSmokePlan ./tests/e2e/... 2>&1 | tee artifacts/e2e/smoke.json > /dev/null && \
		cp artifacts/e2e/smoke.json logs/e2e-smoke.json && \
		python3 scripts/audit/summarize_tests.py --phase e2e \
		--suite smoke=artifacts/e2e/smoke.json \
		--out artifacts/e2e/summary.json \
		--markdown artifacts/e2e/summary.md'
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh e2e ops/audit/e2e.yaml artifacts/e2e --compose deploy/compose/docker-compose.audit-e2e.yaml --hash deploy/compose/docker-compose.audit-e2e.yaml 2>&1 | tee logs/audit-e2e.log'

.PHONY: audit\:chaos
audit\:chaos:
	@mkdir -p logs artifacts/chaos
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh chaos ops/audit/chaos.yaml artifacts/chaos 2>&1 | tee logs/audit-chaos.log'

.PHONY: audit\:perf
audit\:perf:
	@mkdir -p logs artifacts/perf
	@bash -o errexit -o nounset -o pipefail -c ' \
		go test -run ^$$ -bench=. -benchtime=100x -benchmem ./tests/perf/... 2>&1 | tee artifacts/perf/bench.txt && \
		cp artifacts/perf/bench.txt logs/perf-bench.txt && \
		mv tests/perf/consensus_latency_report.json artifacts/perf/consensus_latency_report.json && \
		mv tests/perf/consensus_latency_report.txt artifacts/perf/consensus_latency_report.txt'
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh perf ops/audit/perf.yaml artifacts/perf --hash artifacts/perf/bench.txt --hash artifacts/perf/consensus_latency_report.json 2>&1 | tee logs/audit-perf.log'

.PHONY: audit\:netsec
audit\:netsec:
	@mkdir -p logs artifacts/netsec
	@bash -o errexit -o nounset -o pipefail -c ' \
		go test -json ./tests/netsec/... 2>&1 | tee artifacts/netsec/tests.json > /dev/null && \
		cp artifacts/netsec/tests.json logs/netsec-tests.json && \
		python3 scripts/audit/summarize_tests.py --phase netsec \
		--suite netsec=artifacts/netsec/tests.json \
		--out artifacts/netsec/summary.json \
		--markdown artifacts/netsec/summary.md'
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh netsec ops/audit/netsec.yaml artifacts/netsec 2>&1 | tee logs/audit-netsec.log'

.PHONY: audit\:ledger
audit\:ledger:
	@mkdir -p logs artifacts/ledger
	@bash -o errexit -o nounset -o pipefail -c ' \
		go test -json ./tests/ledger/... 2>&1 | tee artifacts/ledger/tests.json > /dev/null && \
		cp artifacts/ledger/tests.json logs/ledger-tests.json && \
		python3 scripts/audit/summarize_tests.py --phase ledger \
		--suite ledger=artifacts/ledger/tests.json \
		--out artifacts/ledger/summary.json \
		--markdown artifacts/ledger/summary.md'
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh ledger ops/audit/ledger.yaml artifacts/ledger --hash ops/audit/ledger.yaml 2>&1 | tee logs/audit-ledger.log'

.PHONY: audit\:supply
audit\:supply:
	@mkdir -p logs artifacts/supply
	@bash -o errexit -o nounset -o pipefail -c ' \
		go test -json -run TestSupplyFixtureBalances ./tests/ledger/... 2>&1 | tee artifacts/supply/tests.json > /dev/null && \
		cp artifacts/supply/tests.json logs/supply-tests.json && \
		python3 scripts/audit/summarize_tests.py --phase supply \
		--suite supply=artifacts/supply/tests.json \
		--out artifacts/supply/summary.json \
		--markdown artifacts/supply/summary.md'
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh supply ops/audit/supply.yaml artifacts/supply --hash ops/audit/supply.yaml 2>&1 | tee logs/audit-supply.log'

.PHONY: audit\:config
audit\:config:
	@mkdir -p logs artifacts/config
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh config ops/audit/config.yaml artifacts/config --hash config/config.toml --hash config-peer.toml --hash config-local.toml 2>&1 | tee logs/audit-config.log'

.PHONY: audit\:docs
audit\:docs:
	@mkdir -p logs artifacts/docs
	@bash -o errexit -o nounset -o pipefail -c 'go run ./tools/docs/verify.go 2>&1 | tee logs/docs-verify.log'
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh docs ops/audit/docs.yaml artifacts/docs --hash docs/security/audit-readiness.md --hash ops/audit-pack/BUILD_STEPS.md 2>&1 | tee logs/audit-docs.log'

.PHONY: audit\:endpoints
audit\:endpoints:
	@mkdir -p logs artifacts/endpoints
	@bash -o errexit -o nounset -o pipefail -c ' \
		npx --yes newman run examples/postman/Funding.postman_collection.json \
		--env-var stable_base=$(STABLE_BASE) \
		--reporters cli,json \
		--reporter-json-export artifacts/endpoints/newman-report.json 2>&1 | tee logs/audit-endpoints.log'

POS_TEST_TAGS := posreadiness
POS_TEST_PKG := ./tests/posreadiness
POS_TEST_BIN := artifacts/pos/posreadiness.test
POS_LOG_DIR := logs/pos
.PHONY:

pos\:build-tests:
	@mkdir -p $(dir $(POS_TEST_BIN))
	go test -c -tags $(POS_TEST_TAGS) -o $(POS_TEST_BIN) $(POS_TEST_PKG)

pos\:run-intent:
	@mkdir -p $(POS_LOG_DIR)
	@bash -o errexit -o nounset -o pipefail -c "( \
	go test -tags $(POS_TEST_TAGS) -run TestIntentReadiness -v ./tests/posreadiness && \
	go test -tags $(POS_TEST_TAGS) -run TestValidIntentFinalizes -v ./tests/posreadiness/intent && \
	go test -tags $(POS_TEST_TAGS) -run TestDuplicateIntentRejected -v ./tests/posreadiness/intent && \
	go test -tags $(POS_TEST_TAGS) -run TestExpiredIntentRejected -v ./tests/posreadiness/intent \
	) 2>&1 | tee $(POS_LOG_DIR)/intent.log"

pos\:run-paymaster:
	@mkdir -p $(POS_LOG_DIR)
	@test -f $(POS_TEST_BIN) || $(MAKE) pos:build-tests
	@bash -o errexit -o nounset -o pipefail -c "$(POS_TEST_BIN) -test.v -test.run TestPaymasterReadiness 2>&1 | tee $(POS_LOG_DIR)/paymaster.log"

pos\:run-registry:
	@mkdir -p $(POS_LOG_DIR)
	@test -f $(POS_TEST_BIN) || $(MAKE) pos:build-tests
	@bash -o errexit -o nounset -o pipefail -c "( \
$(POS_TEST_BIN) -test.v -test.run TestRegistryReadiness && \
go test -tags $(POS_TEST_TAGS) -v ./tests/posreadiness/registry \
) 2>&1 | tee $(POS_LOG_DIR)/registry.log"

pos\:run-realtime:
	@mkdir -p $(POS_LOG_DIR)
	@test -f $(POS_TEST_BIN) || $(MAKE) pos:build-tests
	@bash -o errexit -o nounset -o pipefail -c "$(POS_TEST_BIN) -test.v -test.run TestRealtimeReadiness && go test -tags $(POS_TEST_TAGS) -v ./tests/posreadiness/realtime" 2>&1 | tee $(POS_LOG_DIR)/realtime.log

pos\:run-security:
	@mkdir -p $(POS_LOG_DIR)
	@test -f $(POS_TEST_BIN) || $(MAKE) pos:build-tests
	@bash -o errexit -o nounset -o pipefail -c "$(POS_TEST_BIN) -test.v -test.run TestSecurityReadiness 2>&1 | tee $(POS_LOG_DIR)/security.log"

pos\:run-fees:
	@mkdir -p $(POS_LOG_DIR)
	@test -f $(POS_TEST_BIN) || $(MAKE) pos:build-tests
	@bash -o errexit -o nounset -o pipefail -c "( \
$(POS_TEST_BIN) -test.v -test.run TestFeesReadiness && \
go test -tags $(POS_TEST_TAGS) -v ./tests/posreadiness/fees \
) 2>&1 | tee $(POS_LOG_DIR)/fees.log"

pos\:bench-qos:
	@mkdir -p $(POS_LOG_DIR)
	@test -f $(POS_TEST_BIN) || $(MAKE) pos:build-tests
	@bash -o errexit -o nounset -o pipefail -c "$(POS_TEST_BIN) -test.run '^$' -test.bench BenchmarkPOSQOS -test.benchmem 2>&1 | tee $(POS_LOG_DIR)/bench-qos.log"

pos\:all: pos\:build-tests pos\:run-intent pos\:run-paymaster pos\:run-registry pos\:run-realtime pos\:run-security pos\:run-fees pos\:bench-qos
