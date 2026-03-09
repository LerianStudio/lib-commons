//go:build unit

package security

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

func TestDefaultSensitiveFields(t *testing.T) {
	t.Parallel()

	// Test that the slice is not empty
	assert.NotEmpty(t, DefaultSensitiveFields(), "DefaultSensitiveFields should not be empty")

	// Test that all expected fields are present
	expectedFields := []string{
		"password", "token", "secret", "key", "authorization",
		"auth", "credential", "credentials", "apikey", "api_key",
		"access_token", "accesstoken", "refresh_token", "refreshtoken", "private_key", "privatekey",
	}

	for _, expectedField := range expectedFields {
		assert.Contains(t, DefaultSensitiveFields(), expectedField,
			"DefaultSensitiveFields should contain %s", expectedField)
	}

	// Test that all fields are lowercase
	for _, field := range DefaultSensitiveFields() {
		assert.Equal(t, strings.ToLower(field), field,
			"All fields in DefaultSensitiveFields should be lowercase, but found: %s", field)
	}
}

func TestDefaultSensitiveFieldsMap(t *testing.T) {
	t.Parallel()

	// Test that the map is not empty
	assert.NotEmpty(t, DefaultSensitiveFieldsMap(), "DefaultSensitiveFieldsMap should not be empty")

	// Test that map size matches slice size
	assert.Equal(t, len(DefaultSensitiveFields()), len(DefaultSensitiveFieldsMap()),
		"DefaultSensitiveFieldsMap should have the same number of entries as DefaultSensitiveFields")

	// Test that all slice entries are in the map
	for _, field := range DefaultSensitiveFields() {
		assert.True(t, DefaultSensitiveFieldsMap()[field],
			"DefaultSensitiveFieldsMap should contain field: %s", field)
	}

	// Test that all map entries are true
	for field, value := range DefaultSensitiveFieldsMap() {
		assert.True(t, value, "All values in DefaultSensitiveFieldsMap should be true, but %s is %v", field, value)
	}
}

func TestIsSensitiveField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		fieldName string
		expected  bool
	}{
		{
			name:      "sensitive field - password",
			fieldName: "password",
			expected:  true,
		},
		{
			name:      "sensitive field - newpassword",
			fieldName: "newpassword",
			expected:  true,
		},
		{
			name:      "sensitive field - oldpassword",
			fieldName: "oldpassword",
			expected:  true,
		},
		{
			name:      "sensitive field - passwordsalt",
			fieldName: "passwordsalt",
			expected:  true,
		},
		{
			name:      "sensitive field - token",
			fieldName: "token",
			expected:  true,
		},
		{
			name:      "sensitive field - uppercase PASSWORD",
			fieldName: "PASSWORD",
			expected:  true,
		},
		{
			name:      "sensitive field - mixed case PaSsWoRd",
			fieldName: "PaSsWoRd",
			expected:  true,
		},
		{
			name:      "sensitive field - api_key",
			fieldName: "api_key",
			expected:  true,
		},
		{
			name:      "sensitive field - API_KEY uppercase",
			fieldName: "API_KEY",
			expected:  true,
		},
		{
			name:      "sensitive field - client_id",
			fieldName: "client_id",
			expected:  true,
		},
		{
			name:      "sensitive field - clientid",
			fieldName: "clientid",
			expected:  true,
		},
		{
			name:      "sensitive field - client_secret",
			fieldName: "client_secret",
			expected:  true,
		},
		{
			name:      "sensitive field - clientsecret",
			fieldName: "clientsecret",
			expected:  true,
		},

		{
			name:      "sensitive field - email (PII)",
			fieldName: "email",
			expected:  true,
		},
		{
			name:      "non-sensitive field - id",
			fieldName: "id",
			expected:  false,
		},
		{
			name:      "non-sensitive field - name",
			fieldName: "name",
			expected:  false,
		},
		{
			name:      "non-sensitive field - status",
			fieldName: "status",
			expected:  false,
		},
		{
			name:      "empty string",
			fieldName: "",
			expected:  false,
		},
		{
			name:      "partial match - pass (should not match password)",
			fieldName: "pass",
			expected:  false,
		},
		{
			name:      "partial match - word (should not match password)",
			fieldName: "word",
			expected:  false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := IsSensitiveField(tt.fieldName)
			assert.Equal(t, tt.expected, result,
				"IsSensitiveField(%s) should return %v", tt.fieldName, tt.expected)
		})
	}
}

func TestIsSensitiveFieldCaseInsensitive(t *testing.T) {
	t.Parallel()

	// Test that case-insensitive matching works for all default fields
	for _, field := range DefaultSensitiveFields() {
		// Test lowercase
		assert.True(t, IsSensitiveField(field),
			"IsSensitiveField should return true for lowercase field: %s", field)

		// Test uppercase
		assert.True(t, IsSensitiveField(strings.ToUpper(field)),
			"IsSensitiveField should return true for uppercase field: %s", strings.ToUpper(field))

		// Test title case
		titleCaser := cases.Title(language.English)
		titleField := titleCaser.String(field)
		assert.True(t, IsSensitiveField(titleField),
			"IsSensitiveField should return true for title case field: %s", titleField)
	}
}

func TestConsistencyBetweenSliceAndMap(t *testing.T) {
	t.Parallel()

	// Ensure that the slice and map are consistent
	// Every field in the slice should be in the map
	for _, field := range DefaultSensitiveFields() {
		assert.Contains(t, DefaultSensitiveFieldsMap(), field,
			"Field %s from DefaultSensitiveFields should exist in DefaultSensitiveFieldsMap", field)
		assert.True(t, DefaultSensitiveFieldsMap()[field],
			"Field %s in DefaultSensitiveFieldsMap should be true", field)
	}

	// Every field in the map should be in the slice
	for field := range DefaultSensitiveFieldsMap() {
		assert.Contains(t, DefaultSensitiveFields(), field,
			"Field %s from DefaultSensitiveFieldsMap should exist in DefaultSensitiveFields", field)
	}
}

func TestDefaultFieldsAreExpected(t *testing.T) {
	t.Parallel()

	fields := DefaultSensitiveFields()

	// Assert that required categories of sensitive fields are present,
	// rather than asserting an exact count (which is brittle as the catalog grows).
	requiredFields := []string{
		// Auth credentials
		"password", "token", "secret", "api_key", "bearer",
		// Financial
		"card_number", "cvv", "account_number", "iban",
		// PII
		"ssn", "date_of_birth", "email", "phone", "address",
		// Infrastructure secrets
		"connection_string", "database_url", "private_key",
	}

	for _, required := range requiredFields {
		assert.Contains(t, fields, required,
			"DefaultSensitiveFields must contain %q", required)
	}

	// Sanity-check minimum size — the catalog should never shrink below baseline.
	assert.GreaterOrEqual(t, len(fields), len(requiredFields),
		"DefaultSensitiveFields must have at least %d entries", len(requiredFields))
}

func TestNoEmptyFields(t *testing.T) {
	t.Parallel()

	// Ensure no empty strings in the default fields
	for i, field := range DefaultSensitiveFields() {
		assert.NotEmpty(t, field, "Field at index %d should not be empty", i)
	}
}

func TestDefaultSensitiveFields_ReturnsClone(t *testing.T) {
	t.Parallel()

	original := DefaultSensitiveFields()
	original[0] = "MUTATED"

	// The mutation should not affect subsequent calls
	fresh := DefaultSensitiveFields()
	assert.NotEqual(t, "MUTATED", fresh[0], "DefaultSensitiveFields must return a clone")
}

func TestIsSensitiveField_FinancialFields(t *testing.T) {
	t.Parallel()

	financialFields := []struct {
		name     string
		expected bool
	}{
		{"card_number", true},
		{"cardnumber", true},
		{"cvv", true},
		{"cvc", true},
		{"ssn", true},
		{"social_security", true},
		{"pin", true},
		{"otp", true},
		{"account_number", true},
		{"accountnumber", true},
		{"routing_number", true},
		{"routingnumber", true},
		{"iban", true},
		{"swift", true},
		{"swift_code", true},
		{"bic", true},
		{"pan", true},
		{"expiry", true},
		{"expiry_date", true},
		{"expiration_date", true},
		{"card_expiry", true},
		{"date_of_birth", true},
		{"dob", true},
		{"tax_id", true},
		{"taxid", true},
		{"tin", true},
		{"national_id", true},
		{"sort_code", true},
		{"bsb", true},
		{"security_answer", true},
		{"security_question", true},
		{"mother_maiden_name", true},
		{"mfa_code", true},
		{"totp", true},
		{"biometric", true},
		{"fingerprint", true},
		// False positives for short tokens
		{"spinning", false},
		{"opinion", false},
		{"pineapple", false},
		{"cotton", false},
		{"panther", false},
	}

	for _, tt := range financialFields {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := IsSensitiveField(tt.name)
			assert.Equal(t, tt.expected, result,
				"IsSensitiveField(%q) = %v, want %v", tt.name, result, tt.expected)
		})
	}
}

func TestShortSensitiveTokens_ExactMatch(t *testing.T) {
	t.Parallel()

	// These short tokens should match exactly but not as substrings
	tests := []struct {
		field    string
		expected bool
	}{
		{"pin", true},
		{"otp", true},
		{"cvv", true},
		{"cvc", true},
		{"ssn", true},
		{"pan", true},
		{"bic", true},
		{"bsb", true},
		{"dob", true},
		{"tin", true},
		// CamelCase variants
		{"userPin", true},
		{"otpCode", true},
		{"userSsn", true},
		// Should NOT match as substrings in larger words
		{"spinning", false},
		{"option", false},
		{"panther", false},
		{"basic", false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.field, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, IsSensitiveField(tt.field),
				"IsSensitiveField(%q)", tt.field)
		})
	}
}

func TestNormalizeFieldName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{"sessionToken", "session_token"},
		{"APIKey", "api_key"},
		{"myPrivateKey", "my_private_key"},
		{"DateOfBirth", "date_of_birth"},
		{"simple", "simple"},
		{"already_snake", "already_snake"},
		{"HTTPSProxy", "https_proxy"},
		{"userID", "user_id"},
		{"", ""},
		{"X", "x"},
		{"ABC", "abc"},
		{"getHTTPResponse", "get_http_response"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			result := normalizeFieldName(tt.input)
			assert.Equal(t, tt.expected, result, "normalizeFieldName(%q)", tt.input)
		})
	}
}

func TestIsSensitiveField_WordBoundaryPositivePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		field    string
		expected bool
	}{
		// Word-boundary matches (pattern found with non-alphanumeric boundaries)
		{"my_secret_value", true},        // "secret" with underscore boundaries
		{"x-authorization-header", true}, // "authorization" with hyphen boundaries
		{"user_password_hash", true},     // "password" with underscore boundaries
		{"db_credential_store", true},    // "credential" with underscore boundaries
		{"old_token_backup", true},       // "token" with underscore boundaries
		// CamelCase that normalizes to word-boundary matchable form
		{"SessionToken", true},   // -> "session_token" -> "token" boundary match
		{"ExpiryDate", true},     // -> "expiry_date" -> exact map match via normalization
		{"AccountNumber", true},  // -> "account_number" -> exact map match via normalization
		{"CardNumber", true},     // -> "card_number" -> exact map match via normalization
		{"PrivateKeyData", true}, // -> "private_key_data" -> "private_key" boundary match
		// Should NOT match
		{"mysecretvalue", false}, // no word boundaries around "secret"
		{"deauthorize", false},   // "authorization" not present
		{"repass", false},        // "password" not present
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.field, func(t *testing.T) {
			t.Parallel()
			result := IsSensitiveField(tt.field)
			assert.Equal(t, tt.expected, result, "IsSensitiveField(%q)", tt.field)
		})
	}
}

func TestDefaultSensitiveFieldsMap_ReturnsClone(t *testing.T) {
	t.Parallel()

	original := DefaultSensitiveFieldsMap()
	// Mutate the returned map
	original["password"] = false
	original["INJECTED"] = true

	// Fresh call should be unaffected
	fresh := DefaultSensitiveFieldsMap()
	assert.True(t, fresh["password"], "Map mutation must not affect shared state")
	assert.False(t, fresh["INJECTED"], "Map mutation must not inject into shared state")
}

func TestIsSensitiveField_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	const goroutines = 100

	type result struct {
		password     bool
		sessionToken bool
		secretValue  bool
		userPin      bool
		harmless     bool
	}

	results := make(chan result, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			// Exercise all code paths concurrently and collect results
			r := result{
				password:     IsSensitiveField("password"),
				sessionToken: IsSensitiveField("SessionToken"),
				secretValue:  IsSensitiveField("my_secret_value"),
				userPin:      IsSensitiveField("userPin"),
				harmless:     IsSensitiveField("harmless"),
			}
			_ = DefaultSensitiveFields()
			_ = DefaultSensitiveFieldsMap()
			results <- r
		}()
	}

	for i := 0; i < goroutines; i++ {
		r := <-results
		assert.True(t, r.password, "concurrent: password should be sensitive")
		assert.True(t, r.sessionToken, "concurrent: SessionToken should be sensitive")
		assert.True(t, r.secretValue, "concurrent: my_secret_value should be sensitive")
		assert.True(t, r.userPin, "concurrent: userPin should be sensitive")
		assert.False(t, r.harmless, "concurrent: harmless should not be sensitive")
	}
}

func TestMatchesWordBoundary_EmptyPattern(t *testing.T) {
	t.Parallel()

	// Empty pattern must return false, not loop forever
	assert.False(t, matchesWordBoundary("anything", ""), "Empty pattern must return false")
	assert.False(t, matchesWordBoundary("", ""), "Both empty must return false")
}

func TestIsSensitiveField_PIIFields(t *testing.T) {
	t.Parallel()

	piiFields := []struct {
		name     string
		expected bool
	}{
		{"email", true},
		{"phone", true},
		{"phone_number", true},
		{"address", true},
		{"street", true},
		{"city", true},
		{"zip", true},
		{"postal_code", true},
		// CamelCase variants
		{"EmailAddress", true},
		{"PhoneNumber", true},
		{"PostalCode", true},
		// False positives for short tokens
		{"unzip", false},
		{"capacity", false},
		{"felicity", false},
	}

	for _, tt := range piiFields {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := IsSensitiveField(tt.name)
			assert.Equal(t, tt.expected, result,
				"IsSensitiveField(%q) = %v, want %v", tt.name, result, tt.expected)
		})
	}
}

func TestIsSensitiveField_NewV2Fields(t *testing.T) {
	t.Parallel()

	newFields := []string{
		"passwd", "passphrase", "bearer", "jwt",
		"session_id", "sessionid", "cookie",
		"certificate", "connection_string", "database_url",
	}

	for _, field := range newFields {
		field := field
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			assert.True(t, IsSensitiveField(field),
				"IsSensitiveField(%q) should return true for v2 field", field)
		})
	}
}
