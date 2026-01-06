package zap

import (
	"fmt"
	"os"
	"strings"

	clog "github.com/LerianStudio/lib-commons/v2/commons/log"
	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// InitializeLogger initializes our log layer and returns it.
// Returns an error if the logger cannot be initialized.
//
//nolint:ireturn
func InitializeLogger() (clog.Logger, error) {
	var zapCfg zap.Config

	envName := strings.ToLower(os.Getenv("ENV_NAME"))
	if envName == "production" || envName == "prod" {
		zapCfg = zap.NewProductionConfig()
		zapCfg.EncoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
		zapCfg.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	} else {
		zapCfg = zap.NewDevelopmentConfig()
		zapCfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		zapCfg.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	}

	if val, ok := os.LookupEnv("LOG_LEVEL"); ok {
		var lvl zapcore.Level
		if err := lvl.Set(val); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: invalid LOG_LEVEL value %q: %v (using default level)\n", val, err)
		} else {
			zapCfg.Level = zap.NewAtomicLevelAt(lvl)
		}
	}

	zapCfg.DisableStacktrace = true

	logger, err := zapCfg.Build(zap.AddCallerSkip(2), zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return zapcore.NewTee(core, otelzap.NewCore(os.Getenv("OTEL_LIBRARY_NAME")))
	}))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize zap logger: %w", err)
	}

	sugarLogger := logger.Sugar()

	sugarLogger.Infof("Log level is (%v)", zapCfg.Level)
	sugarLogger.Infof("Logger is (%T) \n", sugarLogger)

	return &ZapWithTraceLogger{
		Logger: sugarLogger,
	}, nil
}

// MustInitializeLogger initializes the logger and panics if it fails.
// WARNING: This function terminates the process on failure. Callers should
// migrate to InitializeLogger() which returns errors for graceful handling.
// Deprecated: Use InitializeLogger instead for graceful error handling.
func MustInitializeLogger() clog.Logger {
	logger, err := InitializeLogger()
	if err != nil {
		panic(err)
	}

	return logger
}
