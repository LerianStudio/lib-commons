#!/bin/bash

# Define color codes for better readability
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Reports directory
TEST_REPORTS_DIR="${TEST_REPORTS_DIR:-./reports}"
mkdir -p "$TEST_REPORTS_DIR"

echo "${BLUE}Generating test coverage report...${NC}"

# Check if coverage_ignore.txt exists
if [ ! -f "./scripts/coverage_ignore.txt" ]; then
  echo "${YELLOW}Warning: ./scripts/coverage_ignore.txt not found. Using all packages.${NC}"
  PACKAGES=$(go list ./... | grep -v '/vendor/')
else
  # Get the list of packages to test, excluding those in the ignore list
  PACKAGES=$(go list ./... | grep -v '/vendor/' | grep -v -f ./scripts/coverage_ignore.txt)
fi

if [ -z "$PACKAGES" ]; then
  echo "${RED}Error: No packages found to test${NC}"
  exit 1
fi

echo "${BLUE}Running tests on packages:${NC}"
echo "$PACKAGES"

# Run the tests and generate coverage profile
echo ""
go test -cover $PACKAGES -coverprofile="$TEST_REPORTS_DIR/coverage.out" -covermode=atomic

# Check if tests passed
if [ $? -ne 0 ]; then
  echo "${RED}Tests failed${NC}"
  exit 1
fi

# Print coverage summary
printf "\n${GREEN}Coverage Summary:${NC}\n"
go tool cover -func="$TEST_REPORTS_DIR/coverage.out"

# Print note about coverage exclusions if ignore file exists
if [ -f "./scripts/coverage_ignore.txt" ]; then
  printf "\n${YELLOW}NOTE ON COVERAGE:${NC}\n"
  echo "Some packages are excluded from coverage metrics (see ./scripts/coverage_ignore.txt)"
  echo "These exclusions may include:"
  echo "- Test utilities and test packages"
  echo "- Mock implementations"
  echo "- Generated code"
  echo ""
fi

echo "${GREEN}Coverage report generated successfully${NC}"
