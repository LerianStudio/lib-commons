package log

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestTenantAwareLogger_Log(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	t.Run("injects tenant_id when present in context", func(t *testing.T) {
		mockLogger := log.NewMockLogger(ctrl)

		var capturedFields []log.Field

		mockLogger.EXPECT().
			Log(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, level log.Level, msg string, fields ...log.Field) {
				capturedFields = fields
			})

		logger := NewTenantAwareLogger(mockLogger)
		ctx := core.ContextWithTenantID(context.Background(), "tenant-123")

		logger.Log(ctx, log.LevelInfo, "test message", log.String("key", "value"))

		require.Len(t, capturedFields, 2)
		assert.Equal(t, "key", capturedFields[0].Key)
		assert.Equal(t, "value", capturedFields[0].Value)
		assert.Equal(t, "tenant_id", capturedFields[1].Key)
		assert.Equal(t, "tenant-123", capturedFields[1].Value)
	})

	t.Run("works normally when tenant_id is not in context", func(t *testing.T) {
		mockLogger := log.NewMockLogger(ctrl)

		var capturedFields []log.Field

		mockLogger.EXPECT().
			Log(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, level log.Level, msg string, fields ...log.Field) {
				capturedFields = fields
			})

		logger := NewTenantAwareLogger(mockLogger)
		ctx := context.Background()

		logger.Log(ctx, log.LevelInfo, "test message", log.String("key", "value"))

		require.Len(t, capturedFields, 1)
		assert.Equal(t, "key", capturedFields[0].Key)
		assert.Equal(t, "value", capturedFields[0].Value)
	})

	t.Run("does not overwrite caller-provided tenant_id field", func(t *testing.T) {
		mockLogger := log.NewMockLogger(ctrl)

		var capturedFields []log.Field

		mockLogger.EXPECT().
			Log(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, level log.Level, msg string, fields ...log.Field) {
				capturedFields = fields
			})

		logger := NewTenantAwareLogger(mockLogger)
		ctx := core.ContextWithTenantID(context.Background(), "tenant-123")

		logger.Log(ctx, log.LevelInfo, "test message",
			log.String("tenant_id", "caller-tenant"),
			log.String("key", "value"),
		)

		require.Len(t, capturedFields, 3)
		assert.Equal(t, "tenant_id", capturedFields[0].Key)
		assert.Equal(t, "caller-tenant", capturedFields[0].Value)
		assert.Equal(t, "key", capturedFields[1].Key)
		assert.Equal(t, "value", capturedFields[1].Value)
		assert.Equal(t, "tenant_id", capturedFields[2].Key)
		assert.Equal(t, "tenant-123", capturedFields[2].Value)
	})

	t.Run("nil context handled gracefully", func(t *testing.T) {
		mockLogger := log.NewMockLogger(ctrl)

		mockLogger.EXPECT().
			Log(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, level log.Level, msg string, fields ...log.Field) {
				assert.NotNil(t, ctx, "base logger should receive non-nil context")
			})

		logger := NewTenantAwareLogger(mockLogger)

		logger.Log(nil, log.LevelInfo, "test message")
	})
}

func TestTenantAwareLogger_OtherMethods(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	t.Run("With delegates to base logger", func(t *testing.T) {
		mockLogger := log.NewMockLogger(ctrl)
		wrappedLogger := log.NewMockLogger(ctrl)

		mockLogger.EXPECT().With(log.String("key", "value")).Return(wrappedLogger)

		logger := NewTenantAwareLogger(mockLogger)
		result := logger.With(log.String("key", "value"))

		assert.Equal(t, wrappedLogger, result)
	})

	t.Run("WithGroup delegates to base logger", func(t *testing.T) {
		mockLogger := log.NewMockLogger(ctrl)
		wrappedLogger := log.NewMockLogger(ctrl)

		mockLogger.EXPECT().WithGroup("group").Return(wrappedLogger)

		logger := NewTenantAwareLogger(mockLogger)
		result := logger.WithGroup("group")

		assert.Equal(t, wrappedLogger, result)
	})

	t.Run("Enabled delegates to base logger", func(t *testing.T) {
		mockLogger := log.NewMockLogger(ctrl)

		mockLogger.EXPECT().Enabled(log.LevelInfo).Return(true)

		logger := NewTenantAwareLogger(mockLogger)
		result := logger.Enabled(log.LevelInfo)

		assert.True(t, result)
	})

	t.Run("Sync delegates to base logger", func(t *testing.T) {
		mockLogger := log.NewMockLogger(ctrl)

		mockLogger.EXPECT().Sync(gomock.Any()).Return(nil)

		logger := NewTenantAwareLogger(mockLogger)
		err := logger.Sync(context.Background())

		assert.NoError(t, err)
	})
}
