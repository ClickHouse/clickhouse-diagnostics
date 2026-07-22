# ClickHouse Diagnostic Tool Makefile

# Variables
BINARY_NAME=clickhouse-diagnostic
GO_FILES=$(shell find . -name "*.go" -type f)
BUILD_DIR=./bin
CMD_DIR=./cmd
DIST_DIR=./dist

# Platforms to package release archives for (os/arch)
PLATFORMS=linux/amd64 darwin/amd64 darwin/arm64 windows/amd64
# Runtime data the tool reads from the working directory. These must ship
# alongside the binary, so each release archive bundles them.
DATA_DIRS=queries.cloud queries.onprem queries.gov queries.query_analysis alerts

# Default target
.PHONY: all
all: build

# Download dependencies
.PHONY: deps
deps:
	go mod download
	go mod tidy
	@echo "Dependencies downloaded and tidied"

# Build the application
.PHONY: build
build: $(BUILD_DIR)/$(BINARY_NAME)

$(BUILD_DIR)/$(BINARY_NAME): $(GO_FILES) go.mod go.sum
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)
	@echo "Built $(BINARY_NAME) successfully"

# Run the application
.PHONY: run
run: build
	$(BUILD_DIR)/$(BINARY_NAME)

# Clean build artifacts
.PHONY: clean
clean:
	rm -rf $(BUILD_DIR)
	rm -rf $(DIST_DIR)
	rm -rf clickhouse_results
	rm -rf configuration
	rm -f *.tar.gz
	@echo "Cleaned build artifacts"

# Test the application (when tests are added)
.PHONY: test
test:
	go test -v ./...

# Format code
.PHONY: fmt
fmt:
	go fmt ./...

# Lint code (requires golangci-lint)
.PHONY: lint
lint:
	golangci-lint run

# Create release archives for multiple platforms.
# Each archive bundles the binary with the runtime data dirs (queries.*,
# alerts) so the tool works from the extracted folder — the binary alone
# reads those files from the working directory and would otherwise fail.
# Produces .tar.gz for Unix targets and .zip for Windows, under $(DIST_DIR).
.PHONY: release
release: clean
	@mkdir -p $(DIST_DIR)
	@set -e; \
	for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		pkg=$(BINARY_NAME)-$$os-$$arch; \
		stage=$(DIST_DIR)/$$pkg; \
		bin=$(BINARY_NAME); \
		if [ "$$os" = "windows" ]; then bin=$(BINARY_NAME).exe; fi; \
		echo "Packaging $$pkg..."; \
		mkdir -p $$stage; \
		GOOS=$$os GOARCH=$$arch go build -o $$stage/$$bin $(CMD_DIR) || exit 1; \
		cp -R $(DATA_DIRS) $$stage/; \
		cp README.md $$stage/ 2>/dev/null || true; \
		if [ "$$os" = "windows" ]; then \
			(cd $(DIST_DIR) && zip -qr $$pkg.zip $$pkg); \
		else \
			tar -czf $(DIST_DIR)/$$pkg.tar.gz -C $(DIST_DIR) $$pkg; \
		fi; \
		rm -rf $$stage; \
	done
	@echo "Release archives created in $(DIST_DIR)/"

# Install the binary to GOPATH/bin
.PHONY: install
install:
	go install $(CMD_DIR)

# Setup example queries
.PHONY: setup
setup:
	cp -r example-queries queries || echo "example-queries directory not found"
	@echo "Setup complete - queries directory created"

# Show help
.PHONY: help
help:
	@echo "Available commands:"
	@echo "  deps      - Download and tidy dependencies"
	@echo "  build     - Build the application"
	@echo "  run       - Build and run the application"
	@echo "  setup     - Copy example queries to queries directory"
	@echo "  clean     - Clean build artifacts and output files"
	@echo "  test      - Run tests"
	@echo "  fmt       - Format Go code"
	@echo "  lint      - Lint Go code (requires golangci-lint)"
	@echo "  release   - Create release builds for multiple platforms"
	@echo "  install   - Install binary to GOPATH/bin"
	@echo "  help      - Show this help message"