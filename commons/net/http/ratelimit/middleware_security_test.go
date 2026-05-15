//go:build unit

package ratelimit

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons"
	chttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_StrictEnforcedDisabledRateLimitFailsClosed(t *testing.T) {
	t.Setenv("ENV_NAME", commons.Production.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(commons.EnvSecurityEnforcement, "true")
	t.Setenv(commons.EnvAllowRateLimitDisabled, "")
	t.Setenv(commons.EnvAllowRateLimitFailOpen, "")
	t.Setenv("RATE_LIMIT_ENABLED", "false")

	spy := &errorSpy{}
	rl := New(nil, WithLogger(spy))
	require.NotNil(t, rl)
	assert.True(t, rl.policyBlocked)
	assert.True(t, spy.hasError("CRITICAL: rate limiter fail-closed is active; all requests will be rejected with 503 until RATE_LIMIT_ENABLED is restored or an override is configured"))

	app := newTestApp(rl.WithRateLimit(DefaultTier()))
	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var errResp chttp.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &errResp))
	assert.Equal(t, policyBlockedTitle, errResp.Title)
	assert.Equal(t, policyBlockedMessage, errResp.Message)
}

func TestNew_StrictEnforcedNilRedisLogsCriticalStartupBlock(t *testing.T) {
	t.Setenv("ENV_NAME", commons.Production.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(commons.EnvSecurityEnforcement, "true")
	t.Setenv(commons.EnvAllowRateLimitDisabled, "")
	t.Setenv(commons.EnvAllowRateLimitFailOpen, "")
	t.Setenv("RATE_LIMIT_ENABLED", "true")

	spy := &errorSpy{}
	rl := New(nil, WithLogger(spy))
	require.NotNil(t, rl)
	assert.True(t, rl.policyBlocked)
	assert.True(t, spy.hasError("CRITICAL: rate limiter fail-closed is active; all requests will be rejected with 503 until Redis is configured"))

	app := newTestApp(rl.WithRateLimit(DefaultTier()))
	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestNew_StrictEnforcedDisabledRateLimitOverridePassesThrough(t *testing.T) {
	t.Setenv("ENV_NAME", commons.Production.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(commons.EnvSecurityEnforcement, "true")
	t.Setenv(commons.EnvAllowRateLimitDisabled, "private network exception")
	t.Setenv(commons.EnvAllowRateLimitFailOpen, "")
	t.Setenv("RATE_LIMIT_ENABLED", "false")

	rl := New(nil)
	require.Nil(t, rl)

	app := newTestApp(rl.WithRateLimit(DefaultTier()))
	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestNew_StrictEnforcedFailOpenForcedClosed(t *testing.T) {
	t.Setenv("ENV_NAME", commons.Production.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(commons.EnvSecurityEnforcement, "true")
	t.Setenv(commons.EnvAllowRateLimitDisabled, "")
	t.Setenv(commons.EnvAllowRateLimitFailOpen, "")
	t.Setenv(commons.EnvAllowInsecureTLS, "unit test redis server")
	t.Setenv("RATE_LIMIT_ENABLED", "true")

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)
	require.NotNil(t, conn)

	rl := New(conn, WithFailOpen(true))
	require.NotNil(t, rl)
	assert.False(t, rl.failOpen)

	app := newTestApp(rl.WithRateLimit(Tier{Name: "strict-fail-open", Max: 10, Window: 60 * time.Second}))
	mr.Close()

	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestNew_StrictEnforcedFailOpenOverridePreservesBehavior(t *testing.T) {
	t.Setenv("ENV_NAME", commons.Production.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(commons.EnvSecurityEnforcement, "true")
	t.Setenv(commons.EnvAllowRateLimitFailOpen, "edge cache owns availability")
	t.Setenv(commons.EnvAllowInsecureTLS, "unit test redis server")
	t.Setenv("RATE_LIMIT_ENABLED", "true")

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)
	require.NotNil(t, conn)

	rl := New(conn, WithFailOpen(true))
	require.NotNil(t, rl)
	assert.True(t, rl.failOpen)

	app := newTestApp(rl.WithRateLimit(Tier{Name: "strict-override", Max: 10, Window: 60 * time.Second}))
	mr.Close()

	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
