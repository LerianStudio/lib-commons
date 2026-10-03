//go:build unit

package outbound

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/LerianStudio/lib-commons/v7/commons/security/ssrf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// dialer helpers
// ---------------------------------------------------------------------------

// countingTarget is a TLS server that counts the HTTP requests it serves, so
// a test can prove a dial wrote none.
func countingTarget(t *testing.T) (*httptest.Server, *tls.Config, *atomic.Int32) {
	t.Helper()

	var requests atomic.Int32

	srv, tlsCfg := tlsServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))

	return srv, tlsCfg, &requests
}

// rawProxy is a loopback TCP listener whose every connection is handed to
// handle. It stands in for a proxy that misbehaves at the byte level.
func rawProxy(t *testing.T, handle func(net.Conn)) *url.URL {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	done := make(chan struct{})

	go func() {
		defer close(done)

		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			handle(conn)
		}
	}()

	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})

	return &url.URL{Scheme: "http", Host: ln.Addr().String()}
}

// readConnect reads the CONNECT request from conn.
func readConnect(t *testing.T, conn net.Conn) {
	t.Helper()

	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		t.Errorf("read CONNECT: %v", err)

		return
	}

	if req.Method != http.MethodConnect {
		t.Errorf("method %s, want CONNECT", req.Method)
	}
}

// statusProxy answers every CONNECT with status and a body naming a secret.
func statusProxy(t *testing.T, status int) *url.URL {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="egress"`)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "upstream says s3cret-body")
	}))
	t.Cleanup(srv.Close)

	return mustURL(t, srv.URL)
}

func dialTLS(t *testing.T, d *Dialer, rawURL string) (*tls.Conn, error) {
	t.Helper()

	conn, err := d.DialTLS(context.Background(), rawURL)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}

	return conn, err
}

// ---------------------------------------------------------------------------
// NewDialer
// ---------------------------------------------------------------------------

func TestDialer_TunnelsThroughConnectWithoutAnHTTPRequest(t *testing.T) {
	t.Parallel()

	target, tlsCfg, requests := countingTarget(t)
	proxy := newForwardProxy(t, target.Listener.Addr().String())

	d, err := NewDialer(WithProxy(proxy.url(t)), WithTLSConfig(tlsCfg), publicExample())
	require.NoError(t, err)

	conn, err := dialTLS(t, d, "https://example.com/readyz")
	require.NoError(t, err)

	state := conn.ConnectionState()
	assert.True(t, state.HandshakeComplete)
	assert.Equal(t, "example.com", state.ServerName, "the target is verified under the URL hostname")
	assert.Equal(t, []string{"CONNECT example.com:443"}, proxy.seen())

	require.NoError(t, conn.Close())
	proxy.closeTunnels()
	target.Close()
	assert.Zero(t, requests.Load(), "the dial never writes an HTTP request to the target")
}

func TestDialer_TLSConfigServerNameWins(t *testing.T) {
	t.Parallel()

	target, tlsCfg, _ := countingTarget(t)
	proxy := newForwardProxy(t, target.Listener.Addr().String())

	tlsCfg.ServerName = "example.com"

	d, err := NewDialer(WithProxy(proxy.url(t)), WithTLSConfig(tlsCfg),
		WithLookupFunc(hostLookup(nil, map[string][]string{"gateway.example": {publicAddr}})))
	require.NoError(t, err)

	conn, err := dialTLS(t, d, "https://gateway.example:8443")
	require.NoError(t, err)
	assert.Equal(t, "example.com", conn.ConnectionState().ServerName)
	assert.Equal(t, []string{"CONNECT gateway.example:8443"}, proxy.seen())
}

func TestDialer_HTTPSProxyOfferingH2GetsPlainConnectWithCredentials(t *testing.T) {
	t.Parallel()

	target, tlsCfg, _ := countingTarget(t)
	proxy := newTLSForwardProxy(t, target.Listener.Addr().String(), nil)

	proxyURL := proxy.url(t)
	require.Equal(t, "https", proxyURL.Scheme)

	proxyURL.User = url.UserPassword("svc-egress", "s3cret-pw")

	logger := &recordingLogger{}

	d, err := NewDialer(WithLogger(logger), WithProxy(proxyURL), WithTLSConfig(tlsCfg), publicExample())
	require.NoError(t, err)

	_, err = dialTLS(t, d, "https://example.com/")
	require.NoError(t, err, "the CONNECT goes to an h2-capable proxy over HTTP/1.1")

	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("svc-egress:s3cret-pw"))
	assert.Equal(t, []string{"CONNECT example.com:443"}, proxy.seen())
	assert.Equal(t, []string{wantAuth}, proxy.seenAuth())

	warns := logger.withFeature(obs.LevelWarn, "outbound_forward_proxy")
	require.Len(t, warns, 1, "the dialer audits the proxy as the client does")
	assert.Equal(t, "option", warns[0]["source"])
	assertNoSecret(t, logger, "s3cret-pw", "svc-egress")
}

func TestDialer_RefusedTunnelNamesNoSecret(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusProxyAuthRequired, http.StatusBadGateway} {
		proxyURL := statusProxy(t, status)
		proxyURL.User = url.UserPassword("svc-egress", "s3cret-pw")

		d, err := NewDialer(WithProxy(proxyURL), publicExample())
		require.NoError(t, err)

		_, err = dialTLS(t, d, "https://example.com/")
		require.ErrorIs(t, err, ErrProxyConnect)
		assert.Contains(t, err.Error(), "status "+strconv.Itoa(status), "the status is named")

		for _, secret := range []string{"s3cret-pw", "svc-egress", "s3cret-body"} {
			assert.NotContains(t, err.Error(), secret)
		}
	}
}

func TestDialer_BytesAfterTheConnectReplyAreRefused(t *testing.T) {
	t.Parallel()

	proxyURL := rawProxy(t, func(conn net.Conn) {
		readConnect(t, conn)

		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\nsmuggled")

		go func() {
			_, _ = io.Copy(io.Discard, conn)
			_ = conn.Close()
		}()
	})

	d, err := NewDialer(WithProxy(proxyURL), publicExample())
	require.NoError(t, err)

	_, err = dialTLS(t, d, "https://example.com/")
	require.ErrorIs(t, err, ErrProxyConnect)
}

func TestDialer_HonoursNoProxy(t *testing.T) {
	clearProxyEnv(t)

	target, tlsCfg, _ := countingTarget(t)
	proxy := newForwardProxy(t, target.Listener.Addr().String())

	t.Setenv("HTTPS_PROXY", proxy.srv.URL)
	t.Setenv("NO_PROXY", "direct.example.net,example.com")

	var lookups atomic.Int32

	d, err := NewDialer(WithProxyFromEnvironment(), WithTLSConfig(tlsCfg),
		WithLookupFunc(hostLookup(&lookups, map[string][]string{"direct.example.net": {"10.0.0.1"}})))
	require.NoError(t, err)

	_, err = dialTLS(t, d, "https://direct.example.net/")
	require.ErrorIs(t, err, ssrf.ErrBlocked, "a NO_PROXY host takes the direct, SSRF-checked dial")
	assert.Equal(t, int32(1), lookups.Load())

	private, err := NewDialer(WithProxyFromEnvironment(), WithTLSConfig(tlsCfg),
		WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	conn, err := dialTLS(t, private, exampleURL(t, target, "/"))
	require.NoError(t, err, "a NO_PROXY host is dialed directly")
	assert.True(t, conn.ConnectionState().HandshakeComplete)

	assert.Empty(t, proxy.seen(), "the proxy sees no NO_PROXY dial")
}

func TestDialer_DirectWithoutAProxy(t *testing.T) {
	clearProxyEnv(t)

	target, tlsCfg, requests := countingTarget(t)
	proxy := newForwardProxy(t, target.Listener.Addr().String())

	t.Setenv("HTTPS_PROXY", proxy.srv.URL)

	d, err := NewDialer(WithTLSConfig(tlsCfg), WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	conn, err := dialTLS(t, d, exampleURL(t, target, "/"))
	require.NoError(t, err)
	assert.Equal(t, "example.com", conn.ConnectionState().ServerName)
	assert.Empty(t, proxy.seen(), "without a proxy option the environment proxy is ignored")

	require.NoError(t, conn.Close())
	target.Close()
	assert.Zero(t, requests.Load())

	strict, err := NewDialer(WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)

	_, err = dialTLS(t, strict, "https://example.com/")
	require.ErrorIs(t, err, ssrf.ErrBlocked, "the direct dial checks the address it connects to")
}

func TestDialer_RefusesTargetsBeforeContactingTheProxy(t *testing.T) {
	t.Parallel()

	proxy := newForwardProxy(t, "127.0.0.1:1")

	var lookups atomic.Int32

	d, err := NewDialer(WithProxy(proxy.url(t)), WithLookupFunc(hostLookup(&lookups, map[string][]string{
		"intranet.example": {"10.0.0.5"},
		"mixed.example":    {publicAddr, "169.254.169.254"},
	})))
	require.NoError(t, err)

	for _, target := range []string{
		"https://metadata.google.internal/",
		"https://localhost/",
		"https://10.0.0.1/",
		"https://169.254.169.254/latest/meta-data/",
		"https://127.1/",
		"https://0x7f.0.0.1/",
		"https://0177.0.0.1/",
		"https://2130706433/",
		"https://intranet.example/",
		"https://mixed.example/",
	} {
		_, err := dialTLS(t, d, target)
		require.ErrorIs(t, err, ssrf.ErrBlocked, target)
	}

	_, err = dialTLS(t, d, "https://unknown.example/")
	require.ErrorIs(t, err, ssrf.ErrDNSFailed, "a target the client cannot resolve is refused by default")

	assert.Empty(t, proxy.seen(), "a refused target never reaches the proxy")
	assert.Equal(t, int32(3), lookups.Load(), "IP literals and blocked names are judged as written")
}

func TestDialer_UnresolvedTargetsGoToTheProxyWhenDelegated(t *testing.T) {
	t.Parallel()

	proxy := newForwardProxy(t, "127.0.0.1:1")

	d, err := NewDialer(WithProxy(proxy.url(t)), WithProxyUnresolvedTargets(),
		WithLookupFunc(hostLookup(nil, nil)))
	require.NoError(t, err)

	_, err = dialTLS(t, d, "https://unknown.example/")
	require.ErrorIs(t, err, ErrProxyConnect, "the proxy cannot reach the upstream and answers 502")
	assert.Equal(t, []string{"CONNECT unknown.example:443"}, proxy.seen())
}

func TestDialer_HTTPSOnly(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TLS", "true")

	proxy := newForwardProxy(t, "127.0.0.1:1")

	d, err := NewDialer(WithProxy(proxy.url(t)), WithAllowPlaintextHTTP(), WithAllowInsecureHTTP(), publicExample())
	require.NoError(t, err)

	for _, target := range []string{"http://example.com/", "ftp://example.com/", "example.com:443"} {
		_, err := dialTLS(t, d, target)
		require.ErrorIs(t, err, ErrInsecureScheme, target)
	}

	assert.Empty(t, proxy.seen())
}

func TestDialer_TargetCertificateFailureIsATLSError(t *testing.T) {
	t.Parallel()

	target, _, _ := countingTarget(t)
	proxy := newForwardProxy(t, target.Listener.Addr().String())

	d, err := NewDialer(WithProxy(proxy.url(t)), publicExample())
	require.NoError(t, err)

	_, err = dialTLS(t, d, "https://example.com/")
	require.Error(t, err)

	var verifyErr *tls.CertificateVerificationError
	require.ErrorAs(t, err, &verifyErr, "a readiness probe can tell a bad certificate from a dead upstream")
	assert.NotErrorIs(t, err, ErrProxyConnect)
}

func TestDialer_StalledProxyHonoursTheDeadline(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	proxyURL := rawProxy(t, func(conn net.Conn) {
		go func() {
			<-release
			_ = conn.Close()
		}()
	})

	d, err := NewDialer(WithProxy(proxyURL), publicExample())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	conn, err := d.DialTLS(ctx, "https://example.com/")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, conn)
	assert.Less(t, time.Since(start), 5*time.Second)

	bounded, err := NewDialer(WithProxy(proxyURL), publicExample(), WithTimeout(200*time.Millisecond))
	require.NoError(t, err)

	start = time.Now()
	_, err = bounded.DialTLS(context.Background(), "https://example.com/")
	require.ErrorIs(t, err, context.DeadlineExceeded, "WithTimeout bounds the whole dial")
	assert.Less(t, time.Since(start), 5*time.Second)

	cancelled, cancelNow := context.WithCancel(context.Background())

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancelNow()
	}()

	_, err = d.DialTLS(cancelled, "https://example.com/")
	require.ErrorIs(t, err, context.Canceled, "a cancelled context interrupts the CONNECT")
}

func TestDialer_NilAndMalformedInputs(t *testing.T) {
	t.Parallel()

	var nilDialer *Dialer

	_, err := nilDialer.DialTLS(context.Background(), "https://example.com/")
	require.ErrorIs(t, err, ErrNilDialer)

	d, err := NewDialer(publicExample())
	require.NoError(t, err)

	//nolint:staticcheck // a nil context is the input under test
	_, err = d.DialTLS(nil, "https://example.com/")
	require.ErrorIs(t, err, ssrf.ErrInvalidURL)

	for _, target := range []string{"https://exa mple.com/", "https://", "https:///path", "%zz"} {
		_, err := d.DialTLS(context.Background(), target)
		require.Error(t, err, target)
		assert.True(t, errors.Is(err, ssrf.ErrInvalidURL) || errors.Is(err, ErrInsecureScheme), target)
	}
}

func TestNewDialer_ValidatesOptionsAsTheClientDoes(t *testing.T) {
	t.Parallel()

	_, err := NewDialer(WithProxy(mustURL(t, "http://proxy.example:3128")), WithProxyFromEnvironment())
	require.ErrorIs(t, err, ErrInvalidOption)

	_, err = NewDialer(WithProxyUnresolvedTargets())
	require.ErrorIs(t, err, ErrInvalidOption)

	_, err = NewDialer(WithTimeout(0))
	require.ErrorIs(t, err, ErrInvalidOption)

	_, err = NewDialer(WithProxy(mustURL(t, "socks5://svc-egress:s3cret-pw@127.0.0.1:1080")))
	require.ErrorIs(t, err, ErrInvalidOption)
	assert.NotContains(t, err.Error(), "s3cret-pw")

	logger := &recordingLogger{}

	_, err = NewDialer(WithLogger(logger), WithAllowPrivateNetwork())
	require.NoError(t, err)
	assert.Len(t, logger.withFeature(obs.LevelWarn, "outbound_private_network"), 1,
		"the dialer audits a security bypass as the client does")
}

func TestNewDialer_RefusesSkippedVerificationWithoutTheEnvSwitch(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TLS", "")

	//nolint:gosec // the refused config is the input under test
	_, err := NewDialer(WithTLSConfig(&tls.Config{InsecureSkipVerify: true}))
	require.ErrorIs(t, err, ErrInsecureTLSConfig)
}
