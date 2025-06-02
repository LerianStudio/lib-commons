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

.PHONY: test-contracts
test-contracts: ## Run contract tests for interface stability validation
	@echo "Running contract tests..."
	@go test -v ./contracts/...
	@echo "Contract tests completed"

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
	@golangci-lint run --no-config --enable=govet,staticcheck,errcheck,unused,ineffassign,misspell --fix ./...
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
check: format lint test test-contracts ## Run format, lint, test, and contract tests
	@echo "All checks passed"

.PHONY: docs
docs: ## Generate API documentation
	@echo "Generating API documentation..."
	@if command -v swagger-codegen >/dev/null 2>&1; then \
		swagger-codegen validate -i docs/api/openapi.yaml && \
		swagger-codegen generate -i docs/api/openapi.yaml -l html2 -o docs/api/generated/ && \
		echo "✅ API documentation generated at docs/api/generated/"; \
	else \
		echo "⚠️ swagger-codegen not found. Install with: npm install -g @apidevtools/swagger-cli"; \
		echo "📚 OpenAPI spec available at: docs/api/openapi.yaml"; \
	fi

.PHONY: docs-validate
docs-validate: ## Validate OpenAPI specification
	@echo "Validating OpenAPI specification..."
	@if command -v swagger-codegen >/dev/null 2>&1; then \
		swagger-codegen validate -i docs/api/openapi.yaml && \
		echo "✅ OpenAPI specification is valid"; \
	else \
		echo "⚠️ swagger-codegen not found. Skipping validation."; \
	fi

.PHONY: ci
ci: check docs-validate ## Run all CI checks (format, lint, test, contracts, docs)
	@echo "CI checks completed successfully"