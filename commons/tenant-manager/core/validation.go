package core

import "regexp"

// MaxTenantIDLength is the maximum allowed length for a tenant ID.
const MaxTenantIDLength = 256

// validTenantIDPattern enforces a character whitelist for tenant IDs.
// Only alphanumeric characters, hyphens, and underscores are allowed.
// The first character must be alphanumeric.
var validTenantIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// IsValidTenantID validates a tenant ID against security constraints.
// Valid tenant IDs must be non-empty, at most MaxTenantIDLength characters,
// and match validTenantIDPattern.
func IsValidTenantID(id string) bool {
	if id == "" || len(id) > MaxTenantIDLength {
		return false
	}

	return validTenantIDPattern.MatchString(id)
}
