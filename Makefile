.PHONY: proto sdk tidy

proto:
	go run ./tools/proto/gen.go

sdk: proto
	cd sdk && go test ./...

# Convenience target to tidy both modules.
tidy:
	go mod tidy
	cd sdk && go mod tidy
