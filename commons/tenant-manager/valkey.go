// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package tenantmanager

import (
	"context"
	"fmt"
	"strings"
)

const TenantKeyPrefix = "tenant"

// GetKey returns tenant-prefixed key: "tenant:{tenantID}:{key}"
// If tenantID is empty, returns the key unchanged.
func GetKey(tenantID, key string) string {
	if tenantID == "" {
		return key
	}
	return fmt.Sprintf("%s:%s:%s", TenantKeyPrefix, tenantID, key)
}

// GetKeyFromContext returns tenant-prefixed key using tenantID from context.
// If no tenantID in context, returns the key unchanged.
func GetKeyFromContext(ctx context.Context, key string) string {
	tenantID := GetTenantIDFromContext(ctx)
	return GetKey(tenantID, key)
}

// GetPattern returns pattern for scanning tenant keys: "tenant:{tenantID}:{pattern}"
// If tenantID is empty, returns the pattern unchanged.
func GetPattern(tenantID, pattern string) string {
	if tenantID == "" {
		return pattern
	}
	return fmt.Sprintf("%s:%s:%s", TenantKeyPrefix, tenantID, pattern)
}

// GetPatternFromContext returns pattern using tenantID from context.
// If no tenantID in context, returns the pattern unchanged.
func GetPatternFromContext(ctx context.Context, pattern string) string {
	tenantID := GetTenantIDFromContext(ctx)
	return GetPattern(tenantID, pattern)
}

// StripTenantPrefix removes tenant prefix from key, returns original key.
// If key doesn't have the expected prefix, returns the key unchanged.
func StripTenantPrefix(tenantID, prefixedKey string) string {
	if tenantID == "" {
		return prefixedKey
	}
	prefix := fmt.Sprintf("%s:%s:", TenantKeyPrefix, tenantID)
	return strings.TrimPrefix(prefixedKey, prefix)
}
