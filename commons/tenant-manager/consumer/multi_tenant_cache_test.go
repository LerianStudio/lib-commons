// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/tenantcache"
	"github.com/stretchr/testify/assert"
)

func TestDefaultTenantCacheTTL_BackwardCompat(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 12*time.Hour, DefaultTenantCacheTTL, "DefaultTenantCacheTTL should be 12 hours")
	assert.Equal(t, tenantcache.DefaultTenantCacheTTL, DefaultTenantCacheTTL,
		"consumer.DefaultTenantCacheTTL should match tenantcache.DefaultTenantCacheTTL")
}

func TestDefaultMultiTenantConfig_TenantCacheTTL(t *testing.T) {
	t.Parallel()
	cfg := DefaultMultiTenantConfig()
	assert.Equal(t, DefaultTenantCacheTTL, cfg.TenantCacheTTL,
		"DefaultMultiTenantConfig should set TenantCacheTTL to DefaultTenantCacheTTL")
}
