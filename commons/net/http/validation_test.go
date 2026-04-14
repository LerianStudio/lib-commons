//go:build unit

package http

import (
	"testing"

	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
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
