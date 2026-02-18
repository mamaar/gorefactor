.PHONY: all build test clean install help

# Default target
all: build

# Build the MCP server binary
build:
	@echo "Building gorefactor-mcp..."
	@go build -o gorefactor-mcp ./cmd/gorefactor-mcp

# Run all tests
test:
	@echo "Running tests..."
	@go test -v ./...

# Run tests with coverage
test-coverage:
	@echo "Running tests with coverage..."
	@go test -v -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

# Install the binary
install: build
	@echo "Installing gorefactor-mcp to GOPATH/bin..."
	@cp gorefactor-mcp $(GOPATH)/bin/
	@echo "Installed successfully!"

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -f gorefactor-mcp
	@rm -f coverage.out coverage.html
	@echo "Clean complete!"

# Format all Go code
fmt:
	@echo "Formatting code..."
	@go fmt ./...

# Run linter
lint:
	@echo "Running linter..."
	@golangci-lint run || echo "Install golangci-lint: https://golangci-lint.run/usage/install/"

# Development workflow - build and test
dev: fmt build test

# Show help
help:
	@echo "GoRefactor Makefile"
	@echo ""
	@echo "Available targets:"
	@echo "  make build         - Build the MCP server binary"
	@echo "  make test          - Run all tests"
	@echo "  make test-coverage - Run tests with coverage report"
	@echo "  make install       - Install binary to GOPATH/bin"
	@echo "  make clean         - Clean build artifacts"
	@echo "  make fmt           - Format all Go code"
	@echo "  make lint          - Run linter"
	@echo "  make dev           - Format, build, and test"
	@echo "  make help          - Show this help message"
