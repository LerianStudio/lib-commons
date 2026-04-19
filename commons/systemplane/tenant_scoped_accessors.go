// Typed accessor mirrors for tenant-scoped reads.
//
// These accessors forward to GetForTenant and apply the same type-assertion
// rules as the legacy typed accessors in get.go:149-232 (GetString / GetInt /
// GetBool / GetFloat64 / GetDuration). The critical distinction from their
// legacy counterparts: when GetForTenant returns an error (missing ctx,
// invalid tenant ID, unregistered key, non-tenant-scoped key), these methods
// surface the error instead of silently collapsing to a zero value. That is
// decision D8 — "Missing tenant" is not a valid read state for a tenant-
// scoped key, so a silent zero return would mask a consumer bug.
//
// Type mismatch (value exists but cannot be asserted to the requested type)
// is reported as ErrValidation — a configuration issue, not a runtime
// failure. The legacy accessors return a zero value on mismatch; we
// intentionally deviate so tenant-scoped code paths get a loud signal.
//
// # Nil-receiver safety
//
// Each accessor forwards to GetForTenant as its first operation. GetForTenant
// guards against c == nil (returning ErrClosed), so a nil-receiver call on
// any accessor below inherits that guard transitively: the accessor returns
// its zero value and a wrapped ErrClosed without ever dereferencing c.
// Maintainers modifying these accessors MUST preserve that property — any
// field access on c before the GetForTenant call would regress the
// nil-safety contract the Client doc promises.
package systemplane

import (
	"context"
	"fmt"
	"time"
)

// GetStringForTenant returns the current tenant-scoped value as a string.
// Returns ("", err) when GetForTenant fails or the value is not a string.
func (c *Client) GetStringForTenant(ctx context.Context, namespace, key string) (string, error) {
	v, _, err := c.GetForTenant(ctx, namespace, key)
	if err != nil {
		return "", err
	}

	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%w: value at %s/%s is not a string (type %T)", ErrValidation, namespace, key, v)
	}

	return s, nil
}

// GetIntForTenant returns the current tenant-scoped value as an int.
// JSON numbers decode as float64, so this accessor transparently accepts
// both int and float64 backing types (matching the legacy GetInt at
// get.go:164-178). Returns (0, err) on any failure.
func (c *Client) GetIntForTenant(ctx context.Context, namespace, key string) (int, error) {
	v, _, err := c.GetForTenant(ctx, namespace, key)
	if err != nil {
		return 0, err
	}

	switch n := v.(type) {
	case int:
		return n, nil
	case float64:
		return int(n), nil
	default:
		return 0, fmt.Errorf("%w: value at %s/%s is not an int (type %T)", ErrValidation, namespace, key, v)
	}
}

// GetBoolForTenant returns the current tenant-scoped value as a bool.
// Returns (false, err) when GetForTenant fails or the value is not a bool.
func (c *Client) GetBoolForTenant(ctx context.Context, namespace, key string) (bool, error) {
	v, _, err := c.GetForTenant(ctx, namespace, key)
	if err != nil {
		return false, err
	}

	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%w: value at %s/%s is not a bool (type %T)", ErrValidation, namespace, key, v)
	}

	return b, nil
}

// GetFloat64ForTenant returns the current tenant-scoped value as a float64.
// Returns (0, err) when GetForTenant fails or the value is not a float64.
func (c *Client) GetFloat64ForTenant(ctx context.Context, namespace, key string) (float64, error) {
	v, _, err := c.GetForTenant(ctx, namespace, key)
	if err != nil {
		return 0, err
	}

	f, ok := v.(float64)
	if !ok {
		return 0, fmt.Errorf("%w: value at %s/%s is not a float64 (type %T)", ErrValidation, namespace, key, v)
	}

	return f, nil
}

// GetDurationForTenant returns the current tenant-scoped value as a
// time.Duration. Supports string values parseable by time.ParseDuration,
// time.Duration directly, and numeric values interpreted as nanoseconds
// (mirroring GetDuration at get.go:211-232).
// Returns (0, err) on any failure.
func (c *Client) GetDurationForTenant(ctx context.Context, namespace, key string) (time.Duration, error) {
	v, _, err := c.GetForTenant(ctx, namespace, key)
	if err != nil {
		return 0, err
	}

	switch d := v.(type) {
	case time.Duration:
		return d, nil
	case string:
		parsed, err := time.ParseDuration(d)
		if err != nil {
			return 0, fmt.Errorf("%w: value at %s/%s is not a valid duration: %w", ErrValidation, namespace, key, err)
		}

		return parsed, nil
	case float64:
		return time.Duration(int64(d)), nil
	default:
		return 0, fmt.Errorf("%w: value at %s/%s is not a duration (type %T)", ErrValidation, namespace, key, v)
	}
}
