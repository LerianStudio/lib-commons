# Makefile utility functions for lib-commons
# Included by the root Makefile

# Check that a command exists, or print install instructions and exit.
# Usage: $(call check_command,<command>,<install instructions>)
define check_command
	@command -v $(1) >/dev/null 2>&1 || { \
		echo "$(RED)$(BOLD)Error:$(NC) '$(1)' is not installed. $(2)"; \
		exit 1; \
	}
endef
