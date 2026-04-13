// Register and key definition management for systemplane Client.
package systemplane

// keyDef holds the metadata and default value for a registered configuration key.
type keyDef struct {
	namespace    string
	key          string
	defaultValue any
	description  string
	validator    func(any) error
	redaction    RedactPolicy
}

// compositeKey builds the map key used for internal lookups.
func compositeKey(namespace, key string) string {
	return namespace + "\x00" + key
}

// Register declares a configuration key with its default value and optional
// validators. Must be called before [Client.Start]; returns
// [ErrRegisterAfterStart] otherwise.
//
// Namespace is a free-text label such as "global", "tenant:acme", or
// "feature-flags". Key is the setting name within that namespace.
func (c *Client) Register(namespace, key string, defaultValue any, opts ...KeyOption) error {
	if c == nil {
		return ErrClosed
	}

	// TODO(phase-5): implement registration with state-machine check
	_ = namespace
	_ = key
	_ = defaultValue

	for range opts {
		// options will be applied in phase-5
	}

	return nil
}

// validateKeyArgs checks that namespace and key are non-empty.
func validateKeyArgs(namespace, key string) error {
	if namespace == "" || key == "" {
		return ErrUnknownKey
	}

	return nil
}
