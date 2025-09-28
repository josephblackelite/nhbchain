.PHONY: proto sdk tidy up down audit:static

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
