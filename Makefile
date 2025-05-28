# Define the root directory of the project
LIB_COMMONS := $(shell pwd)

# Include shared color definitions and utility functions
include $(LIB_COMMONS)/commons/shell/makefile_colors.mk
include $(LIB_COMMONS)/commons/shell/makefile_utils.mk


# Core Commands
.PHONY: test
test:
	@echo "$(BLUE)Running tests on all projects...$(NC)"
	$(call check_command,go,"Install Go from https://golang.org/doc/install")
	go test -v ./... ./...

.PHONY: cover
cover:
	@echo "$(BLUE)Generating test coverage report...$(NC)"
	$(call check_command,go,"Install Go from https://golang.org/doc/install")
	@sh ./scripts/coverage.sh
	@go tool cover -html=coverage.out -o coverage.html

.PHONY: test-integration
test-integration:
	@echo "$(BLUE)Running integration tests...$(NC)"
	$(call check_command,go,"Install Go from https://golang.org/doc/install")
	@echo "$(YELLOW)Starting test containers...$(NC)"
	@go test -v -tags=integration ./... -timeout 10m

.PHONY: bench
bench:
	@echo "$(BLUE)Running benchmarks...$(NC)"
	$(call check_command,go,"Install Go from https://golang.org/doc/install")
	@go test -bench=. -benchmem ./... | tee bench.txt
	@echo "$(GREEN)$(BOLD)[ok]$(NC) Benchmarks completed$(GREEN) ✔️$(NC)"

# Code Quality Commands
.PHONY: lint
lint:
	@echo "$(BLUE)Running linting and performance checks...$(NC)"
	$(call title1,"STARTING LINT")
	$(call check_command,golangci-lint,"go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest")
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
	@echo "$(BLUE)Formatting Go code using gofmt...$(NC)"
	$(call title1,"Formatting all golang source code")
	$(call check_command,gofmt,"Install Go from https://golang.org/doc/install")
	@gofmt -w ./
	@echo "$(GREEN)$(BOLD)[ok]$(NC) All go files formatted$(GREEN) ✔️$(NC)"

# Git Hook Commands
.PHONY: setup-git-hooks
setup-git-hooks:
	@echo "$(BLUE)Installing and configuring git hooks...$(NC)"
	$(call title1,"Setting up git hooks...")
	@find .githooks -type f -exec cp {} .git/hooks \;
	@chmod +x .git/hooks/*
	@echo "$(GREEN)$(BOLD)[ok]$(NC) All hooks installed and updated$(GREEN) ✔️$(NC)"

.PHONY: check-hooks
check-hooks:
	@echo "$(BLUE)Verifying git hooks installation status...$(NC)"
	$(call title1,"Checking git hooks status...")
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
	@echo "$(BLUE)Checking git hooks and environment files for security issues...$(NC)"
	$(MAKE) check-hooks
	@echo "$(BLUE)Checking for exposed secrets in environment files...$(NC)"
	@if grep -r "SECRET.*=" --include=".env" .; then \
		echo "$(RED)Warning: Secrets found in environment files. Make sure these are not committed to the repository.$(NC)"; \
		exit 1; \
	else \
		echo "$(GREEN)No exposed secrets found in environment files$(GREEN) ✔️$(NC)"; \
	fi

# Development Commands
.PHONY: tidy
tidy:
	@echo "$(BLUE)Running go mod tidy to clean up dependencies...$(NC)"
	$(call check_command,go,"Install Go from https://golang.org/doc/install")
	go mod tidy

.PHONY: goreleaser
goreleaser:
	@echo "$(BLUE)Creating release snapshot with goreleaser...$(NC)"
	$(call check_command,goreleaser,"go install github.com/goreleaser/goreleaser@latest")
	goreleaser release --snapshot --skip-publish --rm-dist

.PHONY: sec
sec:
	@echo "$(BLUE)Running security checks using gosec...$(NC)"
	$(call check_command,gosec,"go install github.com/securego/gosec/v2/cmd/gosec@latest")
	gosec ./...