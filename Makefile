SHELL:=/usr/bin/env bash

BIN_NAME:=papercast
BIN_VERSION:=$(shell ./.version.sh)
BUILD_PKG:=.

default: help
.PHONY: help
help: ## Print help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

.PHONY: clean
clean: ## Remove build products (./out)
	rm -rf ./out

.PHONY: build
build: ## Build for the current platform & architecture to ./out
	mkdir -p out
	env CGO_ENABLED=0 go build -ldflags="-X main.version=${BIN_VERSION}" -o ./out/${BIN_NAME} ${BUILD_PKG}

.PHONY: build-linux-amd64
build-linux-amd64: ## Build for Linux/amd64 to ./out
	mkdir -p out
	env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-X main.version=${BIN_VERSION}" -o ./out/${BIN_NAME}-${BIN_VERSION}-linux-amd64 ${BUILD_PKG}

.PHONY: build-linux-arm64
build-linux-arm64: ## Build for Linux/arm64 to ./out
	mkdir -p out
	env CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-X main.version=${BIN_VERSION}" -o ./out/${BIN_NAME}-${BIN_VERSION}-linux-arm64 ${BUILD_PKG}

.PHONY: build-docker
build-docker: ## Build a Docker image for the current machine
	docker build --build-arg BIN_VERSION=${BIN_VERSION} -t ${BIN_NAME}:${BIN_VERSION} -t ${BIN_NAME}:latest .

.PHONY: test
test: ## Run tests
	go test -race ./...

.PHONY: lint
lint: ## Lint all Go source files (requires golangci-lint: https://golangci-lint.run)
	golangci-lint run
	gofmt -l . | tee /dev/stderr | wc -l | grep -q '^ *0$$'
	go vet ./...

.PHONY: actionlint
actionlint: ## Lint GitHub Actions workflows (requires actionlint)
	actionlint
