//go:build unit

package http

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cn "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testPayload struct {
	Name     string `json:"name"     validate:"required,max=50"`
	Email    string `json:"email"    validate:"required,email"`
	Priority int    `json:"priority" validate:"required,gt=0"`
}

type testOptionalPayload struct {
	Name  string `json:"name"  validate:"omitempty,max=50"`
	Value int    `json:"value" validate:"omitempty,gte=0"`
}

type testPositiveDecimalPayload struct {
	Amount decimal.Decimal `json:"amount" validate:"positive_decimal"`
}

type testPositiveAmountPayload struct {
	Amount string `json:"amount" validate:"positive_amount"`
}

type testNonNegativeAmountPayload struct {
	Amount string `json:"amount" validate:"nonnegative_amount"`
}

type testURLPayload struct {
	Website string `json:"website" validate:"required,url"`
}

type testUUIDPayload struct {
	ID string `json:"id" validate:"required,uuid"`
}

type testLtePayload struct {
	Value int `json:"value" validate:"lte=100"`
}

type testLtPayload struct {
	Value int `json:"value" validate:"lt=100"`
}

type testMinPayload struct {
	Name string `json:"name" validate:"min=5"`
}

func TestGetValidator(t *testing.T) {
	t.Parallel()

	v1, err1 := GetValidator()
	v2, err2 := GetValidator()

	require.NoError(t, err1)
	require.NoError(t, err2)
	assert.NotNil(t, v1)
	assert.Same(t, v1, v2, "GetValidator should return singleton")
}

func TestValidateStruct(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		payload     any
		wantErr     bool
		errContains string
	}{
		{
			name: "valid payload",
			payload: &testPayload{
				Name:     "test",
				Email:    "test@example.com",
				Priority: 1,
			},
			wantErr: false,
		},
		{
			name: "missing required name",
			payload: &testPayload{
				Email:    "test@example.com",
				Priority: 1,
			},
			wantErr:     true,
			errContains: "field is required: 'name'",
		},
		{
			name: "invalid email",
			payload: &testPayload{
				Name:     "test",
				Email:    "not-an-email",
				Priority: 1,
			},
			wantErr:     true,
			errContains: "field must be a valid email: 'email'",
		},
		{
			name: "priority must be greater than 0",
			payload: &testPayload{
				Name:     "test",
				Email:    "test@example.com",
				Priority: 0,
			},
			wantErr:     true,
			errContains: "'priority'",
		},
		{
			name: "name exceeds max length",
			payload: &testPayload{
				Name:     "this is a very long name that exceeds the maximum allowed length of fifty characters",
				Email:    "test@example.com",
				Priority: 1,
			},
			wantErr:     true,
			errContains: "field exceeds maximum length: 'name'",
		},
		{
			name:    "optional fields can be empty",
			payload: &testOptionalPayload{},
			wantErr: false,
		},
		{
			name: "optional field with valid value",
			payload: &testOptionalPayload{
				Name:  "test",
				Value: 10,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateStruct(tt.payload)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestToSnakeCase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"Name", "name"},
		{"FirstName", "first_name"},
		{"HTMLParser", "h_t_m_l_parser"},
		{"userID", "user_i_d"},
		{"simple", "simple"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()

			got := toSnakeCase(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatValidationError(t *testing.T) {
	t.Parallel()

	type testStruct struct {
		Required string `validate:"required"`
		Max      string `validate:"max=10"`
		Min      string `validate:"min=5"`
		Gt       int    `validate:"gt=0"`
		Gte      int    `validate:"gte=10"`
		Lt       int    `validate:"lt=100"`
		Lte      int    `validate:"lte=50"`
		OneOf    string `validate:"oneof=a b c"`
		Email    string `validate:"email"`
		URL      string `validate:"url"`
		UUID     string `validate:"uuid"`
	}

	tests := []struct {
		name    string
		payload testStruct
		errTag  string
	}{
		{
			name:    "required tag",
			payload: testStruct{},
			errTag:  "required",
		},
		{
			name:    "max tag",
			payload: testStruct{Required: "x", Max: "this is too long"},
			errTag:  "max",
		},
		{
			name:    "oneof tag",
			payload: testStruct{Required: "x", OneOf: "invalid"},
			errTag:  "oneof",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateStruct(&tt.payload)
			require.Error(t, err)
		})
	}
}

func TestValidateSortDirection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "uppercase ASC", input: "ASC", want: "ASC"},
		{name: "uppercase DESC", input: "DESC", want: "DESC"},
		{name: "lowercase asc", input: "asc", want: "ASC"},
		{name: "lowercase desc", input: "desc", want: "DESC"},
		{name: "mixed case Asc", input: "Asc", want: "ASC"},
		{name: "mixed case Desc", input: "Desc", want: "DESC"},
		{name: "empty string defaults to ASC", input: "", want: "ASC"},
		{name: "whitespace only defaults to ASC", input: "   ", want: "ASC"},
		{name: "with leading whitespace", input: "  DESC", want: "DESC"},
		{name: "with trailing whitespace", input: "ASC  ", want: "ASC"},
		{name: "invalid value defaults to ASC", input: "INVALID", want: "ASC"},
		{
			name:  "SQL injection attempt defaults to ASC",
			input: "ASC; DROP TABLE users;--",
			want:  "ASC",
		},
		{name: "partial match defaults to ASC", input: "ASCENDING", want: "ASC"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ValidateSortDirection(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		limit        int
		defaultLimit int
		maxLimit     int
		expected     int
	}{
		{"zero uses default", 0, 20, 100, 20},
		{"negative uses default", -5, 20, 100, 20},
		{"valid limit unchanged", 50, 20, 100, 50},
		{"exceeds max capped", 150, 20, 100, 100},
		{"equals max unchanged", 100, 20, 100, 100},
		{"equals default", 20, 20, 100, 20},
		{"min valid (1)", 1, 20, 100, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := ValidateLimit(tc.limit, tc.defaultLimit, tc.maxLimit)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestParseBodyAndValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		contentType string
		payload     any
		wantErr     bool
		errContains string
	}{
		{
			name:        "valid JSON payload",
			body:        `{"name":"test","email":"test@example.com","priority":1}`,
			contentType: "application/json",
			payload:     &testPayload{},
			wantErr:     false,
		},
		{
			name:        "invalid JSON",
			body:        `{"name": invalid}`,
			contentType: "application/json",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "failed to parse request body",
		},
		{
			name:        "valid JSON but validation fails",
			body:        `{"name":"","email":"test@example.com","priority":1}`,
			contentType: "application/json",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "field is required: 'name'",
		},
		{
			name:        "empty body",
			body:        "",
			contentType: "application/json",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "failed to parse request body",
		},
		{
			name:        "application/json with charset is accepted",
			body:        `{"name":"test","email":"test@example.com","priority":1}`,
			contentType: "application/json; charset=utf-8",
			payload:     &testPayload{},
			wantErr:     false,
		},
		{
			name:        "empty Content-Type falls through to body parser",
			body:        `{"name":"test","email":"test@example.com","priority":1}`,
			contentType: "",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "failed to parse request body",
		},
		{
			name:        "text/plain Content-Type is rejected",
			body:        `{"name":"test","email":"test@example.com","priority":1}`,
			contentType: "text/plain",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "Content-Type must be application/json",
		},
		{
			name:        "text/xml Content-Type is rejected",
			body:        `<data/>`,
			contentType: "text/xml",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "Content-Type must be application/json",
		},
		{
			name:        "multipart/form-data Content-Type is rejected",
			body:        `{"name":"test"}`,
			contentType: "multipart/form-data",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "Content-Type must be application/json",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			app.Post("/test", func(c *fiber.Ctx) error {
				err := ParseBodyAndValidate(c, tc.payload)
				if err != nil {
					return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
				}

				return c.SendStatus(fiber.StatusOK)
			})

			req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewBufferString(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}

			resp, err := app.Test(req)
			require.NoError(t, err)

			defer func() {
				_ = resp.Body.Close()
			}()

			if tc.wantErr {
				assert.Equal(t, fiber.StatusBadRequest, resp.StatusCode)
			} else {
				assert.Equal(t, fiber.StatusOK, resp.StatusCode)
			}
		})
	}
}

func TestPositiveDecimalValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		amount  decimal.Decimal
		wantErr bool
	}{
		{
			name:    "positive amount is valid",
			amount:  decimal.NewFromFloat(100.50),
			wantErr: false,
		},
		{
			name:    "zero is invalid",
			amount:  decimal.Zero,
			wantErr: true,
		},
		{
			name:    "negative is invalid",
			amount:  decimal.NewFromFloat(-50.00),
			wantErr: true,
		},
		{
			name:    "small positive is valid",
			amount:  decimal.NewFromFloat(0.01),
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testPositiveDecimalPayload{Amount: tc.amount}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "amount")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestPositiveAmountValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		amount  string
		wantErr bool
	}{
		{
			name:    "positive amount is valid",
			amount:  "100.50",
			wantErr: false,
		},
		{
			name:    "zero is invalid",
			amount:  "0",
			wantErr: true,
		},
		{
			name:    "negative is invalid",
			amount:  "-50.00",
			wantErr: true,
		},
		{
			name:    "empty string is valid (let required handle it)",
			amount:  "",
			wantErr: false,
		},
		{
			name:    "invalid decimal string",
			amount:  "not-a-number",
			wantErr: true,
		},
		{
			name:    "small positive is valid",
			amount:  "0.01",
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testPositiveAmountPayload{Amount: tc.amount}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNonNegativeAmountValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		amount  string
		wantErr bool
	}{
		{
			name:    "positive amount is valid",
			amount:  "100.50",
			wantErr: false,
		},
		{
			name:    "zero is valid",
			amount:  "0",
			wantErr: false,
		},
		{
			name:    "negative is invalid",
			amount:  "-50.00",
			wantErr: true,
		},
		{
			name:    "empty string is valid (let required handle it)",
			amount:  "",
			wantErr: false,
		},
		{
			name:    "invalid decimal string",
			amount:  "not-a-number",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testNonNegativeAmountPayload{Amount: tc.amount}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "amount")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestURLValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		website string
		wantErr bool
	}{
		{
			name:    "valid HTTP URL",
			website: "http://example.com",
			wantErr: false,
		},
		{
			name:    "valid HTTPS URL",
			website: "https://example.com/path",
			wantErr: false,
		},
		{
			name:    "invalid URL",
			website: "not-a-url",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testURLPayload{Website: tc.website}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrFieldURL)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestUUIDValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{
			name:    "valid UUID",
			id:      "550e8400-e29b-41d4-a716-446655440000",
			wantErr: false,
		},
		{
			name:    "invalid UUID",
			id:      "not-a-uuid",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testUUIDPayload{ID: tc.id}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrFieldUUID)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLteValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   int
		wantErr bool
	}{
		{
			name:    "value less than constraint is valid",
			value:   50,
			wantErr: false,
		},
		{
			name:    "value equal to constraint is valid",
			value:   100,
			wantErr: false,
		},
		{
			name:    "value greater than constraint is invalid",
			value:   101,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testLtePayload{Value: tc.value}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrFieldLessThanOrEqual)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLtValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   int
		wantErr bool
	}{
		{
			name:    "value less than constraint is valid",
			value:   50,
			wantErr: false,
		},
		{
			name:    "value equal to constraint is invalid",
			value:   100,
			wantErr: true,
		},
		{
			name:    "value greater than constraint is invalid",
			value:   101,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testLtPayload{Value: tc.value}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrFieldLessThan)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMinValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{
			name:    "value at minimum is valid",
			value:   "hello",
			wantErr: false,
		},
		{
			name:    "value above minimum is valid",
			value:   "hello world",
			wantErr: false,
		},
		{
			name:    "value below minimum is invalid",
			value:   "hi",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testMinPayload{Name: tc.value}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrFieldMinLength)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidationSentinelErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "ErrValidationFailed",
			err:      ErrValidationFailed,
			expected: "validation failed",
		},
		{
			name:     "ErrFieldRequired",
			err:      ErrFieldRequired,
			expected: "field is required",
		},
		{
			name:     "ErrFieldMaxLength",
			err:      ErrFieldMaxLength,
			expected: "field exceeds maximum length",
		},
		{
			name:     "ErrFieldMinLength",
			err:      ErrFieldMinLength,
			expected: "field below minimum length",
		},
		{
			name:     "ErrFieldGreaterThan",
			err:      ErrFieldGreaterThan,
			expected: "field must be greater than constraint",
		},
		{
			name:     "ErrFieldGreaterThanOrEqual",
			err:      ErrFieldGreaterThanOrEqual,
			expected: "field must be greater than or equal to constraint",
		},
		{
			name:     "ErrFieldLessThan",
			err:      ErrFieldLessThan,
			expected: "field must be less than constraint",
		},
		{
			name:     "ErrFieldLessThanOrEqual",
			err:      ErrFieldLessThanOrEqual,
			expected: "field must be less than or equal to constraint",
		},
		{
			name:     "ErrFieldOneOf",
			err:      ErrFieldOneOf,
			expected: "field must be one of allowed values",
		},
		{
			name:     "ErrFieldEmail",
			err:      ErrFieldEmail,
			expected: "field must be a valid email",
		},
		{
			name:     "ErrFieldURL",
			err:      ErrFieldURL,
			expected: "field must be a valid URL",
		},
		{
			name:     "ErrFieldUUID",
			err:      ErrFieldUUID,
			expected: "field must be a valid UUID",
		},
		{
			name:     "ErrFieldPositiveAmount",
			err:      ErrFieldPositiveAmount,
			expected: "field must be a positive amount",
		},
		{
			name:     "ErrFieldNonNegativeAmount",
			err:      ErrFieldNonNegativeAmount,
			expected: "field must be a non-negative amount",
		},
		{
			name:     "ErrBodyParseFailed",
			err:      ErrBodyParseFailed,
			expected: "failed to parse request body",
		},
		{
			name:     "ErrQueryParamTooLong",
			err:      ErrQueryParamTooLong,
			expected: "query parameter exceeds maximum length",
		},
		{
			name:     "ErrUnsupportedContentType",
			err:      ErrUnsupportedContentType,
			expected: "Content-Type must be application/json",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.expected, tc.err.Error())
		})
	}
}

func TestPaginationConstants_Validation(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 20, cn.DefaultLimit)
	assert.Equal(t, 200, cn.MaxLimit)
}

func TestQueryParamLengthConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 50, MaxQueryParamLengthShort)
	assert.Equal(t, 255, MaxQueryParamLengthLong)
}

func TestValidateQueryParamLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		value       string
		paramName   string
		maxLen      int
		wantErr     bool
		errContains string
	}{
		{
			name:      "value within limit",
			value:     "CREATE",
			paramName: "action",
			maxLen:    50,
			wantErr:   false,
		},
		{
			name:      "value at exact limit",
			value:     strings.Repeat("a", 50),
			paramName: "action",
			maxLen:    50,
			wantErr:   false,
		},
		{
			name:        "value exceeds limit",
			value:       strings.Repeat("a", 51),
			paramName:   "action",
			maxLen:      50,
			wantErr:     true,
			errContains: "'action' must be at most 50 characters",
		},
		{
			name:      "empty value always valid",
			value:     "",
			paramName: "actor",
			maxLen:    255,
			wantErr:   false,
		},
		{
			name:        "long value exceeds short limit",
			value:       strings.Repeat("x", 256),
			paramName:   "entity_type",
			maxLen:      255,
			wantErr:     true,
			errContains: "'entity_type' must be at most 255 characters",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateQueryParamLength(tc.value, tc.paramName, tc.maxLen)

			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrQueryParamTooLong)
				assert.Contains(t, err.Error(), tc.errContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Nil guard tests
// ---------------------------------------------------------------------------

func TestParseBodyAndValidate_NilContext(t *testing.T) {
	t.Parallel()

	payload := &testPayload{}
	err := ParseBodyAndValidate(nil, payload)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}

func TestUnknownValidationTag(t *testing.T) {
	t.Parallel()

	type customPayload struct {
		Value string `validate:"alphanum"`
	}

	payload := customPayload{Value: "hello@world"}
	err := ValidateStruct(&payload)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrValidationFailed)
	assert.Contains(t, err.Error(), "failed 'alphanum' check")
}
