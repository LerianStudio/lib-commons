// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package core

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ErrInvalidTenantIDFormat is returned by CanonicalTenantID when the tenant ID
// does not satisfy IsValidTenantID.
var ErrInvalidTenantIDFormat = errors.New("tenant ID has invalid format")

// CanonicalTenantID normalizes a tenant ID to the single form used for every
// tenant-keyed namespace: cache keys, connection-pool keys and Secrets Manager
// paths.
//
// UUID tenant IDs are returned dashless and lowercase, the platform convention
// already applied by the Tenant Manager event publisher and by
// commons/secretsmanager.BuildModuleKafkaSecretPath. The 36-character dashed
// form and the 32-character dashless form therefore collapse onto one key.
//
// Non-UUID tenant IDs are supported and returned verbatim with case preserved:
// slugs such as "tenant-123-abc" are valid per IsValidTenantID and have no
// canonical UUID form.
//
// Input that fails IsValidTenantID is rejected with ErrInvalidTenantIDFormat,
// including the "urn:uuid:" and braced UUID spellings uuid.Parse accepts.
func CanonicalTenantID(id string) (string, error) {
	if !IsValidTenantID(id) {
		return "", fmt.Errorf("%w: %q", ErrInvalidTenantIDFormat, id)
	}

	if parsed, err := uuid.Parse(id); err == nil {
		return hex.EncodeToString(parsed[:]), nil
	}

	return id, nil
}
