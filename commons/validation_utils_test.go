package commons

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckMetadataKeyAndValueLength(t *testing.T) {
	tests := []struct {
		name        string
		limit       int
		metadata    map[string]any
		expectError bool
		errorCode   string
	}{
		{
			name:  "Valid metadata",
			limit: 10,
			metadata: map[string]any{
				"key1": "value1",
				"key2": 123,
				"key3": true,
			},
			expectError: false,
		},
		{
			name:  "Key too long",
			limit: 5,
			metadata: map[string]any{
				"verylongkey": "value",
			},
			expectError: true,
			errorCode:   "0050",
		},
		{
			name:  "Value too long",
			limit: 5,
			metadata: map[string]any{
				"key": "verylongvalue",
			},
			expectError: true,
			errorCode:   "0051",
		},
		{
			name:  "Float value",
			limit: 10,
			metadata: map[string]any{
				"key": 123.456,
			},
			expectError: false,
		},
		{
			name:  "Boolean value",
			limit: 10,
			metadata: map[string]any{
				"key": false,
			},
			expectError: false,
		},
		{
			name:        "Empty metadata",
			limit:       10,
			metadata:    map[string]any{},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckMetadataKeyAndValueLength(tt.limit, tt.metadata)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, tt.errorCode, err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateCountryAddress(t *testing.T) {
	tests := []struct {
		name        string
		country     string
		expectError bool
	}{
		{
			name:        "Valid country - US",
			country:     "US",
			expectError: false,
		},
		{
			name:        "Valid country - BR",
			country:     "BR",
			expectError: false,
		},
		{
			name:        "Valid country - GB",
			country:     "GB",
			expectError: false,
		},
		{
			name:        "Invalid country code",
			country:     "XX",
			expectError: true,
		},
		{
			name:        "Lowercase country code",
			country:     "us",
			expectError: true,
		},
		{
			name:        "Empty string",
			country:     "",
			expectError: true,
		},
		{
			name:        "Too long code",
			country:     "USA",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCountryAddress(tt.country)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, "0032", err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateAccountType(t *testing.T) {
	tests := []struct {
		name        string
		accountType string
		expectError bool
	}{
		{
			name:        "Valid type - deposit",
			accountType: "deposit",
			expectError: false,
		},
		{
			name:        "Valid type - savings",
			accountType: "savings",
			expectError: false,
		},
		{
			name:        "Valid type - loans",
			accountType: "loans",
			expectError: false,
		},
		{
			name:        "Valid type - marketplace",
			accountType: "marketplace",
			expectError: false,
		},
		{
			name:        "Valid type - creditCard",
			accountType: "creditCard",
			expectError: false,
		},
		{
			name:        "Invalid type",
			accountType: "checking",
			expectError: true,
		},
		{
			name:        "Empty string",
			accountType: "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAccountType(tt.accountType)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, "0066", err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateType(t *testing.T) {
	tests := []struct {
		name        string
		assetType   string
		expectError bool
	}{
		{
			name:        "Valid type - crypto",
			assetType:   "crypto",
			expectError: false,
		},
		{
			name:        "Valid type - currency",
			assetType:   "currency",
			expectError: false,
		},
		{
			name:        "Valid type - commodity",
			assetType:   "commodity",
			expectError: false,
		},
		{
			name:        "Valid type - others",
			assetType:   "others",
			expectError: false,
		},
		{
			name:        "Invalid type",
			assetType:   "stock",
			expectError: true,
		},
		{
			name:        "Empty string",
			assetType:   "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateType(tt.assetType)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, "0040", err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateCode(t *testing.T) {
	tests := []struct {
		name        string
		code        string
		expectError bool
		errorCode   string
	}{
		{
			name:        "Valid uppercase code",
			code:        "USD",
			expectError: false,
		},
		{
			name:        "Valid single letter",
			code:        "A",
			expectError: false,
		},
		{
			name:        "Lowercase letters",
			code:        "usd",
			expectError: true,
			errorCode:   "0004",
		},
		{
			name:        "Mixed case",
			code:        "UsD",
			expectError: true,
			errorCode:   "0004",
		},
		{
			name:        "Contains numbers",
			code:        "US1",
			expectError: true,
			errorCode:   "0033",
		},
		{
			name:        "Contains special characters",
			code:        "US-D",
			expectError: true,
			errorCode:   "0033",
		},
		{
			name:        "Empty string",
			code:        "",
			expectError: false, // Empty string passes as it has no characters
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCode(tt.code)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, tt.errorCode, err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateCurrency(t *testing.T) {
	tests := []struct {
		name        string
		code        string
		expectError bool
	}{
		{
			name:        "Valid currency - USD",
			code:        "USD",
			expectError: false,
		},
		{
			name:        "Valid currency - EUR",
			code:        "EUR",
			expectError: false,
		},
		{
			name:        "Valid currency - BRL",
			code:        "BRL",
			expectError: false,
		},
		{
			name:        "Invalid currency code",
			code:        "XXX",
			expectError: true,
		},
		{
			name:        "Lowercase currency",
			code:        "usd",
			expectError: true,
		},
		{
			name:        "Empty string",
			code:        "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCurrency(tt.code)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, "0005", err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
