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
	level int
	msg   string
}

type recordingLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

func (l *recordingLogger) Log(_ context.Context, level int, msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = append(l.entries, logEntry{level: level, msg: msg})
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
