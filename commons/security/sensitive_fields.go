package security

import (
	"strings"
	"sync"
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
	"accesstoken",
	"refresh_token",
	"refreshtoken",
	"private_key",
	"privatekey",
	"clientid",
	"client_id",
	"clientsecret",
	"client_secret",
}

var (
	sensitiveFieldsMapOnce sync.Once
	sensitiveFieldsMap     map[string]bool
)

func DefaultSensitiveFields() []string {
	return defaultSensitiveFields
}

// DefaultSensitiveFieldsMap provides a cached map version of DefaultSensitiveFields
// for efficient lookup operations. All field names are lowercase for
// case-insensitive matching. The map is initialized only once.
func DefaultSensitiveFieldsMap() map[string]bool {
	sensitiveFieldsMapOnce.Do(func() {
		sensitiveFieldsMap = make(map[string]bool, len(defaultSensitiveFields))
		for _, field := range defaultSensitiveFields {
			sensitiveFieldsMap[field] = true
		}
	})

	return sensitiveFieldsMap
}

// IsSensitiveField checks if a field name is considered sensitive based on
// the default sensitive fields list. The check is case-insensitive and uses
// substring matching to catch variations like "user_password", "api-key", etc.
func IsSensitiveField(fieldName string) bool {
	lowerField := strings.ToLower(fieldName)

	if DefaultSensitiveFieldsMap()[lowerField] {
		return true
	}

	for _, sensitive := range defaultSensitiveFields {
		if strings.Contains(lowerField, sensitive) {
			return true
		}
	}

	return false
}
