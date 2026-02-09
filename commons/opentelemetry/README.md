# OpenTelemetry Package

This package provides OpenTelemetry integration for the LerianStudio commons library, including advanced struct obfuscation capabilities for secure telemetry data.

## Features

- **OpenTelemetry Integration**: Complete setup and configuration for tracing, metrics, and logging
- **Struct Obfuscation**: Advanced field obfuscation for sensitive data in telemetry spans
- **Flexible Configuration**: Support for custom obfuscation rules and business logic
- **Backward Compatibility**: Maintains existing API while adding new security features

## Quick Start

### Basic Usage (Without Obfuscation)

```go
import (
    "github.com/LerianStudio/lib-commons/v2/commons/opentelemetry"
    "go.opentelemetry.io/otel"
)

// Create a span and add struct data
tracer := otel.Tracer("my-service")
_, span := tracer.Start(ctx, "operation")
defer span.End()

// Add struct data to span (original behavior)
err := opentelemetry.SetSpanAttributesFromStruct(&span, "user_data", userStruct)
```

### With Default Obfuscation

```go
// Create default obfuscator (covers common sensitive fields)
obfuscator := opentelemetry.NewDefaultObfuscator()

// Add obfuscated struct data to span
err := opentelemetry.SetSpanAttributesFromStructWithObfuscation(
    &span, "user_data", userStruct, obfuscator)
```

### With Custom Obfuscation

```go
// Define custom sensitive fields
customFields := []string{"email", "phone", "address"}
customObfuscator := opentelemetry.NewCustomObfuscator(customFields)

// Apply custom obfuscation
err := opentelemetry.SetSpanAttributesFromStructWithObfuscation(
    &span, "user_data", userStruct, customObfuscator)
```

## Struct Obfuscation Examples

### Example Data Structure

```go
type UserLoginRequest struct {
    Username    string          `json:"username"`
    Password    string          `json:"password"`
    Email       string          `json:"email"`
    RememberMe  bool            `json:"rememberMe"`
    DeviceInfo  DeviceInfo      `json:"deviceInfo"`
    Credentials AuthCredentials `json:"credentials"`
    Metadata    map[string]any  `json:"metadata"`
}

type DeviceInfo struct {
    UserAgent    string `json:"userAgent"`
    IPAddress    string `json:"ipAddress"`
    DeviceID     string `json:"deviceId"`
    SessionToken string `json:"token"` // Will be obfuscated
}

type AuthCredentials struct {
    APIKey       string `json:"apikey"`        // Will be obfuscated
    RefreshToken string `json:"refresh_token"` // Will be obfuscated
    ClientSecret string `json:"secret"`        // Will be obfuscated
}
```

### Example 1: Default Obfuscation

```go
loginRequest := UserLoginRequest{
    Username:   "john.doe",
    Password:   "super_secret_password_123",
    Email:      "john.doe@example.com",
    RememberMe: true,
    DeviceInfo: DeviceInfo{
        UserAgent:    "Mozilla/5.0...",
        IPAddress:    "192.168.1.100",
        DeviceID:     "device_12345",
        SessionToken: "session_token_abc123xyz",
    },
    Credentials: AuthCredentials{
        APIKey:       "api_key_secret_789",
        RefreshToken: "refresh_token_xyz456",
        ClientSecret: "client_secret_ultra_secure",
    },
    Metadata: map[string]any{
        "theme":       "dark",
        "language":    "en-US",
        "private_key": "private_key_should_be_hidden",
        "public_info": "this is safe to show",
    },
}

// Apply default obfuscation
defaultObfuscator := opentelemetry.NewDefaultObfuscator()
err := opentelemetry.SetSpanAttributesFromStructWithObfuscation(
    &span, "login_request", loginRequest, defaultObfuscator)

// Result: password, token, secret, apikey, private_key fields become "***"
```

### Example 2: Custom Field Selection

```go
// Only obfuscate specific fields
customFields := []string{"username", "email", "deviceId", "ipAddress"}
customObfuscator := opentelemetry.NewCustomObfuscator(customFields)

err := opentelemetry.SetSpanAttributesFromStructWithObfuscation(
    &span, "login_request", loginRequest, customObfuscator)

// Result: Only username, email, deviceId, ipAddress become "***"
```

### Example 3: Custom Business Logic Obfuscator

```go
// Implement custom obfuscation logic
type BusinessLogicObfuscator struct {
    companyPolicy map[string]bool
}

func (b *BusinessLogicObfuscator) ShouldObfuscate(fieldName string) bool {
    return b.companyPolicy[strings.ToLower(fieldName)]
}

func (b *BusinessLogicObfuscator) GetObfuscatedValue() string {
    return "[COMPANY_POLICY_REDACTED]"
}

// Use custom obfuscator
businessObfuscator := &BusinessLogicObfuscator{
    companyPolicy: map[string]bool{
        "email":      true,
        "ipaddress":  true,
        "deviceinfo": true, // Obfuscates entire nested object
    },
}

err := opentelemetry.SetSpanAttributesFromStructWithObfuscation(
    &span, "login_request", loginRequest, businessObfuscator)
```

### Example 4: Standalone Obfuscation Utility

```go
// Use obfuscation without OpenTelemetry spans
obfuscator := opentelemetry.NewDefaultObfuscator()
obfuscatedData, err := opentelemetry.ObfuscateStruct(loginRequest, obfuscator)
if err != nil {
    log.Printf("Obfuscation failed: %v", err)
}

// obfuscatedData now contains the struct with sensitive fields replaced
```

## Default Sensitive Fields

The `NewDefaultObfuscator()` uses a shared list of sensitive field names from the `commons/security` package, ensuring consistent obfuscation behavior across HTTP logging, OpenTelemetry spans, and other components.

The following common sensitive field names are automatically obfuscated (case-insensitive):

### Authentication & Security
- `password`
- `token`
- `secret`
- `key`
- `authorization`
- `auth`
- `credential`
- `credentials`
- `apikey`
- `api_key`
- `access_token`
- `refresh_token`
- `private_key`
- `privatekey`

> **Note**: This list is shared with HTTP logging middleware and other components via `security.DefaultSensitiveFields` to ensure consistent behavior across the entire commons library.

## API Reference

### Core Functions

#### `SetSpanAttributesFromStruct(span *trace.Span, key string, valueStruct any) error`
Original function for backward compatibility. Adds struct data to span without obfuscation.

#### `SetSpanAttributesFromStructWithObfuscation(span *trace.Span, key string, valueStruct any, obfuscator FieldObfuscator) error`
Enhanced function that applies obfuscation before adding struct data to span. If `obfuscator` is `nil`, behaves like the original function.

#### `ObfuscateStruct(valueStruct any, obfuscator FieldObfuscator) (any, error)`
Standalone utility function that obfuscates a struct and returns the result. Can be used independently of OpenTelemetry spans.

### Obfuscator Constructors

#### `NewDefaultObfuscator() *DefaultObfuscator`
Creates an obfuscator with predefined common sensitive field names.

#### `NewCustomObfuscator(sensitiveFields []string) *CustomObfuscator`
Creates an obfuscator with custom sensitive field names. Field matching is case-insensitive and uses exact matching (not word-boundary matching like `DefaultObfuscator`).

### Interface

#### `FieldObfuscator`
```go
type FieldObfuscator interface {
    // ShouldObfuscate returns true if the given field name should be obfuscated
    ShouldObfuscate(fieldName string) bool
    // GetObfuscatedValue returns the value to use for obfuscated fields
    GetObfuscatedValue() string
}
```

### Constants

#### `ObfuscatedValue = "***"`
Default value used to replace sensitive fields. Can be referenced for consistency.

## Advanced Features

### Recursive Obfuscation
The obfuscation system works recursively on:
- **Nested structs**: Processes all nested object fields
- **Arrays and slices**: Processes each element in collections
- **Maps**: Processes all key-value pairs

### Case-Insensitive Matching
Field name matching is case-insensitive for flexibility:
```go
// All these variations will be obfuscated if "password" is in the sensitive list
"password", "Password", "PASSWORD", "PaSsWoRd"
```

### Performance Considerations
- **Efficient processing**: Uses pre-allocated maps for field lookups
- **Memory conscious**: Minimal allocations during recursive processing
- **JSON conversion**: Leverages Go's efficient JSON marshaling/unmarshaling

## Best Practices

### Security
- **Always use obfuscation** for production telemetry data
- **Review sensitive field lists** regularly to ensure comprehensive coverage
- **Implement custom obfuscators** for business-specific sensitive data
- **Test obfuscation rules** to verify sensitive data is properly hidden

### Performance
- **Reuse obfuscator instances** instead of creating new ones for each call
- **Use appropriate obfuscation level** - don't over-obfuscate if not needed
- **Consider caching** obfuscated results for frequently used structs

### Maintainability
- **Use `NewDefaultObfuscator()`** for most common use cases
- **Document custom obfuscation rules** in your business logic
- **Centralize obfuscation policies** for consistency across services
- **Test obfuscation behavior** in your unit tests

### Migration
- **Backward compatibility**: Existing code using `SetSpanAttributesFromStruct()` continues to work
- **Gradual adoption**: Add obfuscation incrementally to existing telemetry code
- **Monitoring**: Verify obfuscated telemetry data meets security requirements

## Error Handling

The obfuscation functions return errors in these cases:
- **Invalid JSON**: When the input struct cannot be marshaled to JSON
- **Malformed data**: When JSON unmarshaling fails during processing

```go
err := opentelemetry.SetSpanAttributesFromStructWithObfuscation(
    &span, "data", invalidStruct, obfuscator)
if err != nil {
    log.Printf("Obfuscation failed: %v", err)
    // Handle error appropriately
}
```

## Testing

The package includes comprehensive tests covering:
- **Default obfuscator behavior**
- **Custom obfuscator functionality**
- **Recursive obfuscation of nested structures**
- **Error handling for invalid data**
- **Integration with OpenTelemetry spans**
- **Custom obfuscator interface implementations**

Run tests with:
```bash
go test ./commons/opentelemetry -v
```

## Examples

For complete working examples, see:
- `obfuscation_test.go` - Comprehensive test cases
- `examples/opentelemetry_obfuscation_example.go` - Runnable example application

## Contributing

When adding new features:
1. **Follow the interface pattern** for extensibility
2. **Add comprehensive tests** for new functionality
3. **Update documentation** with examples
4. **Maintain backward compatibility** with existing APIs
5. **Follow Go best practices** and the project's coding standards
