# Oneinstack Panel Makefile

# Variables
APP_NAME := one
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
COMMIT_HASH := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")

# Go build variables
GOOS ?= linux
GOARCH ?= amd64
CGO_ENABLED ?= 0

# Directories
BUILD_DIR := dist
PACKAGE_DIR := packages
DOCKER_DIR := docker

# Ldflags
LDFLAGS := -s -w \
	-X main.Version=$(VERSION) \
	-X main.BuildTime=$(BUILD_TIME) \
	-X main.CommitHash=$(COMMIT_HASH)

# Default target
.PHONY: all
all: clean test build package

# Help
.PHONY: help
help:
	@echo "Available targets:"
	@echo "  build          - Build binary for current platform"
	@echo "  build-all      - Build binaries for all platforms"
	@echo "  test           - Run tests"
	@echo "  lint           - Run linter"
	@echo "  clean          - Clean build artifacts"
	@echo "  package        - Create distribution packages"
	@echo "  docker-build   - Build Docker images"
	@echo "  docker-run     - Run Docker containers"
	@echo "  release        - Create release packages"
	@echo "  install        - Install to local system"
	@echo "  uninstall      - Uninstall from local system"

# Build targets
.PHONY: build
build:
	@echo "Building $(APP_NAME) $(VERSION) for $(GOOS)/$(GOARCH)..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)-$(GOOS)-$(GOARCH) ./cmd/main.go

.PHONY: build-all
build-all:
	@echo "Building for all platforms..."
	@$(MAKE) build GOOS=linux GOARCH=amd64
	@$(MAKE) build GOOS=linux GOARCH=arm64
	@$(MAKE) build GOOS=darwin GOARCH=amd64
	@$(MAKE) build GOOS=darwin GOARCH=arm64
	@$(MAKE) build GOOS=windows GOARCH=amd64

# Test targets
.PHONY: test
test:
	@echo "Running tests..."
	go test -v -race -coverprofile=coverage.out ./...

.PHONY: test-coverage
test-coverage: test
	@echo "Generating coverage report..."
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

.PHONY: lint
lint:
	@echo "Running linter..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run --timeout=5m; \
	else \
		echo "golangci-lint not installed, skipping..."; \
	fi

# Package targets
.PHONY: package
package: build-all
	@echo "Creating packages..."
	@mkdir -p $(PACKAGE_DIR)
	@for target in linux-amd64 linux-arm64; do \
		echo "Packaging $$target..."; \
		mkdir -p $(PACKAGE_DIR)/$(APP_NAME)-$$target; \
		cp $(BUILD_DIR)/$(APP_NAME)-$$target $(PACKAGE_DIR)/$(APP_NAME)-$$target/$(APP_NAME); \
		chmod +x $(PACKAGE_DIR)/$(APP_NAME)-$$target/$(APP_NAME); \
		cp config.yaml $(PACKAGE_DIR)/$(APP_NAME)-$$target/; \
		cp install*.sh $(PACKAGE_DIR)/$(APP_NAME)-$$target/; \
		chmod +x $(PACKAGE_DIR)/$(APP_NAME)-$$target/*.sh; \
		cp README*.md LICENSE $(PACKAGE_DIR)/$(APP_NAME)-$$target/; \
		cd $(PACKAGE_DIR) && tar -czf $(APP_NAME)-$$target.tar.gz $(APP_NAME)-$$target/; \
		cd $(PACKAGE_DIR) && sha256sum $(APP_NAME)-$$target.tar.gz > $(APP_NAME)-$$target.tar.gz.sha256; \
	done

# Docker targets
.PHONY: docker-build
docker-build:
	@echo "Building Docker images..."
	docker build -f $(DOCKER_DIR)/Dockerfile.centos -t oneinstack/panel:centos .
	docker build -f $(DOCKER_DIR)/Dockerfile.ubuntu -t oneinstack/panel:ubuntu .
	docker tag oneinstack/panel:centos oneinstack/panel:latest-centos
	docker tag oneinstack/panel:ubuntu oneinstack/panel:latest-ubuntu

.PHONY: docker-run-centos
docker-run-centos:
	@echo "Running CentOS container..."
	docker-compose --profile centos up -d

.PHONY: docker-run-ubuntu
docker-run-ubuntu:
	@echo "Running Ubuntu container..."
	docker-compose --profile ubuntu up -d

.PHONY: docker-run
docker-run: docker-run-centos

.PHONY: docker-stop
docker-stop:
	@echo "Stopping containers..."
	docker-compose down

.PHONY: docker-logs
docker-logs:
	docker-compose logs -f

# Release targets
.PHONY: release
release: clean test lint package
	@echo "Creating release $(VERSION)..."
	@mkdir -p releases/$(VERSION)
	@cp $(PACKAGE_DIR)/*.tar.gz $(PACKAGE_DIR)/*.sha256 releases/$(VERSION)/
	@cd releases/$(VERSION) && cat *.sha256 > checksums.txt
	@echo "Release $(VERSION) created in releases/$(VERSION)/"

# Install targets
.PHONY: install
install: build
	@echo "Installing $(APP_NAME)..."
	@sudo mkdir -p /usr/local/one
	@sudo cp $(BUILD_DIR)/$(APP_NAME)-$(GOOS)-$(GOARCH) /usr/local/one/$(APP_NAME)
	@sudo chmod +x /usr/local/one/$(APP_NAME)
	@sudo ln -sf /usr/local/one/$(APP_NAME) /usr/local/bin/$(APP_NAME)
	@echo "Installation completed!"

.PHONY: uninstall
uninstall:
	@echo "Uninstalling $(APP_NAME)..."
	@sudo rm -f /usr/local/bin/$(APP_NAME)
	@sudo systemctl stop one 2>/dev/null || true
	@sudo systemctl disable one 2>/dev/null || true
	@sudo rm -f /etc/systemd/system/one.service
	@sudo systemctl daemon-reload
	@echo "Uninstallation completed!"

# Development targets
.PHONY: dev
dev:
	@echo "Starting development server..."
	@go run ./cmd/main.go server start

.PHONY: dev-debug
dev-debug:
	@echo "Starting development server in debug mode..."
	@go run ./cmd/main.go debug

# Clean targets
.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	@rm -rf $(BUILD_DIR) $(PACKAGE_DIR) releases coverage.out coverage.html

.PHONY: clean-all
clean-all: clean
	@echo "Cleaning Docker images..."
	@docker rmi oneinstack/panel:centos oneinstack/panel:ubuntu 2>/dev/null || true
	@docker system prune -f

# Dependency management
.PHONY: deps
deps:
	@echo "Downloading dependencies..."
	go mod download
	go mod tidy

.PHONY: deps-update
deps-update:
	@echo "Updating dependencies..."
	go get -u ./...
	go mod tidy

# Version info
.PHONY: version
version:
	@echo "Version: $(VERSION)"
	@echo "Build Time: $(BUILD_TIME)"
	@echo "Commit Hash: $(COMMIT_HASH)"

# Check if required tools are installed
.PHONY: check-tools
check-tools:
	@echo "Checking required tools..."
	@command -v go >/dev/null 2>&1 || (echo "Go is not installed" && exit 1)
	@command -v docker >/dev/null 2>&1 || echo "Docker is not installed (optional)"
	@command -v docker-compose >/dev/null 2>&1 || echo "Docker Compose is not installed (optional)"
	@echo "All required tools are available!"

# Generate build info
.PHONY: build-info
build-info:
	@echo "package main" > buildinfo.go
	@echo "" >> buildinfo.go
	@echo "const (" >> buildinfo.go
	@echo "    Version = \"$(VERSION)\"" >> buildinfo.go
	@echo "    BuildTime = \"$(BUILD_TIME)\"" >> buildinfo.go
	@echo "    CommitHash = \"$(COMMIT_HASH)\"" >> buildinfo.go
	@echo ")" >> buildinfo.go
