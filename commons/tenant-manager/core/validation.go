package core

// MaxTenantIDLength is the maximum allowed length for a tenant ID.
const MaxTenantIDLength = 256

// IsValidTenantID validates a tenant ID against security constraints.
// Valid tenant IDs must be non-empty, at most MaxTenantIDLength characters,
// start with an ASCII alphanumeric character, and contain only ASCII
// alphanumerics, hyphens, or underscores thereafter.
//
// This is the hand-rolled equivalent of the regular expression
// `^[a-zA-Z0-9][a-zA-Z0-9_-]*$`. The byte-loop form is retained instead of a
// compiled regexp because IsValidTenantID sits on the systemplane tenant
// read hot path (extractTenantID → AC15 sub-microsecond target) and the
// onePass-DFA regex walker accounted for ~60 % of total CPU in the
// benchmark profile. The set is pure ASCII, so there is no Unicode
// correctness trade-off and test coverage in
// commons/tenant-manager/consumer/multi_tenant_test.go#TestIsValidTenantID
// continues to pin behavior.
func IsValidTenantID(id string) bool {
	n := len(id)
	if n == 0 || n > MaxTenantIDLength {
		return false
	}

	// First byte must be ASCII alphanumeric (no leading '-' or '_').
	if !isTenantIDAlnum(id[0]) {
		return false
	}

	for i := 1; i < n; i++ {
		c := id[i]
		if !isTenantIDAlnum(c) && c != '-' && c != '_' {
			return false
		}
	}

	return true
}

// isTenantIDAlnum reports whether c is an ASCII alphanumeric byte.
// Split out so the compiler can inline it at both call sites above.
func isTenantIDAlnum(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
