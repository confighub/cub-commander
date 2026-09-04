# cub-commander — terminal lab for ConfigHub, published as a cub plugin
BINARY   := cub-commander
BIN_DIR  := bin
GO       ?= go
CUB      ?= cub

GIT_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
VERSION ?= $(if $(GIT_VERSION),$(GIT_VERSION),dev)
LDFLAGS := -X github.com/confighub/cub-commander/cmd.version=$(VERSION)

.PHONY: build test vet fmt check plugin plugin-uninstall clean

build: ## Build into bin/
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) .

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

check: vet test

plugin: build ## Install into cub from this checkout
	$(CUB) plugin uninstall commander >/dev/null 2>&1 || true; $(CUB) plugin install ./$(BIN_DIR)/$(BINARY)

plugin-uninstall:
	$(CUB) plugin uninstall commander

clean:
	rm -rf $(BIN_DIR)
