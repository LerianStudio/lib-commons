# Migration Guide

This guide helps you migrate between versions of the commons-go library.

## Overview

The commons-go library follows semantic versioning. This guide covers breaking changes and migration steps for major version updates.

## Version Migration

### Migrating to v2.x from v1.x

#### Breaking Changes

1. **Observability Package Refactor**
   - Middleware functions have been refactored for better cognitive complexity
   - Some internal method signatures have changed (but public APIs remain the same)

2. **Import Path Changes**
   ```go
   // Old (v1.x)
   import "github.com/LerianStudio/lib-commons/commons"
   
   // New (v2.x) 
   import "github.com/yourusername/commons-go/commons"
   ```

3. **Logger Interface Updates**
   - Test logger implementations have been updated
   - `NewLoggerFromContext` behavior is now more consistent

#### Migration Steps

1. **Update Import Paths**
   ```bash
   # Use find and replace to update all imports
   find . -name "*.go" -exec sed -i 's|github.com/LerianStudio/lib-commons|github.com/yourusername/commons-go|g' {} +
   ```

2. **Update Dependencies**
   ```bash
   go mod edit -replace github.com/LerianStudio/lib-commons=github.com/yourusername/commons-go@v2.0.0
   go mod tidy
   ```

3. **Test Your Application**
   ```bash
   go test ./...
   ```

### Migrating from v1.0 to v1.1

#### Changes
- Added new observability middleware options
- Improved error handling in database connectors
- Enhanced logging capabilities

#### Migration Steps
No breaking changes - simple dependency update:
```bash
go get github.com/yourusername/commons-go@v1.1.0
```

## Common Migration Issues

### Issue 1: Context Logger Not Found

**Problem:**
```go
logger := commons.NewLoggerFromContext(ctx)
// logger is nil
```

**Solution:**
Ensure context has a logger attached:
```go
ctx = commons.ContextWithLogger(ctx, yourLogger)
logger := commons.NewLoggerFromContext(ctx)
```

### Issue 2: Observability Provider Not Set

**Problem:**
```go
// Middleware fails to initialize
middleware, err := observability.NewFiberMiddleware(nil)
```

**Solution:**
Always provide a valid provider:
```go
provider, err := observability.NewProvider(...)
if err != nil {
    log.Fatal(err)
}
middleware, err := observability.NewFiberMiddleware(provider)
```

### Issue 3: Test Failures After Migration

**Problem:**
Tests fail with logger-related errors.

**Solution:**
Update test setup to use new test logger:
```go
func TestFunction(t *testing.T) {
    // Create test logger that captures output
    var logOutput bytes.Buffer
    testLogger := &TestLogger{output: &logOutput}
    
    ctx := commons.ContextWithLogger(context.Background(), testLogger)
    
    // Run your test
    err := yourFunction(ctx)
    assert.NoError(t, err)
    
    // Verify log output
    assert.Contains(t, logOutput.String(), "expected log message")
}
```

## Best Practices for Migration

1. **Test Thoroughly**: Run comprehensive tests after migration
2. **Gradual Migration**: Migrate one package at a time if possible
3. **Backup Code**: Always backup your code before major migrations
4. **Review Documentation**: Check the latest documentation for new features

## Getting Help

If you encounter issues during migration:

1. Check the [troubleshooting section](#troubleshooting)
2. Review the [examples in documentation](./README.md)
3. Open an issue in the repository

## Troubleshooting

### Build Errors

**Error:** `cannot find package`

**Solution:** Ensure all import paths are updated and dependencies are fetched:
```bash
go mod download
go mod tidy
```

### Runtime Errors

**Error:** `nil pointer dereference` in observability code

**Solution:** Ensure proper initialization order:
```go
// Initialize provider first
provider, err := observability.NewProvider(...)

// Then create middleware
middleware, err := observability.NewFiberMiddleware(provider)
```

### Test Failures

**Error:** Tests expecting different log output format

**Solution:** Update test assertions to match new log format or use the provided test utilities. 