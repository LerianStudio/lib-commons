// Copyright 2025 Lerian Studio.

package dlq

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	libLog "github.com/LerianStudio/lib-commons/v5/commons/log"
	tmcore "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/redis/go-redis/v9"
)

// tenantScopedKey constructs a Redis key including the tenant ID from context.
// With tenant:    "dlq:tenant-abc:outbound"
// Without tenant: "dlq:outbound"
func (h *Handler) tenantScopedKey(ctx context.Context, source string) string {
	return h.tenantScopedKeyForTenant(tmcore.GetTenantIDContext(ctx), source)
}

func (h *Handler) tenantScopedKeyForTenant(tenantID, source string) string {
	if tenantID != "" {
		if err := validateKeySegment("tenantID", tenantID); err != nil {
			// Fail closed: an invalid tenant ID must not silently fall back
			// to the global key, which would mix tenant-scoped messages into
			// the global queue. Return an empty string so callers can detect
			// the failure and skip the operation.
			h.logger.Log(context.Background(), libLog.LevelError, "dlq: tenantScopedKeyForTenant: invalid tenantID, rejecting operation",
				libLog.String("tenant_id", tenantID),
				libLog.Err(err),
			)

			return ""
		}

		return fmt.Sprintf("%s%s:%s", h.keyPrefix, tenantID, source)
	}

	return fmt.Sprintf("%s%s", h.keyPrefix, source)
}

// ExtractTenantFromKey extracts the tenant ID from a tenant-scoped Redis key.
// Given key="dlq:tenant-abc:outbound" and keyPrefix="dlq:", returns "tenant-abc".
// Returns empty string if the key does not match the expected format.
func (h *Handler) ExtractTenantFromKey(key, source string) string {
	if h == nil {
		return ""
	}

	prefix := h.keyPrefix
	suffix := ":" + source

	if len(key) <= len(prefix)+len(suffix) {
		return ""
	}

	if key[:len(prefix)] != prefix {
		return ""
	}

	if key[len(key)-len(suffix):] != suffix {
		return ""
	}

	return key[len(prefix) : len(key)-len(suffix)]
}

// validateKeySegment ensures that a Redis key segment (source or tenantID)
// does not contain characters that would corrupt key patterns or enable
// injection into SCAN glob patterns. Backslash is included because it is
// the escape character in Redis SCAN patterns.
func validateKeySegment(name, value string) error {
	for _, c := range value {
		if c == ':' || c == '*' || c == '?' || c == '[' || c == ']' || c == '\\' {
			return fmt.Errorf("dlq: %s %q contains disallowed character %q", name, value, c)
		}
	}

	return nil
}

// truncateString returns s unchanged when its rune count is within maxLen,
// otherwise it truncates at the maxLen-th rune boundary and appends "..."
// to signal that content was trimmed. Rune-aware truncation prevents
// splitting multi-byte UTF-8 sequences, which would produce invalid output.
// Used by logEnqueueFallback to prevent large PII-containing error messages
// from leaking into log aggregators.
func truncateString(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}

	// Walk runes and cut at the correct byte offset.
	byteLen := 0
	count := 0

	for _, r := range s {
		if count == maxLen {
			break
		}

		byteLen += utf8.RuneLen(r)
		count++
	}

	return s[:byteLen] + "..."
}

// isRedisNilError reports whether err wraps redis.Nil. The Handler uses
// fmt.Errorf("%w") so errors.Is unwraps correctly through the chain.
func isRedisNilError(err error) bool {
	return errors.Is(err, redis.Nil)
}
