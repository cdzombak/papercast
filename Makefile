SHELL:=/usr/bin/env bash

BIN_NAME:=papercast
BIN_VERSION:=$(shell ./.version.sh)
BUILD_PKG:=.

default: help
.PHONY: help
help: ## Print help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

.PHONY: all
all: clean build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 ## Build for macOS and Linux (amd64, arm64)

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

.PHONY: build-darwin-amd64
build-darwin-amd64: ## Build for macOS/amd64 to ./out
	mkdir -p out
	env CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="-X main.version=${BIN_VERSION}" -o ./out/${BIN_NAME}-${BIN_VERSION}-darwin-amd64 ${BUILD_PKG}

.PHONY: build-darwin-arm64
build-darwin-arm64: ## Build for macOS/arm64 to ./out
	mkdir -p out
	env CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="-X main.version=${BIN_VERSION}" -o ./out/${BIN_NAME}-${BIN_VERSION}-darwin-arm64 ${BUILD_PKG}

.PHONY: package
package: all ## Build all binaries + .deb packages to ./out (requires fpm: https://fpm.readthedocs.io)
	fpm -t deb -v ${BIN_VERSION} -p ./out/${BIN_NAME}-${BIN_VERSION}-amd64.deb -a amd64 ./out/${BIN_NAME}-${BIN_VERSION}-linux-amd64=/usr/bin/${BIN_NAME}
	fpm -t deb -v ${BIN_VERSION} -p ./out/${BIN_NAME}-${BIN_VERSION}-arm64.deb -a arm64 ./out/${BIN_NAME}-${BIN_VERSION}-linux-arm64=/usr/bin/${BIN_NAME}

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
