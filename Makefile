# Commons-Go Library Makefile
.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help message
	@echo "Commons-Go Library"
	@echo "Available commands:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

.PHONY: test
test: ## Run all tests
	@echo "Running tests..."
	@go test -v ./...
	@echo "Tests completed"

.PHONY: test-cover
test-cover: ## Run tests with coverage
	@echo "Running tests with coverage..."
	@go test -v -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

.PHONY: bench
bench: ## Run benchmarks
	@echo "Running benchmarks..."
	@go test -bench=. -benchmem ./...
	@echo "Benchmarks completed"

.PHONY: lint
lint: ## Run linter
	@echo "Running linter..."
	@golangci-lint run --fix ./...
	@echo "Linting completed"

.PHONY: format
format: ## Format code
	@echo "Formatting code..."
	@gofmt -w .
	@echo "Code formatted"

.PHONY: tidy
tidy: ## Clean up dependencies
	@echo "Tidying dependencies..."
	@go mod tidy
	@echo "Dependencies cleaned"

.PHONY: clean
clean: ## Clean build artifacts
	@echo "Cleaning build artifacts..."
	@rm -f coverage.out coverage.html
	@echo "Clean completed"

.PHONY: check
check: format lint test ## Run format, lint, and test
	@echo "All checks passed"