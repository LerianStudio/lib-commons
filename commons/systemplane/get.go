// Typed read accessors for systemplane Client.
package systemplane

import "time"

// Get returns the current value for the given namespace and key.
// Returns (nil, false) when the Client is nil, not started, or the key is unknown.
func (c *Client) Get(namespace, key string) (any, bool) {
	if c == nil {
		return nil, false
	}

	// TODO(phase-5): look up value from in-memory map
	_ = namespace
	_ = key

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
