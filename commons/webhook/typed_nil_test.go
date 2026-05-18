//go:build unit

package webhook

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type typedNilEndpointLister struct{}

func (*typedNilEndpointLister) ListActiveEndpoints(context.Context) ([]Endpoint, error) {
	return nil, nil
}

type typedNilWebhookLogger struct{}

func (*typedNilWebhookLogger) Log(context.Context, log.Level, string, ...log.Field) {}
func (*typedNilWebhookLogger) With(...log.Field) log.Logger                         { return nil }
func (*typedNilWebhookLogger) WithGroup(string) log.Logger                          { return nil }
func (*typedNilWebhookLogger) Enabled(log.Level) bool                               { return false }
func (*typedNilWebhookLogger) Sync(context.Context) error                           { return nil }

type typedNilDeliveryMetrics struct{}

func (*typedNilDeliveryMetrics) RecordDelivery(context.Context, string, bool, int, int) {}

func TestNewDeliverer_TypedNilListerRejected(t *testing.T) {
	t.Parallel()

	var lister *typedNilEndpointLister

	d := NewDeliverer(lister)

	assert.Nil(t, d)
}

func TestDelivererOptions_TypedNilLoggerAndMetricsIgnored(t *testing.T) {
	t.Parallel()

	var logger *typedNilWebhookLogger
	var metrics *typedNilDeliveryMetrics

	d := NewDeliverer(&typedNilEndpointLister{}, WithLogger(logger), WithMetrics(metrics))
	require.NotNil(t, d)

	assert.NotNil(t, d.logger, "typed-nil logger option must preserve default nop logger")
	assert.Nil(t, d.metrics, "typed-nil metrics option must be ignored")
}

func TestDeliverer_RecordMetrics_TypedNilDefensiveGuard(t *testing.T) {
	t.Parallel()

	var metrics *typedNilDeliveryMetrics
	d := NewDeliverer(&typedNilEndpointLister{})
	require.NotNil(t, d)
	d.metrics = metrics

	assert.NotPanics(t, func() {
		d.recordMetrics(context.Background(), "endpoint-1", true, 200, 1)
	})
}
