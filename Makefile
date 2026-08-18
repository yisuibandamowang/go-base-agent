.PHONY: run build test lint migrate cleanup-db clean

APP_NAME    := ragent
BUILD_DIR   := build
GO          := go
GOFLAGS     := -trimpath -ldflags="-s -w"

run:
	$(GO) run ./cmd/ragent

build:
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(APP_NAME) ./cmd/ragent
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/mcp-server ./cmd/mcp-server

test:
	$(GO) test -race -cover ./...

test-integration:
	$(GO) test -tags=integration -count=1 ./...

lint:
	golangci-lint run ./...

fmt:
	gofumpt -l -w .
	goimports -local go-base-agent -w .

vet:
	$(GO) vet ./...

mod:
	$(GO) mod tidy

migrate:
	bash scripts/migrate.sh

cleanup-db:
	bash scripts/cleanup.sh

clean:
	rm -rf $(BUILD_DIR)
	$(GO) clean -cache -testcache
