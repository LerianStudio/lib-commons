# Contract Testing Suite for LerianStudio lib-commons

## Overview

This directory contains contract tests that validate interface stability and prevent breaking changes across the LerianStudio lib-commons library. Contract tests ensure that:

1. **Interface signatures remain stable** - Method names, parameters, and return types
2. **Error handling is consistent** - Error types and codes don't change unexpectedly  
3. **Response formats are preserved** - JSON structures and field names remain consistent
4. **Behavioral contracts are maintained** - Expected behaviors like timeouts, retries, etc.

## Test Categories

### 🔥 **Critical Interface Contracts**
- `cache_interface_test.go` - Cache interface stability and behavior
- `logger_interface_test.go` - Logging interface consistency
- `health_interface_test.go` - Health check contracts
- `http_response_test.go` - HTTP response format stability

### 🏗️ **Infrastructure Contracts**
- `database_contracts_test.go` - Database connection behaviors
- `validation_contracts_test.go` - Validation error formats
- `observability_contracts_test.go` - Observability provider contracts

### ⚙️ **Advanced Pattern Contracts**
- `distributed_patterns_test.go` - Saga, circuit breaker, rate limiter contracts
- `retry_resilience_test.go` - Retry and resilience pattern contracts

## Running Contract Tests

```bash
# Run all contract tests
go test ./contracts/...

# Run specific contract category
go test ./contracts/cache_interface_test.go

# Run with verbose output
go test -v ./contracts/...

# Check for interface compatibility
go test -tags=contracts ./contracts/...
```

## CI/CD Integration

Contract tests are run automatically in CI/CD to catch breaking changes:

```yaml
- name: Run Contract Tests
  run: |
    go test -v ./contracts/...
    if [ $? -ne 0 ]; then
      echo "❌ Contract tests failed - potential breaking changes detected"
      exit 1
    fi
```

## Adding New Contract Tests

When adding new public interfaces:

1. **Create interface definition** in appropriate contract test file
2. **Test method signatures** - ensure they remain stable
3. **Test error scenarios** - validate error types and messages
4. **Test response formats** - verify JSON structures
5. **Test behavioral contracts** - timeouts, retries, etc.

Example contract test structure:

```go
func TestCacheInterfaceContract(t *testing.T) {
    // Test interface exists and has expected methods
    var cache commons.Cache
    cacheType := reflect.TypeOf(&cache).Elem()
    
    // Verify method signatures
    expectedMethods := map[string]string{
        "Get":    "func(context.Context, string, interface{}) error",
        "Set":    "func(context.Context, string, interface{}, time.Duration) error",
        "Delete": "func(context.Context, string) error",
    }
    
    for methodName, expectedSig := range expectedMethods {
        method, exists := cacheType.MethodByName(methodName)
        assert.True(t, exists, "Method %s should exist", methodName)
        assert.Equal(t, expectedSig, method.Type.String())
    }
}
```

## Versioning and Compatibility

- **Breaking changes** require major version bump (semver)
- **New methods** can be added in minor versions (backward compatible)
- **Parameter changes** require major version bump
- **Return type changes** require major version bump
- **Error type changes** require major version bump

## Maintenance

Contract tests should be updated when:
- ✅ Adding new public interfaces
- ✅ Deprecating old methods (add deprecation tests)
- ❌ Never when removing or changing existing contracts without version bump