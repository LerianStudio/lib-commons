//go:build unit

package pacing_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/net/http/pacing"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trackedBody reports whether the transport closed the request body, which the
// http.RoundTripper contract requires even on the error paths.
type trackedBody struct {
	io.Reader

	closed atomic.Bool
}

func (b *trackedBody) Close() error {
	b.closed.Store(true)

	return nil
}

func newUpstream(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func staticBuckets(buckets ...pacing.Bucket) pacing.BucketsFunc {
	return func(*http.Request) ([]pacing.Bucket, error) { return buckets, nil }
}

func TestNewRoundTripper_Validation(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr)

	_, err := pacing.NewRoundTripper(http.DefaultTransport, nil, staticBuckets())
	require.ErrorIs(t, err, pacing.ErrPacerUnavailable)

	_, err = pacing.NewRoundTripper(http.DefaultTransport, p, nil)
	require.ErrorIs(t, err, pacing.ErrNoBuckets)

	rt, err := pacing.NewRoundTripper(nil, p, staticBuckets())
	require.NoError(t, err)
	require.NotNil(t, rt, "a nil next transport must fall back to http.DefaultTransport")
}

func TestRoundTripper_PacesThenDelegates(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64

	srv := newUpstream(t, &hits)
	mr := miniredis.RunT(t)
	p := newPacer(t, mr)

	tenant := mustTenantBucket(t, "default", constRate(1000))
	inst := mustInstitutionBucket(t, "077", constRate(1000))

	rt, err := pacing.NewRoundTripper(srv.Client().Transport, p, staticBuckets(tenant, inst))
	require.NoError(t, err)

	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	req, err := http.NewRequestWithContext(boundedCtx(t), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)

	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int64(1), hits.Load())
	assert.Len(t, bucketKeys(t, mr), 2, "both buckets must be charged for one delegated call")
}

func TestRoundTripper_AcquireFailureBlocksTheCallAndClosesTheBody(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64

	srv := newUpstream(t, &hits)
	mr := miniredis.RunT(t)
	p := newPacer(t, mr)
	boom := errors.New("systemplane unreachable")

	rate := func(context.Context) (float64, error) { return 0, boom }
	tenant := mustTenantBucket(t, "default", rate)

	rt, err := pacing.NewRoundTripper(srv.Client().Transport, p, staticBuckets(tenant))
	require.NoError(t, err)

	body := &trackedBody{Reader: strings.NewReader(`{"cpf":"00000000000"}`)}

	req, err := http.NewRequestWithContext(boundedCtx(t), http.MethodPost, srv.URL, body)
	require.NoError(t, err)

	resp, err := rt.RoundTrip(req) //nolint:bodyclose // the error path returns no response
	require.Error(t, err)
	require.Nil(t, resp)
	require.ErrorIs(t, err, pacing.ErrRateUnavailable)

	assert.Equal(t, int64(0), hits.Load(), "the rail must not be called when pacing cannot be evaluated")
	assert.True(t, body.closed.Load(), "RoundTrip must close the request body on the error path")
}

// failingCloseBody closes with an error, which the refusal path must surface
// alongside the refusal rather than discard.
type failingCloseBody struct {
	io.Reader
}

func (failingCloseBody) Close() error { return errors.New("body close failed") }

func TestRoundTripper_BodyCloseFailureIsJoinedOntoTheRefusal(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64

	srv := newUpstream(t, &hits)
	mr := miniredis.RunT(t)
	p := newPacer(t, mr)
	boom := errors.New("systemplane unreachable")

	rate := func(context.Context) (float64, error) { return 0, boom }

	rt, err := pacing.NewRoundTripper(srv.Client().Transport, p, staticBuckets(mustTenantBucket(t, "default", rate)))
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(boundedCtx(t), http.MethodPost, srv.URL,
		failingCloseBody{Reader: strings.NewReader("{}")})
	require.NoError(t, err)

	resp, err := rt.RoundTrip(req) //nolint:bodyclose // the error path returns no response
	require.Nil(t, resp)
	require.ErrorIs(t, err, pacing.ErrRateUnavailable, "the refusal must still be matchable")
	assert.Contains(t, err.Error(), "body close failed", "a close failure must not be discarded")
	assert.Equal(t, int64(0), hits.Load())
}

func TestRoundTripper_BucketDerivationFailureBlocksTheCall(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64

	srv := newUpstream(t, &hits)
	mr := miniredis.RunT(t)
	p := newPacer(t, mr)
	boom := errors.New("no institution on this request")

	rt, err := pacing.NewRoundTripper(srv.Client().Transport, p, func(*http.Request) ([]pacing.Bucket, error) {
		return nil, boom
	})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(boundedCtx(t), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp, err := rt.RoundTrip(req) //nolint:bodyclose // the error path returns no response
	require.Error(t, err)
	require.Nil(t, resp)
	require.ErrorIs(t, err, boom)
	assert.Equal(t, int64(0), hits.Load())
	assert.Empty(t, mr.Keys())
}

func TestRoundTripper_NoBucketsForARequestFailsClosed(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64

	srv := newUpstream(t, &hits)
	mr := miniredis.RunT(t)
	p := newPacer(t, mr)

	rt, err := pacing.NewRoundTripper(srv.Client().Transport, p, staticBuckets())
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(boundedCtx(t), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp, err := rt.RoundTrip(req) //nolint:bodyclose // the error path returns no response
	require.ErrorIs(t, err, pacing.ErrNoBuckets)
	require.Nil(t, resp)
	assert.Equal(t, int64(0), hits.Load(), "an unpaced request must never reach the rail")
}

func TestRoundTripper_CancelledWaitBlocksTheCall(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64

	srv := newUpstream(t, &hits)
	mr := miniredis.RunT(t)
	p := newPacer(t, mr, pacing.WithPollInterval(fastPoll))

	tenant := mustTenantBucket(t, "default", constRate(0))

	rt, err := pacing.NewRoundTripper(srv.Client().Transport, p, staticBuckets(tenant))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), shortDeadine)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp, err := rt.RoundTrip(req) //nolint:bodyclose // the error path returns no response
	require.ErrorIs(t, err, pacing.ErrWaitAborted)
	require.Nil(t, resp)
	assert.Equal(t, int64(0), hits.Load())
}

func TestRoundTripper_NilReceiverFailsClosed(t *testing.T) {
	t.Parallel()

	var rt *pacing.RoundTripper

	req, err := http.NewRequestWithContext(boundedCtx(t), http.MethodGet, "http://example.invalid", nil)
	require.NoError(t, err)

	resp, roundErr := rt.RoundTrip(req) //nolint:bodyclose // the error path returns no response
	require.ErrorIs(t, roundErr, pacing.ErrPacerUnavailable)
	require.Nil(t, resp)
}

func TestRoundTripper_NilRequestIsRefused(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr)

	rt, err := pacing.NewRoundTripper(http.DefaultTransport, p, staticBuckets())
	require.NoError(t, err)

	resp, roundErr := rt.RoundTrip(nil) //nolint:bodyclose // the error path returns no response
	require.Error(t, roundErr)
	require.Nil(t, resp)
}
