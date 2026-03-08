// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package s3

import (
	"context"
	"fmt"
	"strings"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
)

// GetObjectStorageKey returns a tenant-prefixed object storage key: "{tenantID}/{key}".
// If tenantID is empty, returns the key with leading slashes stripped (normalized).
// Leading slashes are always stripped from the key to ensure clean path construction,
// regardless of whether tenantID is present.
// Returns an error if tenantID contains the path delimiter "/" which would create
// ambiguous object storage paths or enable path traversal.
func GetObjectStorageKey(tenantID, key string) (string, error) {
	key = strings.TrimLeft(key, "/")

	if tenantID == "" {
		return key, nil
	}

	tenantID = strings.Trim(tenantID, "/")

	if tenantID == "" {
		return key, nil
	}

	if strings.Contains(tenantID, "/") {
		return "", fmt.Errorf("tenantID must not contain path delimiter '/': %q", tenantID)
	}

	return tenantID + "/" + key, nil
}

// GetObjectStorageKeyForTenant returns a tenant-prefixed object storage key
// using the tenantID from context.
//
// In multi-tenant mode (tenantID in context): "{tenantId}/{key}"
// In single-tenant mode (no tenant in context): "{key}" (normalized, leading slashes stripped)
//
// If ctx is nil, behaves as single-tenant mode (no prefix).
// Returns an error if the tenantID from context contains the path delimiter "/".
//
// Usage:
//
//	key, err := s3.GetObjectStorageKeyForTenant(ctx, "reports/templateID/reportID.html")
//	// Multi-tenant: "org_01ABC.../reports/templateID/reportID.html"
//	// Single-tenant: "reports/templateID/reportID.html"
//	storage.Upload(ctx, key, reader, contentType)
func GetObjectStorageKeyForTenant(ctx context.Context, key string) (string, error) {
	if ctx == nil {
		return GetObjectStorageKey("", key)
	}

	tenantID := core.GetTenantIDFromContext(ctx)

	return GetObjectStorageKey(tenantID, key)
}

// StripObjectStoragePrefix removes the tenant prefix from an object storage key,
// returning the original key. If the key doesn't have the expected prefix,
// returns the key unchanged.
// Returns an error if tenantID contains the path delimiter "/".
func StripObjectStoragePrefix(tenantID, prefixedKey string) (string, error) {
	if tenantID == "" {
		return prefixedKey, nil
	}

	tenantID = strings.Trim(tenantID, "/")

	if tenantID == "" {
		return prefixedKey, nil
	}

	if strings.Contains(tenantID, "/") {
		return "", fmt.Errorf("tenantID must not contain path delimiter '/': %q", tenantID)
	}

	prefix := tenantID + "/"

	return strings.TrimPrefix(prefixedKey, prefix), nil
}
