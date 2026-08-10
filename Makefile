# OneinStack Panel reproducible build entry points.

APP_NAME := one
WEB_DIR ?= ../Oneinstack-Panel-Web
WEB_ARCHIVE ?= $(WEB_DIR)/version/app-1.0.0.zip

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
COMMIT_HASH ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
WEB_VERSION ?= 1.0.0

GOOS ?= linux
GOARCH ?= amd64
CGO_ENABLED ?= 0

BUILD_DIR := dist
PACKAGE_DIR := packages
LDFLAGS := -s -w \
	-X oneinstack/internal/buildinfo.Version=$(VERSION) \
	-X oneinstack/internal/buildinfo.BuildTime=$(BUILD_TIME) \
	-X oneinstack/internal/buildinfo.CommitHash=$(COMMIT_HASH) \
	-X oneinstack/internal/buildinfo.WebVersion=$(WEB_VERSION)

.PHONY: all
all: quality package verify-release

.PHONY: help
help:
	@echo "OneinStack Panel build targets:"
	@echo "  quality          Run formatting, vet, tests and shell syntax checks"
	@echo "  secret-scan      Scan tracked source for high-confidence credential patterns"
	@echo "  install-test     Run isolated Bats installer contract tests"
	@echo "  build            Build one GOOS/GOARCH target (defaults to linux/amd64)"
	@echo "  build-all        Build supported linux/amd64 and linux/arm64 binaries"
	@echo "  build-ui         Build the sibling frontend and refresh webui/app.zip"
	@echo "  package          Build and package both supported Linux targets"
	@echo "  verify-release   Verify package checksums and required contents"
	@echo "  release          Run all release gates and create local packages"
	@echo "  dev              Start the backend development server"
	@echo "  clean            Remove generated local build and package output"

.PHONY: format-check
format-check:
	@files="$$(gofmt -l $$(find . -path './.git' -prune -o -type f -name '*.go' -print))"; \
	if [ -n "$$files" ]; then \
		echo "The following files need gofmt:"; \
		echo "$$files"; \
		exit 1; \
	fi

.PHONY: lint
lint:
	go vet ./...

.PHONY: test
test:
	go test -count=1 ./...

.PHONY: test-race
test-race:
	go test -race -count=1 ./...

.PHONY: shell-check
shell-check:
	find . -maxdepth 2 -type f -name '*.sh' -print0 | xargs -0 -n1 bash -n

.PHONY: secret-scan
secret-scan:
	./scripts/secret-scan.sh

.PHONY: install-test
install-test:
	@command -v bats >/dev/null 2>&1 || \
		(echo "bats is required for installer tests" && exit 1)
	bats tests/install.bats

.PHONY: quality
quality: format-check lint test shell-check secret-scan

.PHONY: build
build:
	@echo "Building $(APP_NAME) $(VERSION) for $(GOOS)/$(GOARCH)"
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build -trimpath -ldflags="$(LDFLAGS)" \
		-o $(BUILD_DIR)/$(APP_NAME)-$(GOOS)-$(GOARCH) ./cmd

.PHONY: build-all
build-all:
	@$(MAKE) build GOOS=linux GOARCH=amd64 \
		VERSION="$(VERSION)" BUILD_TIME="$(BUILD_TIME)" \
		COMMIT_HASH="$(COMMIT_HASH)" WEB_VERSION="$(WEB_VERSION)"
	@$(MAKE) build GOOS=linux GOARCH=arm64 \
		VERSION="$(VERSION)" BUILD_TIME="$(BUILD_TIME)" \
		COMMIT_HASH="$(COMMIT_HASH)" WEB_VERSION="$(WEB_VERSION)"

.PHONY: build-ui
build-ui:
	@echo "Building production UI from $(WEB_DIR)"
	cd $(WEB_DIR) && npm run build
	./scripts/sync-webui.sh $(WEB_ARCHIVE)
	go test ./webui

.PHONY: package
package: build-all
	./scripts/package-release.sh "$(VERSION)" "$(PACKAGE_DIR)"

.PHONY: verify-release
verify-release:
	@set -- $(PACKAGE_DIR)/*.tar.gz; \
	if [ ! -e "$$1" ]; then \
		echo "No release archives found; run make package first"; \
		exit 1; \
	fi; \
	./scripts/verify-release.sh "$$@"

.PHONY: release
release: quality package verify-release
	@echo "Release packages are ready in $(PACKAGE_DIR)/"

.PHONY: test-coverage
test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

.PHONY: dev
dev:
	GO_ENV=development go run ./cmd server start

.PHONY: dev-debug
dev-debug:
	GO_ENV=development go run ./cmd debug

.PHONY: deps
deps:
	go mod download

.PHONY: version
version:
	@echo "Version: $(VERSION)"
	@echo "Build time: $(BUILD_TIME)"
	@echo "Commit: $(COMMIT_HASH)"
	@echo "Web version: $(WEB_VERSION)"

.PHONY: check-tools
check-tools:
	@command -v go >/dev/null 2>&1 || (echo "Go is required" && exit 1)
	@command -v tar >/dev/null 2>&1 || (echo "tar is required" && exit 1)
	@command -v sha256sum >/dev/null 2>&1 || \
		command -v shasum >/dev/null 2>&1 || \
		(echo "sha256sum or shasum is required" && exit 1)

.PHONY: clean
clean:
	rm -rf $(BUILD_DIR) $(PACKAGE_DIR) coverage.out coverage.html
