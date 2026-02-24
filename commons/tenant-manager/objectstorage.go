// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package tenantmanager

import (
	"context"
	"strings"
)

// GetObjectStorageKey returns a tenant-prefixed object storage key: "{tenantID}/{key}".
// If tenantID is empty, returns the key unchanged (single-tenant mode).
// Leading slashes are stripped from the key to ensure clean path construction.
func GetObjectStorageKey(tenantID, key string) string {
	key = strings.TrimLeft(key, "/")

	if tenantID == "" {
		return key
	}

	return tenantID + "/" + key
}

// GetObjectStorageKeyForTenant returns a tenant-prefixed object storage key
// using the tenantID from context.
//
// In multi-tenant mode (tenantID in context): "{tenantId}/{key}"
// In single-tenant mode (no tenant in context): "{key}" (unchanged)
//
// Usage:
//
//	key := tenantmanager.GetObjectStorageKeyForTenant(ctx, "reports/templateID/reportID.html")
//	// Multi-tenant: "org_01ABC.../reports/templateID/reportID.html"
//	// Single-tenant: "reports/templateID/reportID.html"
//	storage.Upload(ctx, key, reader, contentType)
func GetObjectStorageKeyForTenant(ctx context.Context, key string) string {
	tenantID := GetTenantIDFromContext(ctx)
	return GetObjectStorageKey(tenantID, key)
}

// StripObjectStoragePrefix removes the tenant prefix from an object storage key,
// returning the original key. If the key doesn't have the expected prefix,
// returns the key unchanged.
func StripObjectStoragePrefix(tenantID, prefixedKey string) string {
	if tenantID == "" {
		return prefixedKey
	}

	prefix := tenantID + "/"

	return strings.TrimPrefix(prefixedKey, prefix)
}
