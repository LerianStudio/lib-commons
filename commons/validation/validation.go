// Package validation provides comprehensive input validation functions and utilities.
// It includes struct validation, field validation, and custom validator registration.
package validation

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// ValidationError represents a validation error with field and message details.
// The type name intentionally matches the package name for clarity in external usage.
//
type ValidationError struct {
	Field   string
	Message string
	Value   any
}

// Error implements the error interface
func (e ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s: %s", e.Field, e.Message)
	}

	return e.Message
}

// NewValidationError creates a new validation error
func NewValidationError(message, field string) ValidationError {
	return ValidationError{
		Field:   field,
		Message: message,
	}
}

// Required validates that a value is not empty/zero
func Required(value any, fieldName string) error {
	if value == nil {
		return NewValidationError(fieldName+" is required", fieldName)
	}

	v := reflect.ValueOf(value)

	// Handle pointers
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return NewValidationError(fieldName+" is required", fieldName)
		}
		v = v.Elem()
	}

	return validateRequiredByKind(v, fieldName)
}

// validateRequiredByKind validates required constraint based on the value's kind
func validateRequiredByKind(v reflect.Value, fieldName string) error {
	switch v.Kind() {
	case reflect.String:
		return validateRequiredString(v, fieldName)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return validateRequiredInt(v, fieldName)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return validateRequiredUint(v, fieldName)
	case reflect.Float32, reflect.Float64:
		return validateRequiredFloat(v, fieldName)
	case reflect.Slice, reflect.Array, reflect.Map:
		return validateRequiredCollection(v, fieldName)
	case reflect.Bool:
		// bool is always valid for required check
		return nil
	default:
		return validateRequiredDefault(v, fieldName)
	}
}

// validateRequiredString validates required constraint for string values
func validateRequiredString(v reflect.Value, fieldName string) error {
	if strings.TrimSpace(v.String()) == "" {
		return NewValidationError(fieldName+" is required", fieldName)
	}
	return nil
}

// validateRequiredInt validates required constraint for signed integer values
func validateRequiredInt(v reflect.Value, fieldName string) error {
	if v.Int() == 0 {
		return NewValidationError(fieldName+" is required", fieldName)
	}
	return nil
}

// validateRequiredUint validates required constraint for unsigned integer values
func validateRequiredUint(v reflect.Value, fieldName string) error {
	if v.Uint() == 0 {
		return NewValidationError(fieldName+" is required", fieldName)
	}
	return nil
}

// validateRequiredFloat validates required constraint for floating point values
func validateRequiredFloat(v reflect.Value, fieldName string) error {
	if v.Float() == 0 {
		return NewValidationError(fieldName+" is required", fieldName)
	}
	return nil
}

// validateRequiredCollection validates required constraint for slices, arrays, and maps
func validateRequiredCollection(v reflect.Value, fieldName string) error {
	if v.Len() == 0 {
		return NewValidationError(fieldName+" is required", fieldName)
	}
	return nil
}

// validateRequiredDefault validates required constraint for other types using zero value check
func validateRequiredDefault(v reflect.Value, fieldName string) error {
	if v.IsZero() {
		return NewValidationError(fieldName+" is required", fieldName)
	}
	return nil
}

// MinLength validates minimum string length
func MinLength(value string, minLength int, fieldName string) error {
	if len(value) < minLength {
		return NewValidationError(
			fmt.Sprintf("%s must have minimum length of %d", fieldName, minLength),
			fieldName,
		)
	}

	return nil
}

// MaxLength validates maximum string length
func MaxLength(value string, maxLength int, fieldName string) error {
	if len(value) > maxLength {
		return NewValidationError(
			fmt.Sprintf("%s must have maximum length of %d", fieldName, maxLength),
			fieldName,
		)
	}

	return nil
}

// Email validates email format using a standard regex pattern
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// Email validates that the provided email string matches a valid email format
func Email(email string, fieldName string) error {
	if !emailRegex.MatchString(email) {
		return NewValidationError("invalid email format", fieldName)
	}

	return nil
}

// URL validates URL format
func URL(urlStr string, fieldName string) error {
	if urlStr == "" {
		return NewValidationError("URL is required", fieldName)
	}

	u, err := url.Parse(urlStr)
	if err != nil {
		return NewValidationError("invalid URL format", fieldName)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return NewValidationError("URL must use http or https scheme", fieldName)
	}

	if u.Host == "" {
		return NewValidationError("URL must have a host", fieldName)
	}

	return nil
}

// UUID validates UUID format
func UUID(uuidStr string, fieldName string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return NewValidationError("invalid UUID format", fieldName)
	}

	return nil
}

// InRange validates that a number is within a range
func InRange(value, minVal, maxVal int64, fieldName string) error {
	if value < minVal || value > maxVal {
		return NewValidationError(
			fmt.Sprintf("%s must be in range [%d, %d]", fieldName, minVal, maxVal),
			fieldName,
		)
	}

	return nil
}

// Matches validates that a string matches a regex pattern
func Matches(value, pattern, fieldName string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return NewValidationError(
			"invalid pattern for "+fieldName,
			fieldName,
		)
	}

	if !re.MatchString(value) {
		return NewValidationError(
			fieldName+" does not match required pattern",
			fieldName,
		)
	}

	return nil
}

// OneOf validates that a value is one of the allowed values
func OneOf(value string, allowed []string, fieldName string) error {
	if len(allowed) == 0 {
		return NewValidationError(
			fieldName+" must be one of: (no values defined)",
			fieldName,
		)
	}

	for _, a := range allowed {
		if value == a {
			return nil
		}
	}

	return NewValidationError(
		fieldName+" must be one of: "+fmt.Sprintf("%v", allowed),
		fieldName,
	)
}

// customValidators holds registered custom validators
var (
	customValidators   = make(map[string]func(any) error)
	customValidatorsMu sync.RWMutex
)

// RegisterCustomValidator registers a custom validator function
func RegisterCustomValidator(name string, fn func(any) error) error {
	customValidatorsMu.Lock()
	defer customValidatorsMu.Unlock()

	if _, exists := customValidators[name]; exists {
		return fmt.Errorf("validator %s already registered", name)
	}

	customValidators[name] = fn

	return nil
}

// ValidateStruct validates a struct based on validate tags
func ValidateStruct(s any) []ValidationError {
	var validationErrors []ValidationError

	v := reflect.ValueOf(s)
	t := reflect.TypeOf(s)

	// Handle pointers
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
		t = t.Elem()
	}

	if v.Kind() != reflect.Struct {
		validationErrors = append(validationErrors, NewValidationError("input must be a struct", ""))
		return validationErrors
	}

	// Iterate through struct fields
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		fieldValue := v.Field(i)

		// Get validate tag
		validateTag := field.Tag.Get("validate")
		if validateTag == "" {
			continue
		}

		// Parse validation rules
		rules := strings.Split(validateTag, ",")
		for _, rule := range rules {
			if err := validateField(fieldValue.Interface(), field.Name, rule); err != nil {
				if vErr, ok := err.(ValidationError); ok {
					validationErrors = append(validationErrors, vErr)
				} else {
					validationErrors = append(validationErrors, NewValidationError(err.Error(), field.Name))
				}
			}
		}
	}

	if len(validationErrors) == 0 {
		return nil
	}

	return validationErrors
}

// validateField validates a single field based on a rule
func validateField(value any, fieldName, rule string) error {
	parts := strings.SplitN(rule, "=", 2)
	ruleName := parts[0]

	switch ruleName {
	case "required":
		return Required(value, fieldName)
	case "email":
		return validateEmail(value, fieldName)
	case "url":
		return validateURL(value, fieldName)
	case "uuid":
		return validateUUID(value, fieldName)
	case "min":
		return validateMin(value, fieldName, parts)
	case "max":
		return validateMax(value, fieldName, parts)
	case "oneof":
		return validateOneOf(value, fieldName, parts)
	default:
		return validateCustomRule(ruleName, value)
	}
}

// validateEmail validates email format for string values
func validateEmail(value any, fieldName string) error {
	if str, ok := value.(string); ok && str != "" {
		return Email(str, fieldName)
	}
	return nil
}

// validateURL validates URL format for string values
func validateURL(value any, fieldName string) error {
	if str, ok := value.(string); ok && str != "" {
		return URL(str, fieldName)
	}
	return nil
}

// validateUUID validates UUID format for string values
func validateUUID(value any, fieldName string) error {
	if str, ok := value.(string); ok && str != "" {
		return UUID(str, fieldName)
	}
	return nil
}

// validateMin validates minimum length/value constraints
func validateMin(value any, fieldName string, parts []string) error {
	if len(parts) != 2 {
		return errors.New("min rule requires a value")
	}

	var minVal int
	if _, err := fmt.Sscanf(parts[1], "%d", &minVal); err != nil {
		return fmt.Errorf("invalid min value: %w", err)
	}

	return applyMinValidation(value, fieldName, minVal)
}

// validateMax validates maximum length/value constraints
func validateMax(value any, fieldName string, parts []string) error {
	if len(parts) != 2 {
		return errors.New("max rule requires a value")
	}

	var maxVal int
	if _, err := fmt.Sscanf(parts[1], "%d", &maxVal); err != nil {
		return fmt.Errorf("invalid max value: %w", err)
	}

	return applyMaxValidation(value, fieldName, maxVal)
}

// validateOneOf validates that value is one of allowed values
func validateOneOf(value any, fieldName string, parts []string) error {
	if len(parts) != 2 {
		return errors.New("oneof rule requires values")
	}

	allowed := strings.Split(parts[1], " ")
	if str, ok := value.(string); ok {
		return OneOf(str, allowed, fieldName)
	}
	return nil
}

// validateCustomRule checks and applies custom validators
func validateCustomRule(ruleName string, value any) error {
	customValidatorsMu.RLock()
	validator, exists := customValidators[ruleName]
	customValidatorsMu.RUnlock()

	if exists {
		return validator(value)
	}

	return fmt.Errorf("unknown validation rule: %s", ruleName)
}

// applyMinValidation applies minimum validation based on value type
func applyMinValidation(value any, fieldName string, minVal int) error {
	switch v := value.(type) {
	case string:
		return MinLength(v, minVal, fieldName)
	case int:
		return InRange(int64(v), int64(minVal), int64(^uint(0)>>1), fieldName)
	case int64:
		return InRange(v, int64(minVal), int64(^uint(0)>>1), fieldName)
	}
	return nil
}

// applyMaxValidation applies maximum validation based on value type
func applyMaxValidation(value any, fieldName string, maxVal int) error {
	switch v := value.(type) {
	case string:
		return MaxLength(v, maxVal, fieldName)
	case int:
		return InRange(int64(v), int64(-int(^uint(0)>>1)-1), int64(maxVal), fieldName)
	case int64:
		return InRange(v, int64(-int(^uint(0)>>1)-1), int64(maxVal), fieldName)
	}
	return nil
}
