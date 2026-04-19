// Register and key definition management for systemplane Client.
package systemplane

import "fmt"

// keyDef holds the metadata and default value for a registered configuration key.
type keyDef struct {
	defaultValue any
	description  string
	validator    func(any) error
	redaction    RedactPolicy
}

// Register declares a configuration key with its default value and optional
// validators. Must be called before [Client.Start]; returns
// [ErrRegisterAfterStart] otherwise.
//
// Namespace is a free-text label such as "global", "tenant:acme", or
// "feature-flags". Key is the setting name within that namespace.
//
// If a Validator is provided (via [WithValidator]), it is run against the
// defaultValue at registration time; the call returns [ErrValidation] if the
// default fails validation (a broken default would cause silent misbehavior).
//
// Registering the same (namespace, key) pair twice returns [ErrDuplicateKey].
//
// # Mutable defaults
//
// Avoid mutable defaults (slices, maps, pointers to shared state). The
// registered default is held by reference: when Get falls through to the
// default (no persisted override yet), subsequent readers see whatever
// mutations earlier callers applied. Prefer value types, or wrap
// slices/maps in a defensive copy the caller owns. This caveat is shared
// with [Client.RegisterTenantScoped], where the blast radius is wider
// (every tenant that falls through to the default).
func (c *Client) Register(namespace, key string, defaultValue any, opts ...KeyOption) error {
	if c == nil || c.closed.Load() {
		return ErrClosed
	}

	if c.started.Load() {
		return ErrRegisterAfterStart
	}

	if err := validateKeyArgs(namespace, key); err != nil {
		return err
	}

	nk := nskey{Namespace: namespace, Key: key}

	// Build the key definition from defaults + options.
	def := keyDef{
		defaultValue: defaultValue,
		redaction:    RedactNone,
	}

	for _, o := range opts {
		o(&def)
	}

	// Validate the default value if a validator is set.
	if def.validator != nil {
		if err := def.validator(defaultValue); err != nil {
			return fmt.Errorf("%w: default value rejected: %w", ErrValidation, err)
		}
	}

	c.registryMu.Lock()
	defer c.registryMu.Unlock()

	if _, exists := c.registry[nk]; exists {
		return fmt.Errorf("%w: %s/%s", ErrDuplicateKey, namespace, key)
	}

	c.registry[nk] = def

	return nil
}

// validateKeyArgs checks that namespace and key are non-empty.
func validateKeyArgs(namespace, key string) error {
	if namespace == "" || key == "" {
		return fmt.Errorf("%w: namespace and key must be non-empty", ErrValidation)
	}

	return nil
}
