# HTTP Components API Documentation

This directory contains comprehensive API documentation for the LerianStudio lib-commons HTTP components.

## 📖 **Overview**

The `commons/net/http` package provides standardized HTTP response functions that ensure consistent error handling and response formatting across all services in the LerianStudio ecosystem.

## 🚀 **Quick Start**

### Import the Package
```go
import httpCommons "github.com/LerianStudio/lib-commons/commons/net/http"
import "github.com/LerianStudio/lib-commons/commons"
```

### Basic Usage Examples

#### Success Responses
```go
// Simple OK response
return httpCommons.OK(c, map[string]string{"message": "Success"})

// Created response  
return httpCommons.Created(c, createdResource)

// No content response
return httpCommons.NoContent(c)
```

#### Error Responses
```go
// Structured error response
return httpCommons.BadRequest(c, validationErrors)

// Standard error format
return httpCommons.NotFound(c, "USR001", "User Not Found", "User with ID 123 not found")

// Business error handling
return httpCommons.InternalServerError(c, "SYS001", "Database Error", "Connection failed")
```

## 📋 **Available Functions**

### 🟢 **Success Responses (2xx)**

| Function | Status | Purpose | Body Type |
|----------|--------|---------|-----------|
| `OK(c, data)` | 200 | Standard success response | `any` |
| `Created(c, data)` | 201 | Resource created successfully | `any` |
| `Accepted(c, data)` | 202 | Request accepted for processing | `any` |
| `NoContent(c)` | 204 | Success without response body | None |
| `PartialContent(c, data)` | 206 | Partial data response | `any` |

### 🟡 **Client Error Responses (4xx)**

| Function | Status | Purpose | Parameters |
|----------|--------|---------|------------|
| `BadRequest(c, data)` | 400 | Invalid request data | `any` |
| `Unauthorized(c, code, title, message)` | 401 | Authentication required | `string, string, string` |
| `Forbidden(c, code, title, message)` | 403 | Insufficient permissions | `string, string, string` |
| `NotFound(c, code, title, message)` | 404 | Resource not found | `string, string, string` |
| `Conflict(c, code, title, message)` | 409 | Resource conflict | `string, string, string` |
| `UnprocessableEntity(c, code, title, message)` | 422 | Validation failed | `string, string, string` |

### 🔴 **Server Error Responses (5xx)**

| Function | Status | Purpose | Parameters |
|----------|--------|---------|------------|
| `InternalServerError(c, code, title, message)` | 500 | Server error | `string, string, string` |
| `NotImplemented(c, message)` | 501 | Feature not implemented | `string` |

### ⚙️ **Generic Responses**

| Function | Purpose | Parameters |
|----------|---------|------------|
| `JSONResponse(c, status, data)` | Custom status with JSON body | `int, any` |
| `JSONResponseError(c, err)` | Error response using `commons.Response` | `commons.Response` |

## 🏗️ **Response Schema**

### Standard Error Response (`commons.Response`)

All structured error responses use the `commons.Response` schema:

```go
type Response struct {
    EntityType string `json:"entityType,omitempty"` // Related entity type
    Title      string `json:"title,omitempty"`      // Human-readable title
    Message    string `json:"message,omitempty"`    // Detailed message
    Code       string `json:"code,omitempty"`       // Machine-readable code
    Err        error  `json:"err,omitempty"`        // Internal error (optional)
}
```

### Example Response
```json
{
  "code": "USR001",
  "title": "User Not Found",
  "message": "User with ID 123 does not exist in the system",
  "entityType": "User"
}
```

## 🔄 **Business Error Mapping**

The library provides automatic mapping from business domain errors to appropriate HTTP responses:

```go
// Business errors are automatically mapped
err := constant.ErrInsufficientFunds
mappedError := commons.ValidateBusinessError(err, "Transaction")

// Results in standardized response:
{
  "code": "FUND001",
  "title": "Insufficient Funds Response", 
  "message": "The transaction could not be completed due to insufficient funds...",
  "entityType": "Transaction"
}
```

### Supported Business Errors

| Business Error | HTTP Status | Code | Title |
|----------------|-------------|------|-------|
| `ErrInsufficientFunds` | 400 | FUND001 | Insufficient Funds Response |
| `ErrAccountIneligibility` | 400 | ACC001 | Account Ineligibility Response |
| `ErrAssetCodeNotFound` | 404 | ASSET001 | Asset Code Not Found |
| `ErrAccountStatusTransactionRestriction` | 400 | ACC002 | Account Status Transaction Restriction |
| `ErrOverFlowInt64` | 400 | MATH001 | Overflow Error |

## 📊 **Best Practices**

### 1. **Use Appropriate Response Functions**
```go
// ✅ Good: Use specific function for the use case
return httpCommons.NotFound(c, "USR001", "User Not Found", "User with ID 123 not found")

// ❌ Avoid: Generic response for specific cases  
return httpCommons.JSONResponse(c, 404, map[string]string{"error": "not found"})
```

### 2. **Consistent Error Codes**
```go
// ✅ Good: Use meaningful, consistent codes
return httpCommons.BadRequest(c, "VAL001", "Validation Error", "Email format is invalid")

// ❌ Avoid: Generic or meaningless codes
return httpCommons.BadRequest(c, "ERROR", "Error", "Something went wrong")
```

### 3. **Informative Messages**
```go
// ✅ Good: Clear, actionable message
return httpCommons.Unauthorized(c, "AUTH001", "Token Expired", "Your session has expired. Please log in again.")

// ❌ Avoid: Vague or unhelpful message
return httpCommons.Unauthorized(c, "AUTH001", "Error", "Unauthorized")
```

### 4. **Success Response Data Structure**
```go
// ✅ Good: Structured, consistent data
responseData := map[string]interface{}{
    "user": user,
    "meta": map[string]interface{}{
        "request_id": requestID,
        "timestamp": time.Now(),
    },
}
return httpCommons.OK(c, responseData)
```

## 🧪 **Testing**

### Contract Tests
The library includes comprehensive contract tests that validate:
- Response function signatures
- Status code correctness  
- Response body structure
- Error message consistency

```bash
# Run contract tests
make test-contracts

# Test specific HTTP contracts
go test -v ./contracts/http_response_test.go
```

### Example Test Usage
```go
func TestUserAPI(t *testing.T) {
    app := fiber.New()
    
    app.Get("/users/:id", func(c *fiber.Ctx) error {
        userID := c.Params("id")
        user, err := getUserByID(userID)
        if err != nil {
            return httpCommons.NotFound(c, "USR001", "User Not Found", 
                fmt.Sprintf("User with ID %s not found", userID))
        }
        return httpCommons.OK(c, user)
    })
    
    // Test success case
    req := httptest.NewRequest("GET", "/users/123", nil)
    resp, _ := app.Test(req)
    assert.Equal(t, 200, resp.StatusCode)
    
    // Test error case  
    req = httptest.NewRequest("GET", "/users/999", nil)
    resp, _ = app.Test(req)
    assert.Equal(t, 404, resp.StatusCode)
}
```

## 📚 **Documentation Files**

| File | Purpose |
|------|---------|
| `openapi.yaml` | Complete OpenAPI 3.0 specification |
| `README.md` | This documentation file |
| `examples/` | Usage examples and code samples |

## 🔗 **Related Documentation**

- [Contract Testing Guide](../contracts/README.md)
- [Error Handling Documentation](../error-handling.md)
- [Validation Framework](../validation.md)
- [Main Library Documentation](../../README.md)

## 🛠️ **Tools and Integration**

### Generate Documentation
```bash
# Generate API docs from OpenAPI spec
swagger-codegen generate -i docs/api/openapi.yaml -l html2 -o docs/api/generated/

# Validate OpenAPI specification
swagger-codegen validate -i docs/api/openapi.yaml
```

### IDE Integration
Most IDEs support OpenAPI specifications for:
- Auto-completion
- Request validation
- Response schema validation
- Mock server generation

### Testing Tools
- **Postman**: Import `openapi.yaml` for API testing
- **Insomnia**: Direct OpenAPI import support
- **curl**: Generated examples for command-line testing

---

## 🤝 **Contributing**

When adding new HTTP response functions:

1. **Add function to `commons/net/http/response.go`**
2. **Update contract tests in `contracts/http_response_test.go`**
3. **Update OpenAPI specification in `docs/api/openapi.yaml`**
4. **Add usage examples and documentation**
5. **Run contract tests to ensure compatibility**

## 📝 **License**

This documentation is part of the LerianStudio lib-commons library and is licensed under the MIT License.