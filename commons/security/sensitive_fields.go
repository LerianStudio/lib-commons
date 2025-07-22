package security

import (
	"strings"
)

var defaultSensitiveFields = []string{
	"password",
	"newpassword",
	"oldpassword",
	"passwordsalt",
	"token",
	"secret",
	"key",
	"authorization",
	"auth",
	"credential",
	"credentials",
	"apikey",
	"api_key",
	"access_token",
	"refresh_token",
	"private_key",
	"privatekey",
	"clientid",
	"client_id",
	"clientsecret",
	"client_secret",
}

func DefaultSensitiveFields() []string {
	return defaultSensitiveFields
}

// DefaultSensitiveFieldsMap provides a map version of DefaultSensitiveFields
// for efficient lookup operations. All field names are lowercase for
// case-insensitive matching.
func DefaultSensitiveFieldsMap() map[string]bool {
	fieldMap := make(map[string]bool, len(defaultSensitiveFields))
	for _, field := range defaultSensitiveFields {
		fieldMap[field] = true
	}
	return fieldMap
}

// IsSensitiveField checks if a field name is considered sensitive based on
// the default sensitive fields list. The check is case-insensitive.
func IsSensitiveField(fieldName string) bool {
	return DefaultSensitiveFieldsMap()[strings.ToLower(fieldName)]
}
