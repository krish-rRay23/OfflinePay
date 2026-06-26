# OFFLINEPAY SYSTEM BUILD AND TELEMETRY MAKEFILE

BINARY_DIR=bin
SERVER_BIN=$(BINARY_DIR)/offlinepay-server
SIMULATION_BIN=$(BINARY_DIR)/offlinepay-simulation
REBUILD_BIN=$(BINARY_DIR)/offlinepay-rebuild
REPLAYSTORM_BIN=$(BINARY_DIR)/offlinepay-replaystorm
SEED_BIN=$(BINARY_DIR)/offlinepay-seed

.PHONY: all build test benchmark fuzz replaystorm rebuild lint docker seed migrate bootstrap clean dev

all: build test

build: clean
	@echo "Building all project binaries..."
	@mkdir -p $(BINARY_DIR)
	go build -o $(SERVER_BIN) cmd/server/main.go
	go build -o $(SIMULATION_BIN) cmd/simulation/main.go
	go build -o $(REBUILD_BIN) cmd/rebuild/main.go
	go build -o $(REPLAYSTORM_BIN) cmd/replaystorm/main.go
	go build -o $(SEED_BIN) cmd/seed/main.go

dev:
	@echo "Starting local OfflinePay server..."
	go run cmd/server/main.go

test:
	@echo "Executing Go automated test suite..."
	go test -v ./...

benchmark:
	@echo "Executing micro-benchmarks..."
	go test -v -run=^$$ -bench=. ./internal/...

fuzz:
	@echo "Executing fuzz tests..."
	go test -v -fuzz=Fuzz -fuzztime=10s ./internal/crypto/...

replaystorm:
	@echo "Running concurrent replay storm stress test..."
	go run cmd/replaystorm/main.go

rebuild:
	@echo "Replaying outbox projections rebuild..."
	go run cmd/rebuild/main.go

lint:
	@echo "Verifying code formatting and static analysis..."
	go fmt ./...
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed, skipped secondary analysis."; \
	fi

docker:
	@echo "Launching containers via docker-compose..."
	docker compose up -d --build

migrate:
	@echo "Applying database migrations (starts server temporarily)..."
	go run cmd/server/main.go -migrate-only

seed:
	@echo "Seeding database with default accounts and devices..."
	go run cmd/seed/main.go

bootstrap: build seed test
	@echo "OfflinePay system bootstrap completed successfully."

clean:
	@echo "Cleaning up build artifacts..."
	rm -rf $(BINARY_DIR)
	rm -f server.exe simulation.exe rebuild.exe replaystorm.exe
