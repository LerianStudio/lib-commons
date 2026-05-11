package logfield

import (
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

func emit(ctx context.Context, logger log.Logger, tenantID string) {
	logger.Log(ctx, log.LevelInfo, "ok", log.String("tenant_id", tenantID), log.Int("balance", 10)) // want `log field "tenant_id" level=info` `log field "balance" level=info`
	logger.With(log.String("trace_id", "abc"))                                                      // want `log field "trace_id" level=with`
	logger.Log(ctx, log.LevelError, "failed", log.String("email", "a@example.com"))                 // want `log field "email" level=error`

	// PII keys (password, token, secret) flip pii_risk_flag; covered by
	// IsPIIField unit tests, but the analyzer must also still ingest them
	// through the canonical Log path.
	logger.Log(ctx, log.LevelDebug, "msg", log.String("trace_id", "x"))        // want `log field "trace_id" level=debug`
	logger.Log(ctx, log.LevelWarn, "warn", log.String("password", "redacted")) // want `log field "password" level=warn`
	logger.Log(ctx, log.LevelInfo, "info", log.String("token", "redacted"))    // want `log field "token" level=info`
	logger.Log(ctx, log.LevelError, "err", log.String("secret", "redacted"))   // want `log field "secret" level=error`
}

const constKey = "request_id"

func emitConstKey(ctx context.Context, logger log.Logger) {
	logger.Log(ctx, log.LevelInfo, "ok", log.String(constKey, "abc")) // want `log field "request_id" level=info`
}
