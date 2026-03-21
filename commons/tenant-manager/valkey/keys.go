// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package valkey

import (
	"context"
	"fmt"
	"strings"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
)

const TenantKeyPrefix = "tenant"

// GetKey returns tenant-prefixed key: "tenant:{tenantID}:{key}"
// If tenantID is empty, returns the key unchanged.
// Returns an error if tenantID contains the delimiter character ":"
// which would corrupt the key namespace structure.
func GetKey(tenantID, key string) (string, error) {
	if tenantID == "" {
		return key, nil
	}

	if strings.Contains(tenantID, ":") {
		return "", fmt.Errorf("tenantID must not contain delimiter character ':': %q", tenantID)
	}

	return fmt.Sprintf("%s:%s:%s", TenantKeyPrefix, tenantID, key), nil
}

// GetKeyFromContext returns tenant-prefixed key using tenantID from context.
// If no tenantID in context, returns the key unchanged.
// If ctx is nil, returns the key unchanged (no tenant prefix).
// Returns an error if the tenantID from context contains the delimiter character ":".
func GetKeyFromContext(ctx context.Context, key string) (string, error) {
	if ctx == nil {
		return GetKey("", key)
	}

	tenantID := core.GetTenantIDFromContext(ctx)

	return GetKey(tenantID, key)
}

// GetPattern returns pattern for scanning tenant keys: "tenant:{tenantID}:{pattern}"
// If tenantID is empty, returns the pattern unchanged.
// Returns an error if tenantID contains the delimiter character ":".
func GetPattern(tenantID, pattern string) (string, error) {
	if tenantID == "" {
		return pattern, nil
	}

	if strings.Contains(tenantID, ":") {
		return "", fmt.Errorf("tenantID must not contain delimiter character ':': %q", tenantID)
	}

	return fmt.Sprintf("%s:%s:%s", TenantKeyPrefix, tenantID, pattern), nil
}

// GetPatternFromContext returns pattern using tenantID from context.
// If no tenantID in context, returns the pattern unchanged.
// If ctx is nil, returns the pattern unchanged (no tenant prefix).
// Returns an error if the tenantID from context contains the delimiter character ":".
func GetPatternFromContext(ctx context.Context, pattern string) (string, error) {
	if ctx == nil {
		return GetPattern("", pattern)
	}

	tenantID := core.GetTenantIDFromContext(ctx)

	return GetPattern(tenantID, pattern)
}

// StripTenantPrefix removes tenant prefix from key, returns original key.
// If key doesn't have the expected prefix, returns the key unchanged.
// Returns an error if tenantID contains the delimiter character ":".
func StripTenantPrefix(tenantID, prefixedKey string) (string, error) {
	if tenantID == "" {
		return prefixedKey, nil
	}

	if strings.Contains(tenantID, ":") {
		return "", fmt.Errorf("tenantID must not contain delimiter character ':': %q", tenantID)
	}

	prefix := fmt.Sprintf("%s:%s:", TenantKeyPrefix, tenantID)

	return strings.TrimPrefix(prefixedKey, prefix), nil
}
