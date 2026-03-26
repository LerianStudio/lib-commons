//go:build unit

package configfetch

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestTenantConfig_NilClient(t *testing.T) {
	t.Parallel()

	logger := logcompat.New(nil)
	span := noop.Span{}

	cfg, err := TenantConfig(context.Background(), nil, "tenant-1", "ledger", logger, span)

	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "tenant manager client is nil")
}
