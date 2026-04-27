// Typed read accessors for systemplane Client.
package systemplane

import (
	"sort"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// ListEntry is a single entry returned by [Client.List]. It exposes the key
// name, its current effective value (default or override), and the
// human-readable description registered via [WithDescription].
type ListEntry struct {
	Key         string
	Value       any
	Description string
}

// List returns all currently-cached entries in the given namespace, sorted by
// key for deterministic output. Keys registered but never persisted return
// their default values. Safe to call concurrently; nil-safe.
func (c *Client) List(namespace string) []ListEntry {
	if c == nil {
		return nil
	}

	// Collect all registered keys in this namespace.
	c.registryMu.RLock()

	keys := make([]nskey, 0)

	for nk := range c.registry {
		if nk.Namespace == namespace {
			keys = append(keys, nk)
		}
	}

	c.registryMu.RUnlock()

	if len(keys) == 0 {
		return []ListEntry{}
	}

	// Sort by key name for deterministic output.
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].Key < keys[j].Key
	})

	// Build the result from cache (or registry defaults).
	entries := make([]ListEntry, 0, len(keys))

	c.registryMu.RLock()
	c.cacheMu.RLock()

	for _, nk := range keys {
		val, inCache := c.cache[nk]

		def, registered := c.registry[nk]
		if !inCache && registered {
			// Fallback to the registered default.
			val = def.defaultValue
		}

		var desc string
		if registered {
			desc = def.description
		}

		entries = append(entries, ListEntry{Key: nk.Key, Value: val, Description: desc})
	}

	c.cacheMu.RUnlock()
	c.registryMu.RUnlock()

	return entries
}

// KeyDescription returns the human-readable description for a registered key.
// Returns "" for unregistered keys or nil receivers.
func (c *Client) KeyDescription(namespace, key string) string {
	if c == nil {
		return ""
	}

	nk := nskey{Namespace: namespace, Key: key}

	c.registryMu.RLock()
	def, registered := c.registry[nk]
	c.registryMu.RUnlock()

	if !registered {
		return ""
	}

	return def.description
}

// KeyRedaction returns the redaction policy for a registered key. Returns
// [RedactNone] for unregistered keys or nil receivers.
func (c *Client) KeyRedaction(namespace, key string) RedactPolicy {
	if c == nil {
		return RedactNone
	}

	nk := nskey{Namespace: namespace, Key: key}

	c.registryMu.RLock()
	def, registered := c.registry[nk]
	c.registryMu.RUnlock()

	if !registered {
		return RedactNone
	}

	return def.redaction
}

// KeyStatus reports whether a (namespace, key) pair is registered and, if so,
// whether it was registered via [Client.RegisterTenantScoped]. Returns
// (false, false) for nil receivers or unregistered keys. A key registered via
// the legacy [Client.Register] returns (true, false); a tenant-scoped key
// returns (true, true).
//
// Callers such as the admin HTTP surface use this to distinguish "key does not
// exist" (404) from "key exists but is not tenant-scoped" (400) without
// threading new sentinel errors through the write path.
func (c *Client) KeyStatus(namespace, key string) (registered, tenantScoped bool) {
	if c == nil {
		return false, false
	}

	nk := nskey{Namespace: namespace, Key: key}

	c.registryMu.RLock()
	_, registered = c.registry[nk]
	_, tenantScoped = c.tenantScopedRegistry[nk]
	c.registryMu.RUnlock()

	return registered, tenantScoped
}

// Logger returns the logger attached to this Client (via [WithLogger] at
// construction time) or a nop logger when the Client is nil or was
// constructed without a logger. Never returns nil — callers may safely
// invoke methods on the returned logger without a nil check.
//
// This accessor exists so sibling subpackages (notably commons/systemplane/admin)
// can share the Client's configured logger without reintroducing a
// parallel WithLogger option on their own surface.
func (c *Client) Logger() log.Logger {
	if c == nil || c.logger == nil {
		return log.NewNop()
	}

	return c.logger
}

// Get returns the current value for the given namespace and key.
// Returns (nil, false) when the Client is nil, closed, or the key is unregistered.
// If the key is registered but absent from the cache (before Start), the
// registered default is returned.
func (c *Client) Get(namespace, key string) (any, bool) {
	if c == nil {
		return nil, false
	}

	nk := nskey{Namespace: namespace, Key: key}

	// Try the cache first (populated after Start).
	c.cacheMu.RLock()
	v, inCache := c.cache[nk]
	c.cacheMu.RUnlock()

	if inCache {
		return v, true
	}

	// Fallback to the registered default (before Start or if cache was never populated).
	c.registryMu.RLock()
	def, registered := c.registry[nk]
	c.registryMu.RUnlock()

	if registered {
		return def.defaultValue, true
	}

	return nil, false
}

// GetString returns the current value as a string.
// Returns "" when the Client is nil or the key is not found.
func (c *Client) GetString(namespace, key string) string {
	v, ok := c.Get(namespace, key)
	if !ok {
		return ""
	}

	s, _ := v.(string)

	return s
}

// GetInt returns the current value as an int.
// Returns 0 when the Client is nil or the key is not found.
func (c *Client) GetInt(namespace, key string) int {
	v, ok := c.Get(namespace, key)
	if !ok {
		return 0
	}

	// JSON numbers decode as float64; handle both int and float64.
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

// GetBool returns the current value as a bool.
// Returns false when the Client is nil or the key is not found.
func (c *Client) GetBool(namespace, key string) bool {
	v, ok := c.Get(namespace, key)
	if !ok {
		return false
	}

	b, _ := v.(bool)

	return b
}

// GetFloat64 returns the current value as a float64.
// Returns 0 when the Client is nil or the key is not found.
func (c *Client) GetFloat64(namespace, key string) float64 {
	v, ok := c.Get(namespace, key)
	if !ok {
		return 0
	}

	f, _ := v.(float64)

	return f
}

// GetDuration returns the current value as a [time.Duration].
// It supports both string values parseable by [time.ParseDuration] and
// numeric values interpreted as nanoseconds.
// Returns 0 when the Client is nil or the key is not found.
func (c *Client) GetDuration(namespace, key string) time.Duration {
	v, ok := c.Get(namespace, key)
	if !ok {
		return 0
	}

	switch d := v.(type) {
	case time.Duration:
		return d
	case string:
		parsed, err := time.ParseDuration(d)
		if err != nil {
			return 0
		}

		return parsed
	case float64:
		return time.Duration(int64(d))
	default:
		return 0
	}
}
