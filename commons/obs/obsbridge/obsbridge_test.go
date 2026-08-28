//go:build unit

package obsbridge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/LerianStudio/lib-commons/v6/commons/obs"
	"github.com/LerianStudio/lib-commons/v6/commons/obs/obsbridge"
	liblog "github.com/LerianStudio/lib-observability/v2/log"
	libmetrics "github.com/LerianStudio/lib-observability/v2/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingLibLogger records the typed lib-observability calls the adapter makes.
type capturingLibLogger struct {
	levels   []liblog.Level
	messages []string
	fields   [][]liblog.Field
	enabled  bool
	syncErr  error
}

func (l *capturingLibLogger) Log(_ context.Context, level liblog.Level, msg string, fields ...liblog.Field) {
	l.levels = append(l.levels, level)
	l.messages = append(l.messages, msg)
	l.fields = append(l.fields, fields)
}

func (l *capturingLibLogger) With(...liblog.Field) liblog.Logger { return l }

func (l *capturingLibLogger) WithGroup(string) liblog.Logger { return l }

func (l *capturingLibLogger) Enabled(liblog.Level) bool { return l.enabled }

func (l *capturingLibLogger) Sync(context.Context) error { return l.syncErr }

func TestLogger_SatisfiesTheObsContract(t *testing.T) {
	t.Parallel()

	var _ obs.Logger = obsbridge.Logger(liblog.NewNop())
}

func TestLogger_NilYieldsNop(t *testing.T) {
	t.Parallel()

	logger := obsbridge.Logger(nil)
	require.NotNil(t, logger)
	assert.False(t, logger.Enabled(obs.LevelError))
	assert.NoError(t, logger.Sync(context.Background()))
}

func TestLogger_MapsLevels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		level int
		want  liblog.Level
	}{
		{name: "error", level: obs.LevelError, want: liblog.LevelError},
		{name: "warn", level: obs.LevelWarn, want: liblog.LevelWarn},
		{name: "info", level: obs.LevelInfo, want: liblog.LevelInfo},
		{name: "debug", level: obs.LevelDebug, want: liblog.LevelDebug},
		{name: "out of range clamps to the most severe", level: 99, want: liblog.LevelError},
		{name: "negative clamps to the most severe", level: -1, want: liblog.LevelError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base := &capturingLibLogger{}
			obsbridge.Logger(base).Log(context.Background(), tt.level, "msg")

			require.Len(t, base.levels, 1)
			assert.Equal(t, tt.want, base.levels[0])
		})
	}
}

func TestLogger_ConvertsKeyValuePairsToTypedFields(t *testing.T) {
	t.Parallel()

	base := &capturingLibLogger{}
	failure := errors.New("boom")

	obsbridge.Logger(base).Log(
		context.Background(), obs.LevelWarn, "msg",
		"str", "value", "int", 7, "bool", true, "error", failure, "orphan",
	)

	require.Len(t, base.fields, 1)
	assert.Equal(t, []liblog.Field{
		liblog.String("str", "value"),
		liblog.Int("int", 7),
		liblog.Bool("bool", true),
		liblog.Any("error", failure),
		liblog.Any("orphan", nil),
	}, base.fields[0])
}

func TestLogger_NilContextIsReplaced(t *testing.T) {
	t.Parallel()

	base := &capturingLibLogger{}

	assert.NotPanics(t, func() {
		obsbridge.Logger(base).Log(nil, obs.LevelInfo, "msg") //nolint:staticcheck // nil ctx is the case under test
	})
	assert.Len(t, base.messages, 1)
}

func TestLogger_DelegatesEnabledAndSync(t *testing.T) {
	t.Parallel()

	base := &capturingLibLogger{enabled: true, syncErr: errors.New("flush failed")}
	logger := obsbridge.Logger(base)

	assert.True(t, logger.Enabled(obs.LevelDebug))
	assert.ErrorIs(t, logger.Sync(context.Background()), base.syncErr)
}

func TestMetrics_SatisfiesTheObsContract(t *testing.T) {
	t.Parallel()

	var _ obs.MetricsRecorder = obsbridge.Metrics(libmetrics.NewNopFactory())
}

func TestMetrics_NilFactoryIsSafeToCall(t *testing.T) {
	t.Parallel()

	recorder := obsbridge.Metrics(nil)
	require.NotNil(t, recorder)

	ctx := context.Background()
	attrs := map[string]string{"service": "ledger"}

	assert.NoError(t, recorder.AddCounter(ctx, "requests", "desc", "1", attrs, 1))
	assert.NoError(t, recorder.SetGauge(ctx, "depth", "desc", "1", attrs, 3))
	assert.NoError(t, recorder.RecordHistogram(ctx, "latency", "desc", "ms", attrs, 12.5, []float64{1, 5, 10}))
}

func TestMetrics_NilAttributesAreAccepted(t *testing.T) {
	t.Parallel()

	recorder := obsbridge.Metrics(libmetrics.NewNopFactory())

	assert.NoError(t, recorder.AddCounter(context.Background(), "requests", "desc", "1", nil, 1))
}

func TestFromContext_ReturnsUsableAdapters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	require.NotNil(t, obsbridge.LoggerFromContext(ctx))
	require.NotNil(t, obsbridge.MetricsFromContext(ctx))

	logger, tracer, _, recorder := obsbridge.TrackingFromContext(ctx)
	assert.NotNil(t, logger)
	assert.NotNil(t, tracer)
	assert.NotNil(t, recorder)
}
