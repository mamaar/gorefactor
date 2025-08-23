.PHONY: all build test clean install help

# Default target
all: build

# Build the CLI binary
build:
	@echo "Building gorefactor..."
	@go build -o bin/gorefactor ./cmd/gorefactor

# Build all binaries
build-all-binaries: build

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

# Install the binary to GOPATH/bin
install: build
	@echo "Installing gorefactor to GOPATH/bin..."
	@cp bin/gorefactor $(GOPATH)/bin/
	@echo "Installed successfully!"

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf bin/
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

# Build for multiple platforms
build-all:
	@echo "Building for multiple platforms..."
	@mkdir -p bin
	@GOOS=linux GOARCH=amd64 go build -o bin/gorefactor-linux-amd64 ./cmd/gorefactor
	@GOOS=darwin GOARCH=amd64 go build -o bin/gorefactor-darwin-amd64 ./cmd/gorefactor
	@GOOS=darwin GOARCH=arm64 go build -o bin/gorefactor-darwin-arm64 ./cmd/gorefactor
	@GOOS=windows GOARCH=amd64 go build -o bin/gorefactor-windows-amd64.exe ./cmd/gorefactor
	@echo "Multi-platform build complete!"

# Quick test on sample code
test-sample:
	@echo "Testing on sample code..."
	@make build
	@./bin/gorefactor analyze TestFunction testdata/simple || true
	@./bin/gorefactor --dry-run rename TestFunction RenamedFunction testdata/simple || true

# Development workflow - build and test
dev: fmt build test

# Show help
help:
	@echo "GoRefactor Makefile"
	@echo ""
	@echo "Available targets:"
	@echo "  make build         - Build the gorefactor binary"
	@echo "  make build-all-binaries - Build all binaries"
	@echo "  make test          - Run all tests"
	@echo "  make test-coverage - Run tests with coverage report"
	@echo "  make install       - Install binary to GOPATH/bin"
	@echo "  make clean         - Clean build artifacts"
	@echo "  make fmt           - Format all Go code"
	@echo "  make lint          - Run linter"
	@echo "  make build-all     - Build for multiple platforms"
	@echo "  make test-sample   - Test on sample code"
	@echo "  make dev           - Format, build, and test"
	@echo "  make help          - Show this help message"