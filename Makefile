.PHONY: proto sdk tidy up down audit:static audit:tests audit:determinism audit:e2e audit:chaos audit:perf audit:netsec audit:ledger audit:supply audit:config audit:docs

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

.PHONY: audit:static
audit:static:
        @mkdir -p logs
        @bash -o errexit -o nounset -o pipefail -c ' \
                go mod tidy 2>&1 | tee logs/go-mod-tidy.log && \
                golangci-lint run ./... 2>&1 | tee logs/golangci-lint.log && \
                govulncheck ./... 2>&1 | tee logs/govulncheck.log && \
                staticcheck ./... 2>&1 | tee logs/staticcheck.log && \
                buf lint 2>&1 | tee logs/buf-lint.log && \
                buf breaking --against ".git#branch=main" 2>&1 | tee logs/buf-breaking.log'

.PHONY: audit:tests
audit:tests:
	@mkdir -p logs
	@bash -o errexit -o nounset -o pipefail -c ' \
		go test ./... 2>&1 | tee logs/go-tests.log && \
		(cd sdk && go test ./... 2>&1 | tee ../logs/sdk-go-tests.log)'

.PHONY: audit:determinism
audit:determinism:
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

.PHONY: audit:e2e
audit:e2e:
	@mkdir -p logs artifacts/e2e
	@bash -o errexit -o nounset -o pipefail -c ' \
		go test -json -run TestAuditSmokePlan ./tests/e2e/... 2>&1 | tee artifacts/e2e/smoke.json > /dev/null && \
		cp artifacts/e2e/smoke.json logs/e2e-smoke.json && \
		python3 scripts/audit/summarize_tests.py --phase e2e \
		--suite smoke=artifacts/e2e/smoke.json \
		--out artifacts/e2e/summary.json \
		--markdown artifacts/e2e/summary.md'
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh e2e ops/audit/e2e.yaml artifacts/e2e --compose deploy/compose/docker-compose.audit-e2e.yaml --hash deploy/compose/docker-compose.audit-e2e.yaml 2>&1 | tee logs/audit-e2e.log'

.PHONY: audit:chaos
audit:chaos:
	@mkdir -p logs artifacts/chaos
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh chaos ops/audit/chaos.yaml artifacts/chaos 2>&1 | tee logs/audit-chaos.log'

.PHONY: audit:perf
audit:perf:
	@mkdir -p logs artifacts/perf
	@bash -o errexit -o nounset -o pipefail -c ' \
		go test -run ^$$ -bench=. -benchtime=100x -benchmem ./tests/perf/... 2>&1 | tee artifacts/perf/bench.txt && \
		cp artifacts/perf/bench.txt logs/perf-bench.txt'
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh perf ops/audit/perf.yaml artifacts/perf --hash artifacts/perf/bench.txt 2>&1 | tee logs/audit-perf.log'

.PHONY: audit:netsec
audit:netsec:
	@mkdir -p logs artifacts/netsec
	@bash -o errexit -o nounset -o pipefail -c ' \
		go test -json ./tests/netsec/... 2>&1 | tee artifacts/netsec/tests.json > /dev/null && \
		cp artifacts/netsec/tests.json logs/netsec-tests.json && \
		python3 scripts/audit/summarize_tests.py --phase netsec \
		--suite netsec=artifacts/netsec/tests.json \
		--out artifacts/netsec/summary.json \
		--markdown artifacts/netsec/summary.md'
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh netsec ops/audit/netsec.yaml artifacts/netsec 2>&1 | tee logs/audit-netsec.log'

.PHONY: audit:ledger
audit:ledger:
	@mkdir -p logs artifacts/ledger
	@bash -o errexit -o nounset -o pipefail -c ' \
		go test -json ./tests/ledger/... 2>&1 | tee artifacts/ledger/tests.json > /dev/null && \
		cp artifacts/ledger/tests.json logs/ledger-tests.json && \
		python3 scripts/audit/summarize_tests.py --phase ledger \
		--suite ledger=artifacts/ledger/tests.json \
		--out artifacts/ledger/summary.json \
		--markdown artifacts/ledger/summary.md'
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh ledger ops/audit/ledger.yaml artifacts/ledger --hash ops/audit/ledger.yaml 2>&1 | tee logs/audit-ledger.log'

.PHONY: audit:supply
audit:supply:
	@mkdir -p logs artifacts/supply
	@bash -o errexit -o nounset -o pipefail -c ' \
		go test -json -run TestSupplyFixtureBalances ./tests/ledger/... 2>&1 | tee artifacts/supply/tests.json > /dev/null && \
		cp artifacts/supply/tests.json logs/supply-tests.json && \
		python3 scripts/audit/summarize_tests.py --phase supply \
		--suite supply=artifacts/supply/tests.json \
		--out artifacts/supply/summary.json \
		--markdown artifacts/supply/summary.md'
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh supply ops/audit/supply.yaml artifacts/supply --hash ops/audit/supply.yaml 2>&1 | tee logs/audit-supply.log'

.PHONY: audit:config
audit:config:
	@mkdir -p logs artifacts/config
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh config ops/audit/config.yaml artifacts/config --hash config/config.toml --hash config-peer.toml --hash config-local.toml 2>&1 | tee logs/audit-config.log'

.PHONY: audit:docs
audit:docs:
	@mkdir -p logs artifacts/docs
	@bash -o errexit -o nounset -o pipefail -c 'go run ./tools/docs/verify.go 2>&1 | tee logs/docs-verify.log'
	@bash -o errexit -o nounset -o pipefail -c './scripts/audit/run_phase.sh docs ops/audit/docs.yaml artifacts/docs --hash docs/security/audit-readiness.md --hash ops/audit-pack/BUILD_STEPS.md 2>&1 | tee logs/audit-docs.log'
