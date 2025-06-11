# ClickHouse Diagnostic Tool Makefile

# Variables
BINARY_NAME=clickhouse-diagnostic
GO_FILES=$(shell find . -name "*.go" -type f)
BUILD_DIR=./bin
CMD_DIR=./cmd

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

# Create release builds for multiple platforms
.PHONY: release
release: clean
	@mkdir -p $(BUILD_DIR)
	# Linux
	GOOS=linux GOARCH=amd64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(CMD_DIR)
	# macOS
	GOOS=darwin GOARCH=amd64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(CMD_DIR)
	GOOS=darwin GOARCH=arm64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(CMD_DIR)
	# Windows
	GOOS=windows GOARCH=amd64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe $(CMD_DIR)
	@echo "Release builds created in $(BUILD_DIR)/"

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