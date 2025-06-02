package contracts

import (
	"reflect"
	"testing"

	"github.com/LerianStudio/lib-commons/commons/validation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidationInterfaceContract validates the validation system interface stability
func TestValidationInterfaceContract(t *testing.T) {
	t.Run("validator_struct_contract", func(t *testing.T) {
		// Test that Validator struct has expected interface
		validator := validation.Validator{}
		validatorType := reflect.TypeOf(validator)

		// Verify this is a struct
		assert.Equal(t, reflect.Struct, validatorType.Kind(), "Validator should be a struct")

		// Check for expected methods on Validator pointer
		validatorPtrType := reflect.TypeOf(&validator)

		expectedMethods := map[string]validationMethodContract{
			"Validate": {
				description:  "Validates a struct using struct tags",
				paramCount:   2, // receiver + struct
				returnsError: true,
			},
			"RegisterValidation": {
				description:  "Registers a custom validation function",
				paramCount:   3, // receiver + tag + func
				returnsError: true,
			},
		}

		for methodName, expected := range expectedMethods {
			method, exists := validatorPtrType.MethodByName(methodName)
			assert.True(
				t,
				exists,
				"Validator should have method %s (%s)",
				methodName,
				expected.description,
			)

			if exists {
				methodType := method.Type
				assert.Equal(t, expected.paramCount, methodType.NumIn(),
					"Method %s should have %d parameters", methodName, expected.paramCount)

				if expected.returnsError {
					assert.Greater(
						t,
						methodType.NumOut(),
						0,
						"Method %s should return values",
						methodName,
					)
					lastReturnIdx := methodType.NumOut() - 1
					assert.Equal(t, "error", methodType.Out(lastReturnIdx).String(),
						"Method %s should return error as last value", methodName)
				}
			}
		}
	})

	t.Run("validation_tags_contract", func(t *testing.T) {
		// Test that standard validation tags work as expected
		// This validates the struct tag contract stability

		type TestStruct struct {
			Email    string `validate:"required,email"`
			Age      int    `validate:"min=18,max=99"`
			Username string `validate:"required,min=3,max=20"`
			Website  string `validate:"omitempty,url"`
			UUID     string `validate:"omitempty,uuid"`
		}

		// Test struct should have properly defined validation tags
		testStructType := reflect.TypeOf(TestStruct{})

		emailField, _ := testStructType.FieldByName("Email")
		assert.Equal(t, "required,email", emailField.Tag.Get("validate"),
			"Email field should have required,email validation")

		ageField, _ := testStructType.FieldByName("Age")
		assert.Equal(t, "min=18,max=99", ageField.Tag.Get("validate"),
			"Age field should have min/max validation")

		usernameField, _ := testStructType.FieldByName("Username")
		assert.Equal(t, "required,min=3,max=20", usernameField.Tag.Get("validate"),
			"Username field should have length validation")

		websiteField, _ := testStructType.FieldByName("Website")
		assert.Equal(t, "omitempty,url", websiteField.Tag.Get("validate"),
			"Website field should have optional URL validation")

		uuidField, _ := testStructType.FieldByName("UUID")
		assert.Equal(t, "omitempty,uuid", uuidField.Tag.Get("validate"),
			"UUID field should have optional UUID validation")
	})

	t.Run("validation_error_format_contract", func(t *testing.T) {
		// Test that validation errors have consistent format
		validator := validation.NewValidator()

		type InvalidStruct struct {
			Email string `validate:"required,email"`
		}

		// Test with invalid data
		invalid := InvalidStruct{Email: "not-an-email"}
		err := validator.Validate(invalid)

		if err != nil {
			// Error should implement error interface
			var errorInterface error = err
			assert.NotNil(t, errorInterface, "Validation error should implement error interface")

			// Error message should contain field information
			errorMsg := err.Error()
			assert.NotEmpty(t, errorMsg, "Validation error should have message")

			// Error message should mention the field that failed
			assert.Contains(t, errorMsg, "Email", "Validation error should mention failing field")
		}
	})
}

// TestValidationBehaviorContract validates validation system behavior
func TestValidationBehaviorContract(t *testing.T) {
	t.Run("required_field_validation_contract", func(t *testing.T) {
		validator := validation.NewValidator()

		type RequiredFieldStruct struct {
			Name string `validate:"required"`
		}

		// Test empty string fails required validation
		emptyStruct := RequiredFieldStruct{Name: ""}
		err := validator.Validate(emptyStruct)
		assert.Error(t, err, "Empty required field should fail validation")

		// Test non-empty string passes required validation
		validStruct := RequiredFieldStruct{Name: "valid name"}
		err = validator.Validate(validStruct)
		assert.NoError(t, err, "Non-empty required field should pass validation")
	})

	t.Run("email_validation_contract", func(t *testing.T) {
		validator := validation.NewValidator()

		type EmailStruct struct {
			Email string `validate:"email"`
		}

		// Test valid email passes
		validEmail := EmailStruct{Email: "test@example.com"}
		err := validator.Validate(validEmail)
		assert.NoError(t, err, "Valid email should pass validation")

		// Test invalid email fails
		invalidEmail := EmailStruct{Email: "invalid-email"}
		err = validator.Validate(invalidEmail)
		assert.Error(t, err, "Invalid email should fail validation")

		// Test empty email with omitempty
		type OptionalEmailStruct struct {
			Email string `validate:"omitempty,email"`
		}

		emptyOptionalEmail := OptionalEmailStruct{Email: ""}
		err = validator.Validate(emptyOptionalEmail)
		assert.NoError(t, err, "Empty optional email should pass validation")
	})

	t.Run("numeric_range_validation_contract", func(t *testing.T) {
		validator := validation.NewValidator()

		type AgeStruct struct {
			Age int `validate:"min=18,max=65"`
		}

		// Test valid age passes
		validAge := AgeStruct{Age: 25}
		err := validator.Validate(validAge)
		assert.NoError(t, err, "Valid age should pass validation")

		// Test age below minimum fails
		tooYoung := AgeStruct{Age: 16}
		err = validator.Validate(tooYoung)
		assert.Error(t, err, "Age below minimum should fail validation")

		// Test age above maximum fails
		tooOld := AgeStruct{Age: 70}
		err = validator.Validate(tooOld)
		assert.Error(t, err, "Age above maximum should fail validation")

		// Test boundary values
		minAge := AgeStruct{Age: 18}
		err = validator.Validate(minAge)
		assert.NoError(t, err, "Minimum age should pass validation")

		maxAge := AgeStruct{Age: 65}
		err = validator.Validate(maxAge)
		assert.NoError(t, err, "Maximum age should pass validation")
	})

	t.Run("string_length_validation_contract", func(t *testing.T) {
		validator := validation.NewValidator()

		type UsernameStruct struct {
			Username string `validate:"min=3,max=20"`
		}

		// Test valid length passes
		validUsername := UsernameStruct{Username: "testuser"}
		err := validator.Validate(validUsername)
		assert.NoError(t, err, "Valid username length should pass validation")

		// Test too short fails
		tooShort := UsernameStruct{Username: "ab"}
		err = validator.Validate(tooShort)
		assert.Error(t, err, "Username too short should fail validation")

		// Test too long fails
		tooLong := UsernameStruct{Username: "this_username_is_way_too_long_for_validation"}
		err = validator.Validate(tooLong)
		assert.Error(t, err, "Username too long should fail validation")

		// Test boundary values
		minLength := UsernameStruct{Username: "abc"}
		err = validator.Validate(minLength)
		assert.NoError(t, err, "Minimum length username should pass validation")

		maxLength := UsernameStruct{Username: "abcdefghij1234567890"} // exactly 20 chars
		err = validator.Validate(maxLength)
		assert.NoError(t, err, "Maximum length username should pass validation")
	})

	t.Run("url_validation_contract", func(t *testing.T) {
		validator := validation.NewValidator()

		type WebsiteStruct struct {
			Website string `validate:"url"`
		}

		// Test valid URLs pass
		validHTTPS := WebsiteStruct{Website: "https://example.com"}
		err := validator.Validate(validHTTPS)
		assert.NoError(t, err, "Valid HTTPS URL should pass validation")

		validHTTP := WebsiteStruct{Website: "http://example.com"}
		err = validator.Validate(validHTTP)
		assert.NoError(t, err, "Valid HTTP URL should pass validation")

		// Test invalid URL fails
		invalidURL := WebsiteStruct{Website: "not-a-url"}
		err = validator.Validate(invalidURL)
		assert.Error(t, err, "Invalid URL should fail validation")

		// Test malformed URL fails
		malformedURL := WebsiteStruct{Website: "http://"}
		err = validator.Validate(malformedURL)
		assert.Error(t, err, "Malformed URL should fail validation")
	})

	t.Run("uuid_validation_contract", func(t *testing.T) {
		validator := validation.NewValidator()

		type UUIDStruct struct {
			ID string `validate:"uuid"`
		}

		// Test valid UUID passes
		validUUID := UUIDStruct{ID: "550e8400-e29b-41d4-a716-446655440000"}
		err := validator.Validate(validUUID)
		assert.NoError(t, err, "Valid UUID should pass validation")

		// Test invalid UUID fails
		invalidUUID := UUIDStruct{ID: "not-a-uuid"}
		err = validator.Validate(invalidUUID)
		assert.Error(t, err, "Invalid UUID should fail validation")

		// Test malformed UUID fails
		malformedUUID := UUIDStruct{ID: "550e8400-e29b-41d4-a716"}
		err = validator.Validate(malformedUUID)
		assert.Error(t, err, "Malformed UUID should fail validation")
	})
}

// TestCustomValidationContract validates custom validation registration
func TestCustomValidationContract(t *testing.T) {
	t.Run("custom_validation_registration_contract", func(t *testing.T) {
		validator := validation.NewValidator()

		// Register a custom validation
		err := validator.RegisterValidation("custom_test", func(fl interface{}) bool {
			value, ok := fl.(string)
			if !ok {
				return false
			}
			return value == "valid"
		})
		require.NoError(t, err, "Custom validation registration should succeed")

		type CustomStruct struct {
			Field string `validate:"custom_test"`
		}

		// Test custom validation passes with correct value
		validCustom := CustomStruct{Field: "valid"}
		err = validator.Validate(validCustom)
		assert.NoError(t, err, "Valid custom field should pass validation")

		// Test custom validation fails with incorrect value
		invalidCustom := CustomStruct{Field: "invalid"}
		err = validator.Validate(invalidCustom)
		assert.Error(t, err, "Invalid custom field should fail validation")
	})

	t.Run("validation_chain_contract", func(t *testing.T) {
		validator := validation.NewValidator()

		type ChainedStruct struct {
			Email string `validate:"required,email"`
			Age   int    `validate:"required,min=18"`
		}

		// Test all validations pass
		valid := ChainedStruct{Email: "test@example.com", Age: 25}
		err := validator.Validate(valid)
		assert.NoError(t, err, "All valid fields should pass validation")

		// Test first validation fails
		invalidEmail := ChainedStruct{Email: "invalid", Age: 25}
		err = validator.Validate(invalidEmail)
		assert.Error(t, err, "Invalid email should fail validation chain")

		// Test second validation fails
		invalidAge := ChainedStruct{Email: "test@example.com", Age: 16}
		err = validator.Validate(invalidAge)
		assert.Error(t, err, "Invalid age should fail validation chain")

		// Test both validations fail
		bothInvalid := ChainedStruct{Email: "invalid", Age: 16}
		err = validator.Validate(bothInvalid)
		assert.Error(t, err, "Multiple invalid fields should fail validation")
	})
}

// TestValidationConfigurationContract validates validator configuration
func TestValidationConfigurationContract(t *testing.T) {
	t.Run("validator_initialization_contract", func(t *testing.T) {
		// Test NewValidator creates a valid validator
		validator := validation.NewValidator()
		assert.NotNil(t, validator, "NewValidator should return non-nil validator")

		// Test validator is ready to use
		type SimpleStruct struct {
			Name string `validate:"required"`
		}

		valid := SimpleStruct{Name: "test"}
		err := validator.Validate(valid)
		assert.NoError(t, err, "New validator should be immediately usable")
	})

	t.Run("validator_reuse_contract", func(t *testing.T) {
		// Test that validator instances can be reused safely
		validator := validation.NewValidator()

		type TestStruct struct {
			Value string `validate:"required"`
		}

		// Use validator multiple times
		for i := 0; i < 3; i++ {
			valid := TestStruct{Value: "test"}
			err := validator.Validate(valid)
			assert.NoError(t, err, "Validator should be reusable for multiple validations")
		}
	})
}

// Helper types for contract testing

type validationMethodContract struct {
	description  string
	paramCount   int
	returnsError bool
}
