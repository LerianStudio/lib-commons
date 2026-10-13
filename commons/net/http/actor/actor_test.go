//go:build unit

package actor_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/net/http/actor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const token = "eyJhbGciOiJSUzI1NiJ9.partner-claims.signature"

// recordingTransport captures every request it receives and answers 200, so a
// test can inspect exactly what reached the wire.
type recordingTransport struct {
	mu   sync.Mutex
	reqs []*http.Request
	err  error
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()

	if r.err != nil {
		return nil, r.err
	}

	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
}

func (r *recordingTransport) only(t *testing.T) *http.Request {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	require.Len(t, r.reqs, 1)

	return r.reqs[0]
}

func newRequest(t *testing.T, ctx context.Context, rawURL string) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	require.NoError(t, err)

	return req
}

func roundTrip(t *testing.T, rt http.RoundTripper, req *http.Request) {
	t.Helper()

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

func TestHeaderName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "X-Lerian-Actor", actor.HeaderName)
}

func TestContext_RoundTripsToken(t *testing.T) {
	t.Parallel()

	got, ok := actor.TokenFromContext(actor.ContextWithToken(context.Background(), token))
	assert.True(t, ok)
	assert.Equal(t, token, got)
}

func TestContext_AbsentToken(t *testing.T) {
	t.Parallel()

	got, ok := actor.TokenFromContext(context.Background())
	assert.False(t, ok)
	assert.Empty(t, got)
}

func TestContext_EmptyTokenIsAbsent(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]string{"empty": "", "blank": "  \t "} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := actor.TokenFromContext(actor.ContextWithToken(context.Background(), value))
			assert.False(t, ok)
			assert.Empty(t, got)
		})
	}
}

func TestContext_EmptyTokenClearsOuterToken(t *testing.T) {
	t.Parallel()

	outer := actor.ContextWithToken(context.Background(), token)

	got, ok := actor.TokenFromContext(actor.ContextWithToken(outer, ""))
	assert.False(t, ok)
	assert.Empty(t, got)
}

func TestContext_NilContext(t *testing.T) {
	t.Parallel()

	//nolint:staticcheck // SA1012: a nil context is exactly the input under test.
	ctx := actor.ContextWithToken(nil, token)
	require.NotNil(t, ctx)

	got, ok := actor.TokenFromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, token, got)

	//nolint:staticcheck // SA1012: a nil context is exactly the input under test.
	got, ok = actor.TokenFromContext(nil)
	assert.False(t, ok)
	assert.Empty(t, got)
}

func TestContext_ForeignKeyWithSameStringIsIgnored(t *testing.T) {
	t.Parallel()

	type foreignKey string

	ctx := context.WithValue(context.Background(), foreignKey("actor"), token)

	_, ok := actor.TokenFromContext(ctx)
	assert.False(t, ok)
}

func TestTransport_SetsHeaderForAllowedHostWithToken(t *testing.T) {
	t.Parallel()

	base := &recordingTransport{}
	rt := actor.NewTransport(base, "crm.internal:4003")

	req := newRequest(t, actor.ContextWithToken(context.Background(), token), "http://crm.internal:4003/v1/holders")
	roundTrip(t, rt, req)

	assert.Equal(t, token, base.only(t).Header.Get(actor.HeaderName))
}

func TestTransport_HostMatchIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		allowed string
		target  string
	}{
		"upper allowed": {allowed: "CRM.Internal:4003", target: "http://crm.internal:4003/x"},
		"upper target":  {allowed: "crm.internal:4003", target: "http://CRM.INTERNAL:4003/x"},
		"no port":       {allowed: "Ledger.Internal", target: "https://ledger.internal/x"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := &recordingTransport{}
			rt := actor.NewTransport(base, tc.allowed)

			roundTrip(t, rt, newRequest(t, actor.ContextWithToken(context.Background(), token), tc.target))

			assert.Equal(t, token, base.only(t).Header.Get(actor.HeaderName))
		})
	}
}

func TestTransport_NeverSetsHeaderForOtherHosts(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"different host":            "http://jd.bank.example:4003/spi",
		"same host other port":      "http://crm.internal:4004/v1",
		"same host no port":         "http://crm.internal/v1",
		"allowed host as subdomain": "http://crm.internal.attacker.example:4003/v1",
		"suffix of allowed host":    "http://internal:4003/v1",
	}

	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := &recordingTransport{}
			rt := actor.NewTransport(base, "crm.internal:4003", "ledger.internal")

			roundTrip(t, rt, newRequest(t, actor.ContextWithToken(context.Background(), token), target))

			assert.Empty(t, base.only(t).Header.Values(actor.HeaderName))
		})
	}
}

func TestTransport_NoAllowedHostsNeverSetsHeader(t *testing.T) {
	t.Parallel()

	base := &recordingTransport{}
	rt := actor.NewTransport(base)

	roundTrip(t, rt, newRequest(t, actor.ContextWithToken(context.Background(), token), "http://crm.internal:4003/v1"))

	assert.Empty(t, base.only(t).Header.Values(actor.HeaderName))
}

func TestTransport_BlankAllowedHostMatchesNothing(t *testing.T) {
	t.Parallel()

	base := &recordingTransport{}
	rt := actor.NewTransport(base, "", "  ")

	// A request whose URL has an empty host must not match a blank allowlist entry.
	req := newRequest(t, actor.ContextWithToken(context.Background(), token), "http://crm.internal/v1")
	req.URL.Host = ""

	roundTrip(t, rt, req)

	assert.Empty(t, base.only(t).Header.Values(actor.HeaderName))
}

func TestTransport_NoTokenNeverSetsHeader(t *testing.T) {
	t.Parallel()

	cases := map[string]context.Context{
		"absent": context.Background(),
		"empty":  actor.ContextWithToken(context.Background(), ""),
	}

	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := &recordingTransport{}
			rt := actor.NewTransport(base, "crm.internal:4003")

			roundTrip(t, rt, newRequest(t, ctx, "http://crm.internal:4003/v1"))

			assert.Empty(t, base.only(t).Header.Values(actor.HeaderName))
		})
	}
}

func TestTransport_DoesNotMutateOriginalRequest(t *testing.T) {
	t.Parallel()

	base := &recordingTransport{}
	rt := actor.NewTransport(base, "crm.internal:4003")

	req := newRequest(t, actor.ContextWithToken(context.Background(), token), "http://crm.internal:4003/v1")
	req.Header.Set("X-Request-Id", "abc")

	roundTrip(t, rt, req)

	sent := base.only(t)
	assert.NotSame(t, req, sent)
	assert.Empty(t, req.Header.Values(actor.HeaderName))
	assert.Equal(t, "abc", sent.Header.Get("X-Request-Id"))
	assert.Equal(t, token, sent.Header.Get(actor.HeaderName))
}

func TestTransport_OverwritesCallerSuppliedHeader(t *testing.T) {
	t.Parallel()

	base := &recordingTransport{}
	rt := actor.NewTransport(base, "crm.internal:4003")

	req := newRequest(t, actor.ContextWithToken(context.Background(), token), "http://crm.internal:4003/v1")
	req.Header.Add(actor.HeaderName, "stale")

	roundTrip(t, rt, req)

	assert.Equal(t, []string{token}, base.only(t).Header.Values(actor.HeaderName))
}

func TestTransport_NilBaseUsesDefaultTransport(t *testing.T) {
	t.Parallel()

	var got string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(actor.HeaderName)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	target, err := url.Parse(srv.URL)
	require.NoError(t, err)

	rt := actor.NewTransport(nil, target.Host)

	resp, err := rt.RoundTrip(newRequest(t, actor.ContextWithToken(context.Background(), token), srv.URL))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, token, got)
}

func TestTransport_RedirectToOtherHostDoesNotCarryHeader(t *testing.T) {
	t.Parallel()

	var external string

	externalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		external = r.Header.Get(actor.HeaderName)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(externalSrv.Close)

	var internal string

	internalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		internal = r.Header.Get(actor.HeaderName)
		http.Redirect(w, r, externalSrv.URL, http.StatusFound)
	}))
	t.Cleanup(internalSrv.Close)

	internalURL, err := url.Parse(internalSrv.URL)
	require.NoError(t, err)

	client := &http.Client{Transport: actor.NewTransport(nil, internalURL.Host)}

	resp, err := client.Do(newRequest(t, actor.ContextWithToken(context.Background(), token), internalSrv.URL))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, token, internal)
	assert.Empty(t, external)
}

func TestTransport_BaseErrorIsReturnedWithoutToken(t *testing.T) {
	t.Parallel()

	baseErr := errors.New("dial tcp: connection refused")
	base := &recordingTransport{err: baseErr}
	rt := actor.NewTransport(base, "crm.internal:4003")

	resp, err := rt.RoundTrip(newRequest(t, actor.ContextWithToken(context.Background(), token), "http://crm.internal:4003/v1"))
	require.ErrorIs(t, err, baseErr)
	assert.Nil(t, resp)
	assert.NotContains(t, err.Error(), token)
}

func TestTransport_NilRequestIsRefusedWithoutPanicking(t *testing.T) {
	t.Parallel()

	base := &recordingTransport{}
	rt := actor.NewTransport(base, "crm.internal:4003")

	resp, err := rt.RoundTrip(nil)
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Empty(t, base.reqs)
}

func TestTransport_RequestWithoutURLPassesThrough(t *testing.T) {
	t.Parallel()

	base := &recordingTransport{}
	rt := actor.NewTransport(base, "crm.internal:4003")

	req := newRequest(t, actor.ContextWithToken(context.Background(), token), "http://crm.internal:4003/v1")
	req.URL = nil

	roundTrip(t, rt, req)

	sent := base.only(t)
	assert.Same(t, req, sent)
	assert.Empty(t, sent.Header.Values(actor.HeaderName))
}

func TestTransport_AllowlistIsCopiedAtConstruction(t *testing.T) {
	t.Parallel()

	hosts := []string{"crm.internal:4003"}
	base := &recordingTransport{}
	rt := actor.NewTransport(base, hosts...)

	hosts[0] = "jd.bank.example"

	roundTrip(t, rt, newRequest(t, actor.ContextWithToken(context.Background(), token), "http://jd.bank.example/v1"))

	assert.Empty(t, base.only(t).Header.Values(actor.HeaderName))
}

func TestContext_FormattingDoesNotLeakToken(t *testing.T) {
	t.Parallel()

	ctx := actor.ContextWithToken(context.Background(), token)

	for _, verb := range []string{"%v", "%+v", "%s", "%#v"} {
		assert.NotContains(t, fmt.Sprintf(verb, ctx), token, verb)
	}
}
