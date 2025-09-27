.PHONY: proto sdk tidy up down

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
