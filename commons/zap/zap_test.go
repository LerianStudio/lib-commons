package zap

import (
	"testing"

	"go.uber.org/zap"
)

func TestZap(t *testing.T) {
	t.Run("log with hydration", func(_ *testing.T) {
		l := &ZapWithTraceLogger{}
		l.logWithHydration(func(_ ...any) {}, "")
	})

	t.Run("logf with hydration", func(_ *testing.T) {
		l := &ZapWithTraceLogger{}
		l.logfWithHydration(func(_ string, _ ...any) {}, "", "")
	})

	t.Run("ZapWithTraceLogger info", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		zapLogger.Info(func(_ string, _ ...any) {}, "", "")
	})

	t.Run("ZapWithTraceLogger infof", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		zapLogger.Infof("", "")
	})

	t.Run("ZapWithTraceLogger infoln", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		zapLogger.Infoln("", "")
	})

	t.Run("ZapWithTraceLogger Error", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		zapLogger.Error("", "")
	})

	t.Run("ZapWithTraceLogger Errorf", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		zapLogger.Errorf("", "")
	})

	t.Run("ZapWithTraceLogger Errorln", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		zapLogger.Errorln("", "")
	})

	t.Run("ZapWithTraceLogger Warn", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		zapLogger.Warn("", "")
	})

	t.Run("ZapWithTraceLogger Warnf", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		zapLogger.Warnf("", "")
	})

	t.Run("ZapWithTraceLogger Warnln", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		zapLogger.Warnln("", "")
	})

	t.Run("ZapWithTraceLogger Debug", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		zapLogger.Debug("", "")
	})

	t.Run("ZapWithTraceLogger Debugf", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		zapLogger.Debugf("", "")
	})

	t.Run("ZapWithTraceLogger Debugln", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		zapLogger.Debugln("", "")
	})

	t.Run("ZapWithTraceLogger WithFields", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		zapLogger.WithFields("", "")
	})

	t.Run("ZapWithTraceLogger Sync)", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		_ = zapLogger.Sync()
	})

	t.Run("ZapWithTraceLogger WithDefaultMessageTemplate)", func(_ *testing.T) {
		logger, _ := zap.NewDevelopment()
		sugar := logger.Sugar()

		zapLogger := &ZapWithTraceLogger{
			Logger:                 sugar,
			defaultMessageTemplate: "default template: ",
		}
		zapLogger.WithDefaultMessageTemplate("")
	})

	t.Run("ZapWithTraceLogger hydrateArgs)", func(_ *testing.T) {
		hydrateArgs("", []any{})
	})
}
