package configfetch

import (
	"context"
	"errors"
	"fmt"

	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	"go.opentelemetry.io/otel/trace"
)

func TenantConfig(
	ctx context.Context,
	tmClient *client.Client,
	tenantID, service string,
	logger *logcompat.Logger,
	span trace.Span,
) (*core.TenantConfig, error) {
	config, err := tmClient.GetTenantConfig(ctx, tenantID, service)
	if err == nil {
		return config, nil
	}

	var suspended *core.TenantSuspendedError
	if errors.As(err, &suspended) {
		logger.WarnCtx(ctx, fmt.Sprintf("tenant service is %s: tenantID=%s", suspended.Status, tenantID))
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "tenant service suspended", err)

		return nil, err
	}

	logger.ErrorCtx(ctx, fmt.Sprintf("failed to get tenant config: %v", err))
	libOpentelemetry.HandleSpanError(span, "failed to get tenant config", err)

	return nil, fmt.Errorf("failed to get tenant config: %w", err)
}
