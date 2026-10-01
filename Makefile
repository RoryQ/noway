.PHONY: all build test test-floci test-integration floci-up floci-down floci-logs vet clean help

BINARY_NAME ?= noway
BIN_DIR ?= bin
FLOCI_ENDPOINT ?= http://localhost:4588/bigquery/v2/

all: build test

## build: Build the noway CLI binary
build:
	@mkdir -p $(BIN_DIR)
	go build -v -o $(BIN_DIR)/$(BINARY_NAME) ./cmd/noway

## test: Run unit tests across all packages
test:
	go test -v -count=1 ./...

## test-floci: Start floci-gcp emulator via docker compose and run integration tests
test-floci: floci-up
	@echo "Running tests against floci-gcp emulator..."
	@FLOCI_BIGQUERY_ENDPOINT=$(FLOCI_ENDPOINT) go test -v -count=1 ./...
	@$(MAKE) floci-down

## test-integration: Alias for test-floci
test-integration: test-floci

## floci-up: Start the floci-gcp emulator container in background and wait until ready
floci-up:
	docker compose up -d
	@echo "Waiting for floci-gcp to be ready..."
	@for i in $$(seq 1 30); do \
		if curl -s -f http://localhost:4588/health > /dev/null 2>&1; then \
			echo "floci-gcp is ready!"; \
			exit 0; \
		fi; \
		sleep 0.2; \
	done; \
	echo "Timeout waiting for floci-gcp"; exit 1

## floci-down: Stop and remove the floci-gcp emulator container
floci-down:
	docker compose down

## floci-logs: Stream logs from the floci-gcp emulator container
floci-logs:
	docker compose logs -f

## vet: Run go vet on codebase
vet:
	go vet ./...

## clean: Remove built binaries and test artifacts
clean:
	rm -rf $(BIN_DIR)

## help: Display available targets
help:
	@echo "Available make targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed -e 's/## /  /'
