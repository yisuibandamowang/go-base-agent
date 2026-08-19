.PHONY: run build test lint migrate cleanup-db preflight initialize clean

APP_NAME    := ragent
BUILD_DIR   := build
AGENT_TYPE_DIR ?= resources/initializer/enterprise-knowledge-base
GO          := go
GOFLAGS     := -trimpath -ldflags="-s -w"

run:
	$(GO) run ./cmd/ragent

build:
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(APP_NAME) ./cmd/ragent
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/mcp-server ./cmd/mcp-server
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/initializer ./cmd/initializer

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
	$(GO) run ./cmd/initializer migrate

cleanup-db:
	$(GO) run ./cmd/initializer cleanup --confirm RESET-ENTERPRISE-KNOWLEDGE-BASE

preflight:
	$(GO) run ./cmd/initializer preflight

initialize:
	$(GO) run ./cmd/initializer initialize --agent-type-dir $(AGENT_TYPE_DIR) --confirm RESET-ENTERPRISE-KNOWLEDGE-BASE

clean:
	rm -rf $(BUILD_DIR)
	$(GO) clean -cache -testcache
