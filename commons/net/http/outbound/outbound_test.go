//go:build unit

package outbound

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons"
	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/LerianStudio/lib-commons/v7/commons/security/ssrf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type logEntry struct {
	level  int
	msg    string
	fields []any
}

type recordingLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

func (l *recordingLogger) Log(_ context.Context, level int, msg string, fields ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = append(l.entries, logEntry{level: level, msg: msg, fields: append([]any(nil), fields...)})
}

func (l *recordingLogger) Enabled(int) bool { return true }

func (l *recordingLogger) Sync(context.Context) error { return nil }

func (l *recordingLogger) has(level int, fragment string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, e := range l.entries {
		if e.level == level && strings.Contains(e.msg, fragment) {
			return true
		}
	}

	return false
}

// withFeature returns the fields of every entry at level whose "feature" field
// equals feature, as key-value maps.
func (l *recordingLogger) withFeature(level int, feature string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()

	var out []map[string]any

	for _, e := range l.entries {
		if e.level != level {
			continue
		}

		kv := make(map[string]any, len(e.fields)/2)

		for i := 0; i+1 < len(e.fields); i += 2 {
			if key, ok := e.fields[i].(string); ok {
				kv[key] = e.fields[i+1]
			}
		}

		if kv["feature"] == feature {
			out = append(out, kv)
		}
	}

	return out
}

// loopbackLookup answers every hostname with 127.0.0.1 and counts lookups, so
// a test can prove a refused request never reached DNS.
func loopbackLookup(count *atomic.Int32) ssrf.LookupFunc {
	return func(context.Context, string) ([]string, error) {
		if count != nil {
			count.Add(1)
		}

		return []string{"127.0.0.1"}, nil
	}
}

// tlsServer starts a loopback TLS server whose certificate names example.com
// and returns it with a client TLS config that trusts only that certificate.
func tlsServer(t *testing.T, handler http.Handler) (*httptest.Server, *tls.Config) {
	t.Helper()

	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	return srv, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// exampleURL rewrites the server's loopback URL to example.com on the same
// port, so the request carries a hostname and the lookup maps it back.
func exampleURL(t *testing.T, srv *httptest.Server, path string) string {
	t.Helper()

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)

	u.Host = "example.com:" + u.Port()
	u.Path = path

	return u.String()
}

func get(t *testing.T, client *http.Client, rawURL string) (*http.Response, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	if resp != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}

	return resp, err
}

// ---------------------------------------------------------------------------
// scheme policy
// ---------------------------------------------------------------------------

func TestNewClient_RefusesPlainHTTPBeforeDialing(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "")

	var lookups atomic.Int32

	client, err := NewClient(WithLookupFunc(loopbackLookup(&lookups)))
	require.NoError(t, err)

	_, err = get(t, client, "http://example.com/")
	require.ErrorIs(t, err, ErrInsecureScheme)
	assert.Zero(t, lookups.Load(), "a refused scheme never resolves or dials")
}

func TestNewClient_InsecureHTTPOptionWithoutEnvStaysHTTPSOnly(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "")

	logger := &recordingLogger{}

	client, err := NewClient(WithLogger(logger), WithAllowInsecureHTTP(),
		WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	_, err = get(t, client, "http://example.com/")
	require.ErrorIs(t, err, ErrInsecureScheme)
	assert.True(t, logger.has(obs.LevelError, commons.EnvAllowInsecureTLS))
}

func TestNewClient_InsecureHTTPOptionWithEnvAllowsPlainHTTP(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "plain")
	}))
	t.Cleanup(srv.Close)

	logger := &recordingLogger{}

	client, err := NewClient(WithLogger(logger), WithAllowInsecureHTTP(),
		WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	resp, err := get(t, client, exampleURL(t, srv, "/"))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, logger.has(obs.LevelWarn, "security bypass active"))
}

func TestNewClient_EnvAloneDoesNotAllowPlainHTTP(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "true")

	client, err := NewClient(WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	_, err = get(t, client, "http://example.com/")
	require.ErrorIs(t, err, ErrInsecureScheme)
}

func TestNewClient_PlaintextHTTPOptionAllowsPlainHTTPWithoutEnv(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "plain")
	}))
	t.Cleanup(srv.Close)

	logger := &recordingLogger{}

	client, err := NewClient(WithLogger(logger), WithAllowPlaintextHTTP(),
		WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	resp, err := get(t, client, exampleURL(t, srv, "/"))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "plain", string(body))

	warns := logger.withFeature(obs.LevelWarn, "outbound_plaintext_http")
	require.Len(t, warns, 1, "the per-client allowance is audited once, at construction")
	assert.NotContains(t, warns[0], "env_var", "the per-client allowance does not depend on the environment")
	assert.True(t, logger.has(obs.LevelWarn, "security bypass active"))
	assert.False(t, logger.has(obs.LevelError, commons.EnvAllowInsecureTLS),
		"the per-client allowance is not the refused double-key path")
}

func TestNewClient_PlaintextHTTPOptionLeavesProcessSwitchAlone(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "")

	_, err := NewClient(WithAllowPlaintextHTTP())
	require.NoError(t, err)

	_, err = NewTransport(WithAllowPlaintextHTTP())
	require.NoError(t, err)

	assert.False(t, commons.AllowInsecureTLS(), "the per-client allowance never enables the process-wide switch")
	assert.Empty(t, os.Getenv(commons.EnvAllowInsecureTLS))
}

func TestNewClient_PlaintextHTTPOptionDoesNotRelaxCertificateChecks(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "")

	insecure := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the refusal of this config is under test

	client, err := NewClient(WithAllowPlaintextHTTP(), WithTLSConfig(insecure))
	require.ErrorIs(t, err, ErrInsecureTLSConfig)
	assert.Nil(t, client)
}

func TestNewClient_PlaintextHTTPOptionWithEnvLogsOnlyPerClientAllowance(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "")

	logger := &recordingLogger{}

	_, err := NewClient(WithLogger(logger), WithAllowPlaintextHTTP(), WithAllowInsecureHTTP())
	require.NoError(t, err)

	assert.Len(t, logger.withFeature(obs.LevelWarn, "outbound_plaintext_http"), 1)
	assert.True(t, logger.has(obs.LevelError, commons.EnvAllowInsecureTLS),
		"the double-key option still reports its refusal when the environment is off")
}

func TestNewClient_RefusesNonHTTPSchemes(t *testing.T) {
	t.Parallel()

	transport, err := NewTransport()
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "ftp://example.com/", nil)
	require.NoError(t, err)

	_, err = transport.RoundTrip(req)
	require.ErrorIs(t, err, ErrInsecureScheme)
}

// ---------------------------------------------------------------------------
// SSRF at dial time
// ---------------------------------------------------------------------------

func TestNewClient_RefusesPrivateResolutionByDefault(t *testing.T) {
	t.Parallel()

	srv, tlsCfg := tlsServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the private target must never be reached")
	}))

	client, err := NewClient(WithTLSConfig(tlsCfg), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	_, err = get(t, client, exampleURL(t, srv, "/"))
	require.ErrorIs(t, err, ssrf.ErrBlocked)
}

func TestNewClient_RefusesBlockedHostnameAndIPLiteral(t *testing.T) {
	t.Parallel()

	var lookups atomic.Int32

	client, err := NewClient(WithLookupFunc(loopbackLookup(&lookups)))
	require.NoError(t, err)

	for _, target := range []string{
		"https://metadata.google.internal/computeMetadata/v1/",
		"https://localhost/",
		"https://169.254.169.254/latest/meta-data/",
		"https://10.0.0.1/",
	} {
		_, err = get(t, client, target)
		require.ErrorIs(t, err, ssrf.ErrBlocked, target)
	}

	assert.Zero(t, lookups.Load())
}

func TestNewClient_AllowHostnameExemptsNamedHost(t *testing.T) {
	t.Parallel()

	srv, tlsCfg := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	tlsCfg.ServerName = "example.com"

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)

	target := "https://rsfn.corp.internal:" + u.Port() + "/"

	blocked, err := NewClient(WithTLSConfig(tlsCfg), WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	_, err = get(t, blocked, target)
	require.ErrorIs(t, err, ssrf.ErrBlocked)

	allowed, err := NewClient(WithTLSConfig(tlsCfg), WithAllowPrivateNetwork(),
		WithAllowHostname("rsfn.corp.internal"), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	resp, err := get(t, allowed, target)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestNewClient_SuccessKeepsHostnameAsSNI(t *testing.T) {
	t.Parallel()

	var serverName atomic.Value

	srv, tlsCfg := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverName.Store(r.TLS.ServerName)
		_, _ = io.WriteString(w, "ok")
	}))

	logger := &recordingLogger{}

	client, err := NewClient(WithLogger(logger), WithTLSConfig(tlsCfg),
		WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	resp, err := get(t, client, exampleURL(t, srv, "/"))
	require.NoError(t, err)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(body))
	assert.Equal(t, "example.com", serverName.Load(), "TLS verifies the URL hostname, not the dialed IP")
	assert.True(t, logger.has(obs.LevelWarn, "security bypass active"))
}

// ---------------------------------------------------------------------------
// redirects
// ---------------------------------------------------------------------------

func redirectingServer(t *testing.T, hops map[string]string) (*httptest.Server, *tls.Config) {
	t.Helper()

	return tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if next, ok := hops[r.URL.Path]; ok {
			http.Redirect(w, r, next, http.StatusFound)

			return
		}

		_, _ = io.WriteString(w, "landed")
	}))
}

func TestNewClient_RefusesRedirectsByDefault(t *testing.T) {
	t.Parallel()

	srv, tlsCfg := redirectingServer(t, map[string]string{"/a": "/b"})

	client, err := NewClient(WithTLSConfig(tlsCfg), WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	_, err = get(t, client, exampleURL(t, srv, "/a"))
	require.ErrorIs(t, err, ErrRedirectRefused, "a 3xx is an error, never a response mistaken for success")
}

func TestNewClient_RevalidateFollowsHTTPSHop(t *testing.T) {
	t.Parallel()

	srv, tlsCfg := redirectingServer(t, map[string]string{"/a": "/b"})

	client, err := NewClient(WithTLSConfig(tlsCfg), WithRedirects(RedirectRevalidate, 3),
		WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	resp, err := get(t, client, exampleURL(t, srv, "/a"))
	require.NoError(t, err)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "landed", string(body))
	assert.Equal(t, "/b", resp.Request.URL.Path)
}

func TestNewClient_RevalidateRefusesHopToPlainHTTP(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "")

	srv, tlsCfg := redirectingServer(t, map[string]string{"/a": "http://example.com/b"})

	client, err := NewClient(WithTLSConfig(tlsCfg), WithRedirects(RedirectRevalidate, 3),
		WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	_, err = get(t, client, exampleURL(t, srv, "/a"))
	require.ErrorIs(t, err, ErrInsecureScheme)
}

func TestNewClient_RevalidateRefusesHopToBlockedHost(t *testing.T) {
	t.Parallel()

	srv, tlsCfg := redirectingServer(t, map[string]string{"/a": "https://metadata.google.internal/computeMetadata/v1/"})

	client, err := NewClient(WithTLSConfig(tlsCfg), WithRedirects(RedirectRevalidate, 3),
		WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	_, err = get(t, client, exampleURL(t, srv, "/a"))
	require.ErrorIs(t, err, ssrf.ErrBlocked)
}

func TestNewClient_RevalidateEnforcesHopLimit(t *testing.T) {
	t.Parallel()

	srv, tlsCfg := redirectingServer(t, map[string]string{"/a": "/b", "/b": "/c"})

	client, err := NewClient(WithTLSConfig(tlsCfg), WithRedirects(RedirectRevalidate, 1),
		WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	_, err = get(t, client, exampleURL(t, srv, "/a"))
	require.ErrorIs(t, err, ErrRedirectRefused)
}

// unfollowableServer answers each path with a 3xx the stdlib client does not
// follow on its own, and /b with "landed".
func unfollowableServer(t *testing.T) (*httptest.Server, *tls.Config) {
	t.Helper()

	return tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/b":
			_, _ = io.WriteString(w, "landed")
		case "/302-no-location":
			w.WriteHeader(http.StatusFound)
		case "/300":
			w.Header().Set("Location", "/b")
			w.WriteHeader(http.StatusMultipleChoices)
		case "/307":
			w.Header().Set("Location", "/b")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "/308":
			w.Header().Set("Location", "/b")
			w.WriteHeader(http.StatusPermanentRedirect)
		case "/304":
			w.WriteHeader(http.StatusNotModified)
		}
	}))
}

func doRequest(t *testing.T, client *http.Client, method, rawURL string, body io.Reader, header http.Header) (*http.Response, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, rawURL, body)
	require.NoError(t, err)

	for key, values := range header {
		req.Header[key] = values
	}

	resp, err := client.Do(req)
	if resp != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}

	return resp, err
}

func TestNewClient_UnfollowedRedirectIsAnError(t *testing.T) {
	t.Parallel()

	srv, tlsCfg := unfollowableServer(t)

	// A body without GetBody cannot be replayed, so the stdlib client returns
	// a 307 or 308 to the caller instead of following it.
	oneShot := func() io.Reader { return io.NopCloser(strings.NewReader("payload")) }

	cases := []struct {
		name   string
		method string
		path   string
		body   func() io.Reader
		header http.Header
	}{
		{name: "302 without Location", method: http.MethodGet, path: "/302-no-location"},
		{name: "300 with Location", method: http.MethodGet, path: "/300"},
		{name: "307 on a non-replayable POST", method: http.MethodPost, path: "/307", body: oneShot},
		{name: "308 on a non-replayable POST", method: http.MethodPost, path: "/308", body: oneShot},
		{name: "304 on an unconditional GET", method: http.MethodGet, path: "/304"},
	}

	policies := map[string][]Option{
		"refuse":     nil,
		"revalidate": {WithRedirects(RedirectRevalidate, 3)},
	}

	for policyName, policyOpts := range policies {
		client, err := NewClient(append([]Option{WithTLSConfig(tlsCfg), WithAllowPrivateNetwork(),
			WithLookupFunc(loopbackLookup(nil))}, policyOpts...)...)
		require.NoError(t, err)

		for _, tc := range cases {
			var body io.Reader
			if tc.body != nil {
				body = tc.body()
			}

			resp, err := doRequest(t, client, tc.method, exampleURL(t, srv, tc.path), body, tc.header)
			require.ErrorIs(t, err, ErrRedirectRefused, "%s: %s must not pass for success", policyName, tc.name)
			assert.Nil(t, resp, "%s: %s", policyName, tc.name)
		}

		resp, err := doRequest(t, client, http.MethodGet, exampleURL(t, srv, "/304"), nil,
			http.Header{"If-None-Match": {`"v1"`}})
		require.NoError(t, err, "%s: a 304 answering a conditional request is not a redirect", policyName)
		assert.Equal(t, http.StatusNotModified, resp.StatusCode)
	}
}

func TestNewClient_RevalidateFollowsReplayable307(t *testing.T) {
	t.Parallel()

	srv, tlsCfg := unfollowableServer(t)

	client, err := NewClient(WithTLSConfig(tlsCfg), WithRedirects(RedirectRevalidate, 3),
		WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	resp, err := doRequest(t, client, http.MethodPost, exampleURL(t, srv, "/307"), strings.NewReader("payload"), nil)
	require.NoError(t, err, "a replayable body lets the client follow the 307")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "landed", string(body))
}

func TestCheckRedirect_RefuseStaysClosed(t *testing.T) {
	t.Parallel()

	// The transport refuses the 3xx first; the client's CheckRedirect still
	// refuses on its own, so neither layer alone lets a redirect through.
	next := &http.Request{URL: &url.URL{Scheme: "https", Host: "example.com"}}

	require.ErrorIs(t, checkRedirect(RedirectRefuse, 0)(next, nil), ErrRedirectRefused)
}

func TestNewTransport_RefusesRedirectResponse(t *testing.T) {
	t.Parallel()

	srv, tlsCfg := redirectingServer(t, map[string]string{"/a": "/b"})

	transport, err := NewTransport(WithTLSConfig(tlsCfg), WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	// A client that would follow every redirect still gets the refusal: the
	// transport enforces the default policy on its own.
	client := &http.Client{Transport: transport}

	_, err = doRequest(t, client, http.MethodGet, exampleURL(t, srv, "/a"), nil, nil)
	require.ErrorIs(t, err, ErrRedirectRefused)
}

// ---------------------------------------------------------------------------
// construction
// ---------------------------------------------------------------------------

func TestNewClient_Defaults(t *testing.T) {
	t.Parallel()

	client, err := NewClient(nil)
	require.NoError(t, err, "a nil option is ignored")
	assert.Equal(t, defaultTimeout, client.Timeout)

	guarded, ok := client.Transport.(*guardedTransport)
	require.True(t, ok)
	assert.Nil(t, guarded.base.Proxy, "an environment proxy would make the dial check judge the proxy's IP")
	assert.Equal(t, uint16(tls.VersionTLS12), guarded.base.TLSClientConfig.MinVersion)
	assert.NotPanics(t, guarded.CloseIdleConnections)

	custom, err := NewClient(WithTimeout(5 * time.Second))
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, custom.Timeout)
}

func TestNewTransport_InvalidOptions(t *testing.T) {
	t.Parallel()

	for name, opt := range map[string]Option{
		"zero timeout":          WithTimeout(0),
		"negative timeout":      WithTimeout(-time.Second),
		"revalidate no hops":    WithRedirects(RedirectRevalidate, 0),
		"unknown policy":        WithRedirects(RedirectPolicy(9), 1),
		"refuse with hop count": WithRedirects(RedirectRefuse, 2),
	} {
		_, err := NewClient(opt)
		require.ErrorIs(t, err, ErrInvalidOption, name)

		_, err = NewTransport(opt)
		require.ErrorIs(t, err, ErrInvalidOption, name)
	}
}

func TestWithTLSConfig_ClonesAndRaisesFloor(t *testing.T) {
	t.Parallel()

	original := &tls.Config{MinVersion: tls.VersionTLS10, ServerName: "rails.example.com"}

	transport, err := NewTransport(WithTLSConfig(original), WithTLSConfig(nil))
	require.NoError(t, err)

	guarded, ok := transport.(*guardedTransport)
	require.True(t, ok)
	assert.Equal(t, uint16(tls.VersionTLS12), guarded.base.TLSClientConfig.MinVersion)
	assert.Equal(t, "rails.example.com", guarded.base.TLSClientConfig.ServerName)
	assert.Equal(t, uint16(tls.VersionTLS10), original.MinVersion, "the caller's config is not mutated")
	assert.NotSame(t, original, guarded.base.TLSClientConfig)
}

func TestWithTLSConfig_InsecureSkipVerifyNeedsEnv(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "")

	insecure := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the refusal of this config is under test

	transport, err := NewTransport(WithTLSConfig(insecure))
	require.ErrorIs(t, err, ErrInsecureTLSConfig)
	assert.Nil(t, transport, "a refused transport is an untyped nil")

	client, err := NewClient(WithTLSConfig(insecure))
	require.ErrorIs(t, err, ErrInsecureTLSConfig)
	assert.Nil(t, client)

	t.Setenv(commons.EnvAllowInsecureTLS, "true")

	logger := &recordingLogger{}

	_, err = NewTransport(WithLogger(logger), WithTLSConfig(insecure))
	require.NoError(t, err)
	assert.True(t, logger.has(obs.LevelWarn, "security bypass active"))
}

func TestGuardedTransport_RefusalClosesBodyAndNilRequest(t *testing.T) {
	t.Parallel()

	transport, err := NewTransport()
	require.NoError(t, err)

	_, err = transport.RoundTrip(nil)
	require.ErrorIs(t, err, ErrNilRequest)

	body := &closeTracker{Reader: strings.NewReader("payload")}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com/", body)
	require.NoError(t, err)

	_, err = transport.RoundTrip(req)
	require.ErrorIs(t, err, ErrInsecureScheme)
	assert.True(t, body.closed.Load(), "RoundTrip closes the request body on refusal")

	nilURL := &http.Request{Method: http.MethodGet}

	_, err = transport.RoundTrip(nilURL)
	require.ErrorIs(t, err, ErrNilRequest)
}

type closeTracker struct {
	io.Reader
	closed atomic.Bool
}

func (c *closeTracker) Close() error {
	c.closed.Store(true)

	return nil
}

func TestRedirectPolicy_String(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "refuse", RedirectRefuse.String())
	assert.Equal(t, "revalidate", RedirectRevalidate.String())
	assert.Equal(t, "unknown", RedirectPolicy(9).String())
	assert.False(t, errors.Is(ErrRedirectRefused, ErrInsecureScheme))
}
