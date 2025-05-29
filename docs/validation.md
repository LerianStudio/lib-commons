# Validation Package

The validation package provides comprehensive struct validation with built-in validators and support for custom validation rules.

## Table of Contents

- [Overview](#overview)
- [Basic Usage](#basic-usage)
- [Built-in Validators](#built-in-validators)
- [Custom Validators](#custom-validators)
- [Error Handling](#error-handling)
- [Best Practices](#best-practices)

## Overview

The validation package uses struct tags to define validation rules and provides a simple API for validating structs.

### Features

- Struct tag-based validation
- Built-in validators for common use cases
- Custom validator registration
- Detailed error messages
- Field-level and cross-field validation
- Nested struct validation

## Basic Usage

### Simple Struct Validation

```go
package main

import (
    "fmt"
    "github.com/yourusername/commons-go/commons/validation"
)

type User struct {
    Name     string `validate:"required,minlength=3,maxlength=50"`
    Email    string `validate:"required,email"`
    Age      int    `validate:"inrange=18:120"`
    Website  string `validate:"url"`
    ID       string `validate:"uuid"`
}

func main() {
    user := User{
        Name:    "John Doe",
        Email:   "john@example.com",
        Age:     25,
        Website: "https://example.com",
        ID:      "550e8400-e29b-41d4-a716-446655440000",
    }

    validator := validation.New()
    if err := validator.ValidateStruct(user); err != nil {
        fmt.Printf("Validation failed: %v\n", err)
        return
    }
    fmt.Println("Validation successful!")
}
```

### Handling Validation Errors

```go
err := validator.ValidateStruct(user)
if err != nil {
    // Type assert to get detailed errors
    if validationErr, ok := err.(*validation.ValidationError); ok {
        for field, fieldErr := range validationErr.Errors {
            fmt.Printf("Field %s: %s\n", field, fieldErr.Error())
        }
    }
}
```

## Built-in Validators

### Required

Ensures the field is not empty.

```go
type Product struct {
    Name string `validate:"required"`
}
```

### Length Validators

#### MinLength

```go
type Account struct {
    Username string `validate:"minlength=3"`
}
```

#### MaxLength

```go
type Comment struct {
    Text string `validate:"maxlength=500"`
}
```

### Format Validators

#### Email

```go
type Contact struct {
    Email string `validate:"email"`
}
```

#### URL

```go
type Website struct {
    URL string `validate:"url"`
}
```

#### UUID

```go
type Entity struct {
    ID string `validate:"uuid"`
}
```

### Range Validator

For numeric values:

```go
type Person struct {
    Age    int     `validate:"inrange=0:150"`
    Height float64 `validate:"inrange=0.5:3.0"`
}
```

### Pattern Matching

Using regular expressions:

```go
type Account struct {
    // Only alphanumeric characters
    Username string `validate:"matches=^[a-zA-Z0-9]+$"`
    // US phone number
    Phone string `validate:"matches=^\\+1[0-9]{10}$"`
}
```

### OneOf Validator

For enum-like validation:

```go
type Order struct {
    Status   string `validate:"oneof=pending:processing:completed:cancelled"`
    Priority string `validate:"oneof=low:medium:high"`
}
```

## Custom Validators

### Registering Custom Validators

```go
// Define a custom validator function
func validatePassword(value interface{}, params []string) error {
    password, ok := value.(string)
    if !ok {
        return fmt.Errorf("password must be a string")
    }
    
    if len(password) < 8 {
        return fmt.Errorf("password must be at least 8 characters")
    }
    
    hasUpper := false
    hasLower := false
    hasDigit := false
    
    for _, char := range password {
        switch {
        case unicode.IsUpper(char):
            hasUpper = true
        case unicode.IsLower(char):
            hasLower = true
        case unicode.IsDigit(char):
            hasDigit = true
        }
    }
    
    if !hasUpper || !hasLower || !hasDigit {
        return fmt.Errorf("password must contain uppercase, lowercase, and digit")
    }
    
    return nil
}

// Register the validator
validator := validation.New()
validator.RegisterValidator("password", validatePassword)

// Use in struct
type User struct {
    Password string `validate:"password"`
}
```

### Cross-Field Validation

```go
func validatePasswordMatch(value interface{}, params []string) error {
    // Custom validators receive the entire struct
    user, ok := value.(User)
    if !ok {
        return fmt.Errorf("invalid type")
    }
    
    if user.Password != user.ConfirmPassword {
        return fmt.Errorf("passwords do not match")
    }
    
    return nil
}

type User struct {
    Password        string `validate:"required,password"`
    ConfirmPassword string `validate:"required"`
    _               struct{} `validate:"passwordmatch"`
}
```

## Error Handling

### ValidationError Structure

```go
type ValidationError struct {
    Errors map[string]error
}

// Check specific field errors
err := validator.ValidateStruct(user)
if valErr, ok := err.(*validation.ValidationError); ok {
    if emailErr, exists := valErr.Errors["Email"]; exists {
        // Handle email validation error
        fmt.Printf("Email error: %v\n", emailErr)
    }
}
```

### Custom Error Messages

```go
func validateAge(value interface{}, params []string) error {
    age, ok := value.(int)
    if !ok {
        return fmt.Errorf("age must be a number")
    }
    
    if age < 18 {
        return fmt.Errorf("must be 18 or older to register")
    }
    
    if age > 120 {
        return fmt.Errorf("please enter a valid age")
    }
    
    return nil
}
```

## Best Practices

### 1. Validate Early

```go
func CreateUser(input CreateUserInput) (*User, error) {
    // Validate input immediately
    if err := validator.ValidateStruct(input); err != nil {
        return nil, fmt.Errorf("invalid input: %w", err)
    }
    
    // Proceed with business logic
    user := &User{
        Name:  input.Name,
        Email: input.Email,
    }
    
    return user, nil
}
```

### 2. Use Specific Validators

```go
type Config struct {
    // Be specific about constraints
    Port     int    `validate:"required,inrange=1:65535"`
    Timeout  int    `validate:"required,inrange=1:300"`
    LogLevel string `validate:"required,oneof=debug:info:warn:error"`
}
```

### 3. Combine Validators

```go
type Article struct {
    // Multiple validators can be combined
    Title   string `validate:"required,minlength=10,maxlength=200"`
    Content string `validate:"required,minlength=100,maxlength=10000"`
    Slug    string `validate:"required,matches=^[a-z0-9-]+$"`
}
```

### 4. Nested Validation

```go
type Address struct {
    Street  string `validate:"required"`
    City    string `validate:"required"`
    ZipCode string `validate:"required,matches=^[0-9]{5}$"`
}

type User struct {
    Name    string  `validate:"required"`
    Email   string  `validate:"required,email"`
    Address Address `validate:"required"` // Nested struct validation
}
```

### 5. Validation Groups

```go
type User struct {
    ID       string `validate:"required,uuid" groups:"update"`
    Name     string `validate:"required,minlength=3" groups:"create,update"`
    Email    string `validate:"required,email" groups:"create"`
    Password string `validate:"required,password" groups:"create"`
}

// Validate only specific groups
func CreateUser(input User) error {
    return validator.ValidateStructWithGroups(input, []string{"create"})
}

func UpdateUser(input User) error {
    return validator.ValidateStructWithGroups(input, []string{"update"})
}
```

### 6. Performance Considerations

```go
// Reuse validator instances
var (
    userValidator = validation.New()
    once          sync.Once
)

func init() {
    once.Do(func() {
        // Register custom validators once
        userValidator.RegisterValidator("username", validateUsername)
        userValidator.RegisterValidator("password", validatePassword)
    })
}

// Use the shared validator
func ValidateUser(user User) error {
    return userValidator.ValidateStruct(user)
}
```

### 7. Testing Validation

```go
func TestUserValidation(t *testing.T) {
    tests := []struct {
        name    string
        user    User
        wantErr bool
        errField string
    }{
        {
            name: "valid user",
            user: User{
                Name:  "John Doe",
                Email: "john@example.com",
                Age:   25,
            },
            wantErr: false,
        },
        {
            name: "missing email",
            user: User{
                Name: "John Doe",
                Age:  25,
            },
            wantErr:  true,
            errField: "Email",
        },
        {
            name: "invalid age",
            user: User{
                Name:  "John Doe",
                Email: "john@example.com",
                Age:   -1,
            },
            wantErr:  true,
            errField: "Age",
        },
    }
    
    validator := validation.New()
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := validator.ValidateStruct(tt.user)
            if (err != nil) != tt.wantErr {
                t.Errorf("ValidateStruct() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            
            if tt.wantErr && tt.errField != "" {
                valErr, ok := err.(*validation.ValidationError)
                if !ok {
                    t.Errorf("Expected ValidationError type")
                    return
                }
                
                if _, exists := valErr.Errors[tt.errField]; !exists {
                    t.Errorf("Expected error for field %s", tt.errField)
                }
            }
        })
    }
}
```

### 8. API Integration

```go
func UserHandler(w http.ResponseWriter, r *http.Request) {
    var input CreateUserInput
    if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
        http.Error(w, "Invalid JSON", http.StatusBadRequest)
        return
    }
    
    if err := validator.ValidateStruct(input); err != nil {
        valErr, ok := err.(*validation.ValidationError)
        if ok {
            // Return detailed validation errors
            response := map[string]interface{}{
                "error":  "Validation failed",
                "fields": valErr.Errors,
            }
            w.Header().Set("Content-Type", "application/json")
            w.WriteHeader(http.StatusBadRequest)
            json.NewEncoder(w).Encode(response)
            return
        }
        
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    
    // Process valid input
    // ...
}
```

## Examples

### Complete Example: User Registration

```go
package main

import (
    "encoding/json"
    "fmt"
    "net/http"
    "regexp"
    "unicode"
    
    "github.com/yourusername/commons-go/commons/validation"
)

type RegistrationInput struct {
    Username        string `json:"username" validate:"required,minlength=3,maxlength=20,username"`
    Email           string `json:"email" validate:"required,email"`
    Password        string `json:"password" validate:"required,password"`
    ConfirmPassword string `json:"confirm_password" validate:"required"`
    Age             int    `json:"age" validate:"required,inrange=13:120"`
    AgreeToTerms    bool   `json:"agree_to_terms" validate:"required"`
}

var (
    validator     = validation.New()
    usernameRegex = regexp.MustCompile("^[a-zA-Z0-9_]+$")
)

func init() {
    // Register custom validators
    validator.RegisterValidator("username", validateUsername)
    validator.RegisterValidator("password", validateStrongPassword)
}

func validateUsername(value interface{}, params []string) error {
    username, ok := value.(string)
    if !ok {
        return fmt.Errorf("username must be a string")
    }
    
    if !usernameRegex.MatchString(username) {
        return fmt.Errorf("username can only contain letters, numbers, and underscores")
    }
    
    // Check for reserved usernames
    reserved := []string{"admin", "root", "system"}
    for _, r := range reserved {
        if username == r {
            return fmt.Errorf("username '%s' is reserved", username)
        }
    }
    
    return nil
}

func validateStrongPassword(value interface{}, params []string) error {
    password, ok := value.(string)
    if !ok {
        return fmt.Errorf("password must be a string")
    }
    
    if len(password) < 8 {
        return fmt.Errorf("password must be at least 8 characters long")
    }
    
    var hasUpper, hasLower, hasDigit, hasSpecial bool
    
    for _, char := range password {
        switch {
        case unicode.IsUpper(char):
            hasUpper = true
        case unicode.IsLower(char):
            hasLower = true
        case unicode.IsDigit(char):
            hasDigit = true
        case unicode.IsPunct(char) || unicode.IsSymbol(char):
            hasSpecial = true
        }
    }
    
    if !hasUpper {
        return fmt.Errorf("password must contain at least one uppercase letter")
    }
    if !hasLower {
        return fmt.Errorf("password must contain at least one lowercase letter")
    }
    if !hasDigit {
        return fmt.Errorf("password must contain at least one digit")
    }
    if !hasSpecial {
        return fmt.Errorf("password must contain at least one special character")
    }
    
    return nil
}

func RegistrationHandler(w http.ResponseWriter, r *http.Request) {
    var input RegistrationInput
    
    // Decode JSON
    if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
        respondWithError(w, http.StatusBadRequest, "Invalid JSON")
        return
    }
    
    // Validate input
    if err := validator.ValidateStruct(input); err != nil {
        if valErr, ok := err.(*validation.ValidationError); ok {
            respondWithValidationError(w, valErr)
            return
        }
        respondWithError(w, http.StatusBadRequest, err.Error())
        return
    }
    
    // Check password confirmation
    if input.Password != input.ConfirmPassword {
        respondWithError(w, http.StatusBadRequest, "Passwords do not match")
        return
    }
    
    // Check terms agreement
    if !input.AgreeToTerms {
        respondWithError(w, http.StatusBadRequest, "You must agree to the terms of service")
        return
    }
    
    // Process registration...
    respondWithSuccess(w, map[string]string{
        "message": "Registration successful",
        "username": input.Username,
    })
}

func respondWithError(w http.ResponseWriter, status int, message string) {
    response := map[string]string{"error": message}
    respondWithJSON(w, status, response)
}

func respondWithValidationError(w http.ResponseWriter, valErr *validation.ValidationError) {
    errors := make(map[string]string)
    for field, err := range valErr.Errors {
        errors[field] = err.Error()
    }
    
    response := map[string]interface{}{
        "error":  "Validation failed",
        "fields": errors,
    }
    respondWithJSON(w, http.StatusBadRequest, response)
}

func respondWithSuccess(w http.ResponseWriter, data interface{}) {
    respondWithJSON(w, http.StatusOK, data)
}

func respondWithJSON(w http.ResponseWriter, status int, data interface{}) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    json.NewEncoder(w).Encode(data)
}
```