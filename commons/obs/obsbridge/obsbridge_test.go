//go:build unit

package obsbridge_test

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v6/commons/obs"
	"github.com/LerianStudio/lib-commons/v6/commons/obs/obsbridge"
	libobs "github.com/LerianStudio/lib-observability/v4"
	liblog "github.com/LerianStudio/lib-observability/v4/log"
	libmetrics "github.com/LerianStudio/lib-observability/v4/metrics"
	"github.com/stretchr/testify/require"
)

// capturingLogger records the events it receives. It is declared with
// universal types only, exactly as a consumer that never imports
// lib-observability would declare it.
type capturingLogger struct {
	msgs []string
}

func (l *capturingLogger) Log(_ context.Context, _ int, msg string, _ ...any) {
	l.msgs = append(l.msgs, msg)
}

func (l *capturingLogger) With(...any) liblog.Logger { return l }

func (l *capturingLogger) WithGroup(string) liblog.Logger { return l }

func (l *capturingLogger) Enabled(int) bool { return true }

func (l *capturingLogger) Sync(context.Context) error { return nil }

// TestLibObservabilityTypesSatisfyObsContracts pins the property that removed
// every adapter from this package: the lib-observability values satisfy the
// commons/obs contracts with no conversion.
func TestLibObservabilityTypesSatisfyObsContracts(t *testing.T) {
	t.Parallel()

	var _ obs.Logger = liblog.NewNop()
	var _ obs.Logger = &liblog.GoLogger{Level: liblog.LevelWarn}
	var _ obs.MetricsRecorder = libmetrics.NewNopFactory()
	var _ obs.Logger = &capturingLogger{}
}

func TestLoggerFromContext_ReturnsContextLogger(t *testing.T) {
	t.Parallel()

	base := &capturingLogger{}
	ctx := libobs.ContextWithLogger(context.Background(), base)

	logger := obsbridge.LoggerFromContext(ctx)
	require.NotNil(t, logger)

	logger.Log(ctx, obs.LevelInfo, "hello")
	require.Equal(t, []string{"hello"}, base.msgs)
}

func TestLoggerFromContext_EmptyContextIsUsable(t *testing.T) {
	t.Parallel()

	logger := obsbridge.LoggerFromContext(context.Background())
	require.NotNil(t, logger)
	require.NotPanics(t, func() { logger.Log(context.Background(), obs.LevelError, "no logger in context") })
	require.NoError(t, logger.Sync(context.Background()))
}

func TestMetricsFromContext_IsSafeToCall(t *testing.T) {
	t.Parallel()

	recorder := obsbridge.MetricsFromContext(context.Background())
	require.NotNil(t, recorder)
	require.NotPanics(t, func() {
		_ = recorder.AddCounter(context.Background(), "c", "d", "1", nil, 1)
		_ = recorder.SetGauge(context.Background(), "g", "d", "1", nil, 1)
		_ = recorder.RecordHistogram(context.Background(), "h", "d", "ms", nil, 1, nil)
	})
}

func TestTrackingFromContext_ReturnsUsableComponents(t *testing.T) {
	t.Parallel()

	base := &capturingLogger{}
	ctx := libobs.ContextWithLogger(context.Background(), base)

	logger, tracer, headerID, recorder := obsbridge.TrackingFromContext(ctx)
	require.NotNil(t, logger)
	require.NotNil(t, tracer)
	require.NotNil(t, recorder)
	require.NotEmpty(t, headerID)

	logger.Log(ctx, obs.LevelInfo, "tracked")
	require.Equal(t, []string{"tracked"}, base.msgs)
}
