package http

import (
	"errors"
	"fmt"
	"mime"
	"strings"
	"sync"

	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
)

// Validation errors.
var (
	// ErrValidationFailed is returned when struct validation fails.
	ErrValidationFailed = errors.New("validation failed")
	// ErrFieldRequired is returned when a required field is missing.
	ErrFieldRequired = errors.New("field is required")
	// ErrFieldMaxLength is returned when a field exceeds maximum length.
	ErrFieldMaxLength = errors.New("field exceeds maximum length")
	// ErrQueryParamTooLong is returned when a query parameter exceeds its maximum length.
	ErrQueryParamTooLong = errors.New("query parameter exceeds maximum length")
	// ErrFieldMinLength is returned when a field is below minimum length.
	ErrFieldMinLength = errors.New("field below minimum length")
	// ErrFieldGreaterThan is returned when a field must be greater than a value.
	ErrFieldGreaterThan = errors.New("field must be greater than constraint")
	// ErrFieldGreaterThanOrEqual is returned when a field must be greater than or equal to a value.
	ErrFieldGreaterThanOrEqual = errors.New("field must be greater than or equal to constraint")
	// ErrFieldLessThan is returned when a field must be less than a value.
	ErrFieldLessThan = errors.New("field must be less than constraint")
	// ErrFieldLessThanOrEqual is returned when a field must be less than or equal to a value.
	ErrFieldLessThanOrEqual = errors.New("field must be less than or equal to constraint")
	// ErrFieldOneOf is returned when a field must be one of allowed values.
	ErrFieldOneOf = errors.New("field must be one of allowed values")
	// ErrFieldEmail is returned when a field must be a valid email.
	ErrFieldEmail = errors.New("field must be a valid email")
	// ErrFieldURL is returned when a field must be a valid URL.
	ErrFieldURL = errors.New("field must be a valid URL")
	// ErrFieldUUID is returned when a field must be a valid UUID.
	ErrFieldUUID = errors.New("field must be a valid UUID")
	// ErrFieldPositiveAmount is returned when a field must be a positive amount.
	ErrFieldPositiveAmount = errors.New("field must be a positive amount")
	// ErrFieldNonNegativeAmount is returned when a field must be a non-negative amount.
	ErrFieldNonNegativeAmount = errors.New("field must be a non-negative amount")
	// ErrBodyParseFailed is returned when request body parsing fails.
	ErrBodyParseFailed = errors.New("failed to parse request body")
	// ErrUnsupportedContentType is returned when the Content-Type is not application/json.
	ErrUnsupportedContentType = errors.New("Content-Type must be application/json")
)

// ErrValidatorInit is returned when custom validator registration fails during initialization.
var ErrValidatorInit = errors.New("validator initialization failed")

var (
	validate     *validator.Validate
	validateOnce sync.Once
	errValidate  error
)

// initValidators creates and configures the validator with custom validation rules.
// Returns an error if any custom validator registration fails.
func initValidators() (*validator.Validate, error) {
	vld := validator.New(validator.WithRequiredStructEnabled())

	// Note: We do NOT register a custom type function for decimal.Decimal
	// because returning the same type causes an infinite loop in the validator.
	// Instead, custom validators like positive_decimal access the field directly.

	// Register custom validator for decimal amounts that must be positive
	if err := vld.RegisterValidation("positive_decimal", func(fl validator.FieldLevel) bool {
		value, ok := fl.Field().Interface().(decimal.Decimal)
		if !ok {
			return false
		}

		return value.IsPositive()
	}); err != nil {
		return nil, fmt.Errorf("%w: failed to register 'positive_decimal': %w", ErrValidatorInit, err)
	}

	// Register custom validator for string amounts that must be positive
	if err := vld.RegisterValidation("positive_amount", func(fl validator.FieldLevel) bool {
		str := fl.Field().String()
		if str == "" {
			return true // Let required tag handle empty strings
		}

		d, parseErr := decimal.NewFromString(str)
		if parseErr != nil {
			return false
		}

		return d.IsPositive()
	}); err != nil {
		return nil, fmt.Errorf("%w: failed to register 'positive_amount': %w", ErrValidatorInit, err)
	}

	// Register custom validator for string amounts that must be non-negative
	if err := vld.RegisterValidation("nonnegative_amount", func(fl validator.FieldLevel) bool {
		str := fl.Field().String()
		if str == "" {
			return true // Let required tag handle empty strings
		}

		d, parseErr := decimal.NewFromString(str)
		if parseErr != nil {
			return false
		}

		return !d.IsNegative()
	}); err != nil {
		return nil, fmt.Errorf("%w: failed to register 'nonnegative_amount': %w", ErrValidatorInit, err)
	}

	return vld, nil
}

// GetValidator returns the singleton validator instance.
// Returns the validator and any initialization error that may have occurred.
func GetValidator() (*validator.Validate, error) {
	validateOnce.Do(func() {
		validate, errValidate = initValidators()
	})

	return validate, errValidate
}

// ValidateStruct validates a struct using the go-playground/validator tags.
// Returns nil if validation passes, or the first validation error.
func ValidateStruct(payload any) error {
	vld, initErr := GetValidator()
	if initErr != nil {
		return fmt.Errorf("%w: %w", ErrValidationFailed, initErr)
	}

	if err := vld.Struct(payload); err != nil {
		var validationErrors validator.ValidationErrors
		if errors.As(err, &validationErrors) && len(validationErrors) > 0 {
			return formatValidationError(validationErrors[0])
		}

		return fmt.Errorf("%w: %w", ErrValidationFailed, err)
	}

	return nil
}

// validationErrorFormatters maps validation tags to their error formatting functions.
// Using a map-based approach reduces cyclomatic complexity compared to a large switch.
var validationErrorFormatters = map[string]func(field, param string) error{
	"required": func(field, _ string) error {
		return fmt.Errorf("%w: '%s'", ErrFieldRequired, field)
	},
	"max": func(field, param string) error {
		return fmt.Errorf("%w: '%s' must be at most %s", ErrFieldMaxLength, field, param)
	},
	"min": func(field, param string) error {
		return fmt.Errorf("%w: '%s' must be at least %s", ErrFieldMinLength, field, param)
	},
	"gt": func(field, param string) error {
		return fmt.Errorf("%w: '%s' must be greater than %s", ErrFieldGreaterThan, field, param)
	},
	"gte": func(field, param string) error {
		return fmt.Errorf("%w: '%s' must be at least %s", ErrFieldGreaterThanOrEqual, field, param)
	},
	"lt": func(field, param string) error {
		return fmt.Errorf("%w: '%s' must be less than %s", ErrFieldLessThan, field, param)
	},
	"lte": func(field, param string) error {
		return fmt.Errorf("%w: '%s' must be at most %s", ErrFieldLessThanOrEqual, field, param)
	},
	"oneof": func(field, param string) error {
		return fmt.Errorf("%w: '%s' must be one of [%s]", ErrFieldOneOf, field, param)
	},
	"email": func(field, _ string) error {
		return fmt.Errorf("%w: '%s'", ErrFieldEmail, field)
	},
	"url": func(field, _ string) error {
		return fmt.Errorf("%w: '%s'", ErrFieldURL, field)
	},
	"uuid": func(field, _ string) error {
		return fmt.Errorf("%w: '%s'", ErrFieldUUID, field)
	},
	"positive_amount": func(field, _ string) error {
		return fmt.Errorf("%w: '%s'", ErrFieldPositiveAmount, field)
	},
	"positive_decimal": func(field, _ string) error {
		return fmt.Errorf("%w: '%s'", ErrFieldPositiveAmount, field)
	},
	"nonnegative_amount": func(field, _ string) error {
		return fmt.Errorf("%w: '%s'", ErrFieldNonNegativeAmount, field)
	},
}

// formatValidationError creates a user-friendly error message from a validation error.
func formatValidationError(fe validator.FieldError) error {
	field := toSnakeCase(fe.Field())

	if formatter, ok := validationErrorFormatters[fe.Tag()]; ok {
		return formatter(field, fe.Param())
	}

	return fmt.Errorf("%w: '%s' failed '%s' check", ErrValidationFailed, field, fe.Tag())
}

// toSnakeCase converts a PascalCase or camelCase string to snake_case.
func toSnakeCase(s string) string {
	var result strings.Builder

	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteByte('_')
		}

		result.WriteRune(r)
	}

	return strings.ToLower(result.String())
}

// ParseBodyAndValidate parses the request body into the given struct and validates it.
// Returns a bad request error if parsing or validation fails.
// Rejects requests with explicit non-JSON Content-Type headers to provide clear
// error messages while preserving existing parser behavior when the header is absent.
func ParseBodyAndValidate(fiberCtx *fiber.Ctx, payload any) error {
	if fiberCtx == nil {
		return ErrContextNotFound
	}

	ct := strings.TrimSpace(fiberCtx.Get(fiber.HeaderContentType))
	if ct != "" {
		mediaType, _, err := mime.ParseMediaType(ct)
		if err != nil {
			mediaType = strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])
		}

		if !strings.EqualFold(mediaType, fiber.MIMEApplicationJSON) {
			return ErrUnsupportedContentType
		}

		fiberCtx.Request().Header.SetContentType(fiber.MIMEApplicationJSON)
	}

	if err := fiberCtx.BodyParser(payload); err != nil {
		return fmt.Errorf("%w: %w", ErrBodyParseFailed, err)
	}

	return ValidateStruct(payload)
}

// ValidateSortDirection validates and normalizes a sort direction string.
// Only "ASC" and "DESC" (case-insensitive) are allowed.
// Returns "ASC" as the safe default for any invalid input.
func ValidateSortDirection(dir string) string {
	upper := strings.ToUpper(strings.TrimSpace(dir))
	if upper == cn.SortDirDESC {
		return cn.SortDirDESC
	}

	return cn.SortDirASC
}

// ValidateLimit validates and normalizes a pagination limit.
// It ensures the limit is within the allowed range [1, maxLimit].
// If limit is <= 0, returns defaultLimit. If limit > maxLimit, returns maxLimit.
func ValidateLimit(limit, defaultLimit, maxLimit int) int {
	if limit <= 0 {
		return defaultLimit
	}

	if limit > maxLimit {
		return maxLimit
	}

	return limit
}

// MaxQueryParamLengthShort is the maximum length for short query parameters (action, entity_type, status).
const MaxQueryParamLengthShort = 50

// MaxQueryParamLengthLong is the maximum length for long query parameters (actor, assigned_to).
const MaxQueryParamLengthLong = 255

// ValidateQueryParamLength checks that a query parameter value does not exceed maxLen.
// Returns nil if the value is within bounds, or a descriptive error if it exceeds the limit.
func ValidateQueryParamLength(value, name string, maxLen int) error {
	if len(value) > maxLen {
		return fmt.Errorf("%w: '%s' must be at most %d characters", ErrQueryParamTooLong, name, maxLen)
	}

	return nil
}
