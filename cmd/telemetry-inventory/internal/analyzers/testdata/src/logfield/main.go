package logfield

import (
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

func emit(ctx context.Context, logger log.Logger, tenantID string) {
	logger.Log(ctx, log.LevelInfo, "ok", log.String("tenant_id", tenantID), log.Int("balance", 10)) // want `log field "tenant_id" level=info` `log field "balance" level=info`
	logger.With(log.String("trace_id", "abc"))                                                      // want `log field "trace_id" level=with`
	logger.Log(ctx, log.LevelError, "failed", log.String("email", "a@example.com"))                 // want `log field "email" level=error`

	// Short-form helpers — Debug / Info / Warn — share the same matcher
	// branch and need explicit fixture coverage; PII keys (password, token,
	// secret) flip pii_risk_flag and the assertions in logfield_test.go pin
	// the regex behavior.
	logger.Debug(ctx, "msg", log.String("trace_id", "x"))        // want `log field "trace_id" level=debug`
	logger.Warn(ctx, "warn", log.String("password", "redacted")) // want `log field "password" level=warn`
	logger.Info(ctx, "info", log.String("token", "redacted"))    // want `log field "token" level=info`
	logger.Error(ctx, "err", log.String("secret", "redacted"))   // want `log field "secret" level=error`
	// Message-only call — early-return path before fields are read.
	logger.Info(ctx, "ping")
}

const constKey = "request_id"

func emitConstKey(ctx context.Context, logger log.Logger) {
	logger.Log(ctx, log.LevelInfo, "ok", log.String(constKey, "abc")) // want `log field "request_id" level=info`
}
