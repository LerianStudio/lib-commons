# Commons-Go Library Makefile
.DEFAULT_GOAL := help

# Colors for output
BLUE := \033[34m
GREEN := \033[32m
YELLOW := \033[33m
RED := \033[31m
NC := \033[0m
BOLD := \033[1m

.PHONY: help
help: ## Show this help message
	@echo "$(BOLD)Commons-Go Library$(NC)"
	@echo "Available commands:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  $(BLUE)%-12s$(NC) %s\n", $$1, $$2}'

.PHONY: test
test: ## Run all tests
	@echo "$(BLUE)Running tests...$(NC)"
	@go test -v ./...
	@echo "$(GREEN)✓ Tests completed$(NC)"

.PHONY: test-cover
test-cover: ## Run tests with coverage
	@echo "$(BLUE)Running tests with coverage...$(NC)"
	@go test -v -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "$(GREEN)✓ Coverage report generated: coverage.html$(NC)"

.PHONY: bench
bench: ## Run benchmarks
	@echo "$(BLUE)Running benchmarks...$(NC)"
	@go test -bench=. -benchmem ./...
	@echo "$(GREEN)✓ Benchmarks completed$(NC)"

.PHONY: lint
lint: ## Run linter
	@echo "$(BLUE)Running linter...$(NC)"
	@golangci-lint run --fix ./...
	@echo "$(GREEN)✓ Linting completed$(NC)"

.PHONY: format
format: ## Format code
	@echo "$(BLUE)Formatting code...$(NC)"
	@gofmt -w .
	@echo "$(GREEN)✓ Code formatted$(NC)"

.PHONY: tidy
tidy: ## Clean up dependencies
	@echo "$(BLUE)Tidying dependencies...$(NC)"
	@go mod tidy
	@echo "$(GREEN)✓ Dependencies cleaned$(NC)"

.PHONY: clean
clean: ## Clean build artifacts
	@echo "$(BLUE)Cleaning build artifacts...$(NC)"
	@rm -f coverage.out coverage.html
	@echo "$(GREEN)✓ Clean completed$(NC)"

.PHONY: check
check: format lint test ## Run format, lint, and test
	@echo "$(GREEN)$(BOLD)✓ All checks passed$(NC)"