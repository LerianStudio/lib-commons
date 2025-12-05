package commons

import (
	"errors"
	"testing"

	constant "github.com/LerianStudio/lib-commons/v2/commons/constants"
	"github.com/stretchr/testify/assert"
)

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
