package systemplane

// RedactPolicy controls how a key's value is rendered in admin endpoints and logs.
type RedactPolicy int

const (
	// RedactNone leaves the value visible as-is.
	RedactNone RedactPolicy = iota

	// RedactMask replaces the value with a fixed mask string (e.g. "****").
	RedactMask

	// RedactFull hides the value entirely, replacing it with "[REDACTED]".
	RedactFull
)

// ApplyRedaction returns the value rendered per policy. Used by admin handlers
// and structured logging to prevent sensitive values from leaking.
func ApplyRedaction(value any, policy RedactPolicy) any {
	switch policy {
	case RedactMask:
		return "****"
	case RedactFull:
		return "[REDACTED]"
	default:
		return value
	}
}
