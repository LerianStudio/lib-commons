// Register and key definition management for systemplane Client.
package systemplane

import (
	"fmt"
	"strings"
)

// reservedKey is a key name that is forbidden from being registered because
// it collides with an admin-surface path segment. See validateKeyArgs.
//
// The admin package mounts tenant-scoped routes at
// /<prefix>/:namespace/:key/tenants and /<prefix>/:namespace/:key/tenants/:tenantID.
// A registered key literally named "tenants" would shadow those admin
// endpoints from the router's perspective. Rejecting at registration time
// is a cheap preemption and keeps the admin surface unambiguous.
const reservedKey = "tenants"

// unitSeparator is the U+001F control character used as the composite-key
// delimiter by singleflightKey (see tenant_scoped.go). A namespace or key
// containing U+001F could collide with another (namespace, key) tuple in
// singleflight slots. Rejecting at registration time is a 3-line guard
// against a class of silent misrouting bugs.
const unitSeparator = "\x1f"

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

// validateKeyArgs enforces the shape constraints the Client relies on when
// routing a (namespace, key) through its internal caches, the admin HTTP
// surface, and the singleflight coalescing layer.
//
// Rules:
//   - Neither namespace nor key may be empty.
//   - Key must not equal "tenants" (reserved: the admin surface mounts
//     /<prefix>/:namespace/:key/tenants and /<prefix>/:namespace/:key/tenants/:tenantID;
//     a literal "tenants" key would shadow those routes).
//   - Neither namespace nor key may contain the U+001F (Unit Separator)
//     control character, which singleflightKey uses as a composite-key
//     delimiter. Allowing it would let two distinct (namespace, key) tuples
//     collide on the same singleflight slot.
func validateKeyArgs(namespace, key string) error {
	if namespace == "" || key == "" {
		return fmt.Errorf("%w: namespace and key must be non-empty", ErrValidation)
	}

	if key == reservedKey {
		return fmt.Errorf("%w: key %q is reserved for admin routing", ErrValidation, reservedKey)
	}

	if strings.Contains(namespace, unitSeparator) || strings.Contains(key, unitSeparator) {
		return fmt.Errorf("%w: namespace/key must not contain the U+001F information-separator-one character (reserved as the singleflight composite-key delimiter)", ErrValidation)
	}

	return nil
}
