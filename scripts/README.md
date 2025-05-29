# Commons-Go Development Scripts

This directory contains shared development and build utilities for the Midaz platform.

## Shell Scripts

### `ascii.sh`
Provides ASCII art and border functions for terminal output:
- `border()` - Creates bordered text output
- `lineOk()` - Displays success messages with checkmarks
- `lineError()` - Displays error messages with X marks
- `title1()` - Creates section headers with borders and emojis

Usage:
```bash
source scripts/ascii.sh
title1 "Building Application"
lineOk "Build completed successfully"
```

### `colors.sh`
Provides terminal color definitions and formatting:
- Color variables: `red`, `green`, `blue`, `yellow`, `cyan`, `magenta`, `white`, `black`
- Formatting: `bold`, `underline`, `standout`, `normal`

Usage:
```bash
source scripts/colors.sh
echo "${green}Success!${normal}"
echo "${bold}${red}Error!${normal}"
```

## Makefile Includes

### `makefile_colors.mk`
Standardized ANSI color codes for Makefiles:
- `BLUE`, `GREEN`, `YELLOW`, `RED`, `CYAN`, `WHITE`
- `BOLD`, `NC` (no color)

Usage in Makefile:
```makefile
include $(COMMONS_GO_ROOT)/scripts/makefile_colors.mk

target:
	@echo "$(GREEN)Success!$(NC)"
	@echo "$(BOLD)$(BLUE)Building...$(NC)"
```

### `makefile_utils.mk`
Common utility functions and Docker detection for Makefiles:
- `DOCKER_CMD` - Auto-detects `docker compose` vs `docker-compose`
- `border()` - Creates bordered text in Makefiles
- `title1()`, `title2()` - Section headers with emojis

Usage in Makefile:
```makefile
include $(COMMONS_GO_ROOT)/scripts/makefile_utils.mk

build:
	$(call title1,"Building Application")
	@$(DOCKER_CMD) build .
	@echo "$(GREEN)Build completed$(NC)"
```

## Integration Example

For components that want to use these utilities:

```makefile
# In your component Makefile
COMMONS_GO_ROOT := $(shell cd ../../libs/commons-go && pwd)
include $(COMMONS_GO_ROOT)/scripts/makefile_colors.mk
include $(COMMONS_GO_ROOT)/scripts/makefile_utils.mk

build:
	$(call title1,"Building $(SERVICE_NAME)")
	@echo "$(CYAN)Starting build process...$(NC)"
	# build commands here
	@echo "$(GREEN)Build completed successfully$(NC)"
```

## Files

- `ascii.sh` - Terminal ASCII art and formatting functions
- `colors.sh` - Terminal color definitions
- `logo.txt` - ASCII logo for the platform
- `makefile_colors.mk` - ANSI color codes for Makefiles
- `makefile_utils.mk` - Common Makefile utility functions

These utilities provide consistent formatting and behavior across all Midaz components and plugins.