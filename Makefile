# Define the root directory of the project
LIB_COMMONS := $(shell pwd)

# Include shared color definitions and utility functions
include $(LIB_COMMONS)/commons/shell/makefile_colors.mk
include $(LIB_COMMONS)/commons/shell/makefile_utils.mk

# Define common utility functions
define print_title
	@echo ""
	@echo "------------------------------------------"
	@echo "   📝 $(1)  "
	@echo "------------------------------------------"
endef

#-------------------------------------------------------
# Help Command
#-------------------------------------------------------

.PHONY: help
help:
	@echo ""
	@echo ""
	@echo "Lib-Commons Project Management Commands"
	@echo ""
	@echo ""
	@echo "Core Commands:"
	@echo "  make help                        - Display this help message"
	@echo "  make test                        - Run tests on all packages"
	@echo "  make build                       - Build all packages"
	@echo "  make clean                       - Clean all build artifacts"
	@echo "  make cover                       - Run test coverage"
	@echo ""
	@echo ""
	@echo "Code Quality Commands:"
	@echo "  make lint                        - Run linting on all packages"
	@echo "  make format                      - Format code in all packages"
	@echo "  make tidy                        - Clean dependencies"
	@echo "  make check-tests                 - Verify test coverage for packages"
	@echo "  make sec                         - Run security checks using gosec"
	@echo ""
	@echo ""
	@echo "Git Hook Commands:"
	@echo "  make setup-git-hooks             - Install and configure git hooks"
	@echo "  make check-hooks                 - Verify git hooks installation status"
	@echo "  make check-envs                  - Check if github hooks are installed and secret env files are not exposed"
	@echo ""
	@echo ""
	@echo "Release Commands:"
	@echo "  make goreleaser                  - Create release snapshot with goreleaser"
	@echo ""
	@echo ""

#-------------------------------------------------------
# Core Commands
#-------------------------------------------------------

.PHONY: test
test:
	$(call print_title,Running tests on all packages)
	$(call check_command,go,"Install Go from https://golang.org/doc/install")
	go test -v ./...
	@echo "$(GREEN)$(BOLD)[ok]$(NC) All tests passed$(GREEN) ✔️$(NC)"

.PHONY: build
build:
	$(call print_title,Building all packages)
	$(call check_command,go,"Install Go from https://golang.org/doc/install")
	go build ./...
	@echo "$(GREEN)$(BOLD)[ok]$(NC) All packages built successfully$(GREEN) ✔️$(NC)"

.PHONY: clean
clean:
	$(call print_title,Cleaning build artifacts)
	@rm -rf ./bin ./dist coverage.out coverage.html
	@go clean -cache -testcache
	@echo "$(GREEN)$(BOLD)[ok]$(NC) All build artifacts cleaned$(GREEN) ✔️$(NC)"

.PHONY: cover
cover:
	$(call print_title,Generating test coverage report)
	$(call check_command,go,"Install Go from https://golang.org/doc/install")
	@sh ./scripts/coverage.sh
	@go tool cover -html=coverage.out -o coverage.html
	@echo ""
	@echo "Coverage Summary:"
	@echo "----------------------------------------"
	@go tool cover -func=coverage.out | grep total | awk '{print "Total coverage: " $$3}'
	@echo "----------------------------------------"
	@echo "Open coverage.html in your browser to view detailed coverage report"
	@echo "$(GREEN)$(BOLD)[ok]$(NC) Coverage report generated successfully$(GREEN) ✔️$(NC)"

#-------------------------------------------------------
# Code Quality Commands
#-------------------------------------------------------

.PHONY: lint
lint:
	$(call print_title,Running linters on all packages)
	$(call check_command,golangci-lint,"go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest")
	@out=$$(golangci-lint run --fix ./... 2>&1); \
	out_err=$$?; \
	perf_out=$$(perfsprint ./... 2>&1); \
	perf_err=$$?; \
	echo "$$out"; \
	echo "$$perf_out"; \
	if [ $$out_err -ne 0 ]; then \
		echo -e "\n$(BOLD)$(RED)An error has occurred during the lint process: \n $$out\n"; \
		exit 1; \
	fi; \
	if [ $$perf_err -ne 0 ]; then \
		echo -e "\n$(BOLD)$(RED)An error has occurred during the performance check: \n $$perf_out\n"; \
		exit 1; \
	fi
	@echo "$(GREEN)$(BOLD)[ok]$(NC) Lint and performance checks passed successfully$(GREEN) ✔️$(NC)"

.PHONY: format
format:
	$(call print_title,Formatting code in all packages)
	$(call check_command,gofmt,"Install Go from https://golang.org/doc/install")
	@gofmt -w ./
	@echo "$(GREEN)$(BOLD)[ok]$(NC) All go files formatted$(GREEN) ✔️$(NC)"

.PHONY: check-tests
check-tests:
	$(call print_title,Verifying test coverage for packages)
	@if [ -f "./scripts/check-tests.sh" ]; then \
		sh ./scripts/check-tests.sh; \
	else \
		echo "Running basic test coverage check..."; \
		go test -cover ./...; \
	fi
	@echo "$(GREEN)$(BOLD)[ok]$(NC) Test coverage verification completed$(GREEN) ✔️$(NC)"

#-------------------------------------------------------
# Git Hook Commands
#-------------------------------------------------------

.PHONY: setup-git-hooks
setup-git-hooks:
	$(call print_title,Installing and configuring git hooks)
	@find .githooks -type f -exec cp {} .git/hooks \;
	@chmod +x .git/hooks/*
	@echo "$(GREEN)$(BOLD)[ok]$(NC) All hooks installed and updated$(GREEN) ✔️$(NC)"

.PHONY: check-hooks
check-hooks:
	$(call print_title,Verifying git hooks installation status)
	@err=0; \
	for hook_dir in .githooks/*; do \
		if [ -d "$$hook_dir" ]; then \
			for FILE in "$$hook_dir"/*; do \
				if [ -f "$$FILE" ]; then \
					f=$$(basename -- $$hook_dir)/$$(basename -- $$FILE); \
					hook_name=$$(basename -- $$FILE); \
					FILE2=.git/hooks/$$hook_name; \
					if [ -f "$$FILE2" ]; then \
						if cmp -s "$$FILE" "$$FILE2"; then \
							echo "$(GREEN)$(BOLD)[ok]$(NC) Hook file $$f installed and updated$(GREEN) ✔️$(NC)"; \
						else \
							echo "$(RED)Hook file $$f installed but out-of-date [OUT-OF-DATE] ✗$(NC)"; \
							err=1; \
						fi; \
					else \
						echo "$(RED)Hook file $$f not installed [NOT INSTALLED] ✗$(NC)"; \
						err=1; \
					fi; \
				fi; \
			done; \
		fi; \
	done; \
	if [ $$err -ne 0 ]; then \
		echo -e "\nRun $(BOLD)make setup-git-hooks$(NC) to setup your development environment, then try again.\n"; \
		exit 1; \
	else \
		echo "$(GREEN)$(BOLD)[ok]$(NC) All hooks are properly installed$(GREEN) ✔️$(NC)"; \
	fi

.PHONY: check-envs
check-envs:
	$(call print_title,Checking git hooks and environment files for security issues)
	$(MAKE) check-hooks
	@echo "Checking for exposed secrets in environment files..."
	@if grep -rq "SECRET.*=" --include=".env" .; then \
		echo "$(RED)Warning: Secrets found in environment files. Make sure these are not committed to the repository.$(NC)"; \
		exit 1; \
	else \
		echo "$(GREEN)No exposed secrets found in environment files$(GREEN) ✔️$(NC)"; \
	fi
	@echo "$(GREEN)$(BOLD)[ok]$(NC) Environment check completed$(GREEN) ✔️$(NC)"

#-------------------------------------------------------
# Development Commands
#-------------------------------------------------------

.PHONY: tidy
tidy:
	$(call print_title,Cleaning dependencies)
	$(call check_command,go,"Install Go from https://golang.org/doc/install")
	go mod tidy
	@echo "$(GREEN)$(BOLD)[ok]$(NC) Dependencies cleaned successfully$(GREEN) ✔️$(NC)"

.PHONY: sec
sec:
	$(call print_title,Running security checks using gosec)
	@if ! command -v gosec >/dev/null 2>&1; then \
		echo "Installing gosec..."; \
		go install github.com/securego/gosec/v2/cmd/gosec@latest; \
	fi
	@if find . -name "*.go" -type f | grep -q .; then \
		echo "Running security checks on all packages..."; \
		if gosec ./...; then \
			echo "$(GREEN)$(BOLD)[ok]$(NC) Security checks completed$(GREEN) ✔️$(NC)"; \
		else \
			echo -e "\n$(BOLD)$(RED)Security issues found by gosec. Please address them before proceeding.$(NC)\n"; \
			exit 1; \
		fi; \
	else \
		echo "No Go files found, skipping security checks"; \
	fi

#-------------------------------------------------------
# Release Commands
#-------------------------------------------------------

.PHONY: goreleaser
goreleaser:
	$(call print_title,Creating release snapshot with goreleaser)
	$(call check_command,goreleaser,"go install github.com/goreleaser/goreleaser@latest")
	goreleaser release --snapshot --skip-publish --clean
	@echo "$(GREEN)$(BOLD)[ok]$(NC) Release snapshot created successfully$(GREEN) ✔️$(NC)"