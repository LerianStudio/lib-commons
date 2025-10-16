package commons

import (
	"errors"
	"testing"

	constant "github.com/LerianStudio/lib-commons/v2/commons/constants"
	"github.com/stretchr/testify/assert"
)

func TestRateLimitError_Error(t *testing.T) {
	tests := []struct {
		name     string
		error    RateLimitError
		expected string
	}{
		{
			name: "error with code and message",
			error: RateLimitError{
				Code:    "RATE_LIMIT_EXCEEDED",
				Message: "Too many requests",
			},
			expected: "RATE_LIMIT_EXCEEDED - Too many requests",
		},
		{
			name: "error with only message",
			error: RateLimitError{
				Code:    "",
				Message: "Too many requests",
			},
			expected: "Too many requests",
		},
		{
			name: "error with whitespace code",
			error: RateLimitError{
				Code:    "   ",
				Message: "Too many requests",
			},
			expected: "Too many requests",
		},
		{
			name: "error with empty message",
			error: RateLimitError{
				Code:    "RATE_LIMIT_EXCEEDED",
				Message: "",
			},
			expected: "RATE_LIMIT_EXCEEDED - ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.error.Error()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRateLimitError_Unwrap(t *testing.T) {
	innerErr := errors.New("inner error")

	tests := []struct {
		name     string
		error    RateLimitError
		expected error
	}{
		{
			name: "unwrap with inner error",
			error: RateLimitError{
				Code:    "RATE_LIMIT_EXCEEDED",
				Message: "Too many requests",
				Err:     innerErr,
			},
			expected: innerErr,
		},
		{
			name: "unwrap with nil inner error",
			error: RateLimitError{
				Code:    "RATE_LIMIT_EXCEEDED",
				Message: "Too many requests",
				Err:     nil,
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.error.Unwrap()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateRateLimitError(t *testing.T) {
	innerErr := errors.New("test error")

	tests := []struct {
		name       string
		err        error
		entityType string
		validate   func(t *testing.T, result *RateLimitError)
	}{
		{
			name:       "create rate limit error with entity type",
			err:        innerErr,
			entityType: "user",
			validate: func(t *testing.T, result *RateLimitError) {
				assert.NotNil(t, result)
				assert.Equal(t, "user", result.EntityType)
				assert.Equal(t, constant.ErrRateLimitExceeded.Error(), result.Code)
				assert.Equal(t, "Rate Limit Exceeded", result.Title)
				assert.Equal(t, "Too many requests. Please try again later.", result.Message)
				assert.Equal(t, innerErr, result.Err)
			},
		},
		{
			name:       "create rate limit error without entity type",
			err:        innerErr,
			entityType: "",
			validate: func(t *testing.T, result *RateLimitError) {
				assert.NotNil(t, result)
				assert.Equal(t, "", result.EntityType)
				assert.Equal(t, constant.ErrRateLimitExceeded.Error(), result.Code)
				assert.Equal(t, "Rate Limit Exceeded", result.Title)
				assert.Equal(t, "Too many requests. Please try again later.", result.Message)
				assert.Equal(t, innerErr, result.Err)
			},
		},
		{
			name:       "create rate limit error with nil error",
			err:        nil,
			entityType: "api",
			validate: func(t *testing.T, result *RateLimitError) {
				assert.NotNil(t, result)
				assert.Equal(t, "api", result.EntityType)
				assert.Equal(t, constant.ErrRateLimitExceeded.Error(), result.Code)
				assert.Nil(t, result.Err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateRateLimitError(tt.err, tt.entityType)
			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

func TestValidateRateLimitError_ErrorInterface(t *testing.T) {
	innerErr := errors.New("test error")
	rateLimitErr := ValidateRateLimitError(innerErr, "user")

	// Test that RateLimitError implements error interface
	var err error = rateLimitErr
	assert.NotNil(t, err)

	// Test Error() method
	errorString := rateLimitErr.Error()
	assert.Contains(t, errorString, constant.ErrRateLimitExceeded.Error())
	assert.Contains(t, errorString, "Too many requests")
}

func TestValidateRateLimitError_Unwrapping(t *testing.T) {
	innerErr := errors.New("original error")
	rateLimitErr := ValidateRateLimitError(innerErr, "api")

	// Test that we can unwrap to get the original error
	unwrapped := rateLimitErr.Unwrap()
	assert.Equal(t, innerErr, unwrapped)
	assert.True(t, errors.Is(rateLimitErr, innerErr))
}

func TestResponse_Error(t *testing.T) {
	tests := []struct {
		name     string
		response Response
		expected string
	}{
		{
			name: "response with message",
			response: Response{
				EntityType: "user",
				Code:       "NOT_FOUND",
				Title:      "User Not Found",
				Message:    "The requested user was not found",
			},
			expected: "The requested user was not found",
		},
		{
			name: "response with empty message",
			response: Response{
				EntityType: "user",
				Code:       "NOT_FOUND",
				Title:      "User Not Found",
				Message:    "",
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.response.Error()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateBusinessError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		entityType string
		validate   func(t *testing.T, result error)
	}{
		{
			name:       "account ineligibility error",
			err:        constant.ErrAccountIneligibility,
			entityType: "account",
			validate: func(t *testing.T, result error) {
				response, ok := result.(Response)
				assert.True(t, ok)
				assert.Equal(t, "account", response.EntityType)
				assert.Equal(t, constant.ErrAccountIneligibility.Error(), response.Code)
				assert.Equal(t, "Account Ineligibility Response", response.Title)
				assert.Contains(t, response.Message, "not eligible")
			},
		},
		{
			name:       "insufficient funds error",
			err:        constant.ErrInsufficientFunds,
			entityType: "transaction",
			validate: func(t *testing.T, result error) {
				response, ok := result.(Response)
				assert.True(t, ok)
				assert.Equal(t, "transaction", response.EntityType)
				assert.Equal(t, constant.ErrInsufficientFunds.Error(), response.Code)
				assert.Equal(t, "Insufficient Funds Response", response.Title)
				assert.Contains(t, response.Message, "insufficient funds")
			},
		},
		{
			name:       "asset code not found error",
			err:        constant.ErrAssetCodeNotFound,
			entityType: "asset",
			validate: func(t *testing.T, result error) {
				response, ok := result.(Response)
				assert.True(t, ok)
				assert.Equal(t, "asset", response.EntityType)
				assert.Equal(t, constant.ErrAssetCodeNotFound.Error(), response.Code)
				assert.Equal(t, "Asset Code Not Found", response.Title)
			},
		},
		{
			name:       "account status transaction restriction error",
			err:        constant.ErrAccountStatusTransactionRestriction,
			entityType: "account",
			validate: func(t *testing.T, result error) {
				response, ok := result.(Response)
				assert.True(t, ok)
				assert.Equal(t, "account", response.EntityType)
				assert.Equal(t, constant.ErrAccountStatusTransactionRestriction.Error(), response.Code)
			},
		},
		{
			name:       "overflow int64 error",
			err:        constant.ErrOverFlowInt64,
			entityType: "calculation",
			validate: func(t *testing.T, result error) {
				response, ok := result.(Response)
				assert.True(t, ok)
				assert.Equal(t, "calculation", response.EntityType)
				assert.Equal(t, constant.ErrOverFlowInt64.Error(), response.Code)
				assert.Contains(t, response.Message, "overflow")
			},
		},
		{
			name:       "on hold external account error",
			err:        constant.ErrOnHoldExternalAccount,
			entityType: "account",
			validate: func(t *testing.T, result error) {
				response, ok := result.(Response)
				assert.True(t, ok)
				assert.Equal(t, "account", response.EntityType)
				assert.Equal(t, constant.ErrOnHoldExternalAccount.Error(), response.Code)
			},
		},
		{
			name:       "unknown error - return as is",
			err:        errors.New("unknown error"),
			entityType: "unknown",
			validate: func(t *testing.T, result error) {
				assert.Equal(t, "unknown error", result.Error())
			},
		},
		{
			name:       "nil error - return as is",
			err:        nil,
			entityType: "test",
			validate: func(t *testing.T, result error) {
				assert.Nil(t, result)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateBusinessError(tt.err, tt.entityType)
			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

func TestValidateBusinessError_WithArgs(t *testing.T) {
	// Test that ValidateBusinessError accepts variadic args (even if not used currently)
	result := ValidateBusinessError(constant.ErrAccountIneligibility, "account", "arg1", "arg2")

	response, ok := result.(Response)
	assert.True(t, ok)
	assert.Equal(t, "account", response.EntityType)
}
