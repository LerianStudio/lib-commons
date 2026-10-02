//go:build unit

package outbound

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/LerianStudio/lib-commons/v7/commons/security/ssrf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// forward-proxy helpers
// ---------------------------------------------------------------------------

// forwardProxy is a loopback forward proxy. A CONNECT is tunnelled to
// upstream whatever authority it names; any other request is answered by the
// proxy itself with the request-target it received, so a test can see the
// absolute form. Every request is counted and recorded.
type forwardProxy struct {
	srv  *httptest.Server
	hits atomic.Int32

	mu      sync.Mutex
	targets []string
	auth    []string
	conns   []net.Conn
	pipes   sync.WaitGroup
}

func newForwardProxy(t *testing.T, upstream string) *forwardProxy {
	t.Helper()

	p := &forwardProxy{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.serve(t, upstream, w, r)
	}))

	t.Cleanup(func() {
		p.srv.Close()
		p.closeTunnels()
	})

	return p
}

func (p *forwardProxy) serve(t *testing.T, upstream string, w http.ResponseWriter, r *http.Request) {
	p.hits.Add(1)

	p.mu.Lock()
	p.targets = append(p.targets, r.Method+" "+r.RequestURI)
	p.auth = append(p.auth, r.Header.Get("Proxy-Authorization"))
	p.mu.Unlock()

	if r.Method != http.MethodConnect {
		_, _ = io.WriteString(w, "proxied "+r.RequestURI)

		return
	}

	up, err := (&net.Dialer{}).DialContext(r.Context(), "tcp", upstream)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)

		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = up.Close()

		t.Error("the proxy's response writer cannot hijack")

		return
	}

	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = up.Close()

		t.Errorf("hijack: %v", err)

		return
	}

	p.mu.Lock()
	p.conns = append(p.conns, conn, up)
	p.mu.Unlock()

	_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")

	closeBoth := func() {
		_ = conn.Close()
		_ = up.Close()
	}

	p.pipes.Add(2)

	go func() {
		defer p.pipes.Done()
		defer closeBoth()

		_, _ = io.Copy(up, buffered.Reader)
	}()

	go func() {
		defer p.pipes.Done()
		defer closeBoth()

		_, _ = io.Copy(conn, up)
	}()
}

// closeTunnels closes every hijacked connection and waits for the pipes, so no
// goroutine outlives the test.
func (p *forwardProxy) closeTunnels() {
	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}

	p.pipes.Wait()
}

func (p *forwardProxy) url(t *testing.T) *url.URL {
	t.Helper()

	u, err := url.Parse(p.srv.URL)
	require.NoError(t, err)

	return u
}

func (p *forwardProxy) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.targets...)
}

func (p *forwardProxy) seenAuth() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.auth...)
}

// blockedLookup answers every hostname with a private address and counts
// lookups, so a direct dial is refused and observable.
func blockedLookup(count *atomic.Int32) ssrf.LookupFunc {
	return func(context.Context, string) ([]string, error) {
		count.Add(1)

		return []string{"10.0.0.1"}, nil
	}
}

// clearProxyEnv empties every variable httpproxy reads, lowercase first, so a
// test sees only what it sets.
func clearProxyEnv(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY",
		"no_proxy", "NO_PROXY", "REQUEST_METHOD",
	} {
		t.Setenv(name, "")
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	u, err := url.Parse(raw)
	require.NoError(t, err)

	return u
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return string(body)
}

// ---------------------------------------------------------------------------
// WithProxy
// ---------------------------------------------------------------------------

func TestWithProxy_TunnelsHTTPSThroughConnect(t *testing.T) {
	t.Parallel()

	target, tlsCfg := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "tunnelled "+r.Host)
	}))
	proxy := newForwardProxy(t, target.Listener.Addr().String())

	client, err := NewClient(WithProxy(proxy.url(t)), WithTLSConfig(tlsCfg))
	require.NoError(t, err)
	t.Cleanup(client.CloseIdleConnections)

	resp, err := get(t, client, "https://example.com/")
	require.NoError(t, err, "a default client reaches a public target through a loopback proxy")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "tunnelled example.com", readBody(t, resp),
		"TLS runs end to end to the target, verified against its certificate")
	assert.Equal(t, []string{"CONNECT example.com:443"}, proxy.seen())
}

func TestWithProxy_ClonesTheURL(t *testing.T) {
	t.Parallel()

	target, tlsCfg := tlsServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	proxy := newForwardProxy(t, target.Listener.Addr().String())

	proxyURL := proxy.url(t)

	client, err := NewClient(WithProxy(proxyURL), WithTLSConfig(tlsCfg))
	require.NoError(t, err)
	t.Cleanup(client.CloseIdleConnections)

	proxyURL.Host = "203.0.113.9:1"

	_, err = get(t, client, "https://example.com/")
	require.NoError(t, err)
	assert.Equal(t, int32(1), proxy.hits.Load(), "a later change to the caller's URL has no effect")
}

func TestWithProxy_RefusesBlockedTargetsBeforeContactingProxy(t *testing.T) {
	t.Parallel()

	proxy := newForwardProxy(t, "127.0.0.1:1")

	client, err := NewClient(WithProxy(proxy.url(t)))
	require.NoError(t, err)

	for _, target := range []string{
		"https://metadata.google.internal/",
		"https://localhost/",
		"https://10.0.0.1/",
		"https://169.254.169.254/latest/meta-data/",
	} {
		_, err := get(t, client, target)
		require.ErrorIs(t, err, ssrf.ErrBlocked, target)
	}

	assert.Zero(t, proxy.hits.Load(), "a blocked target never reaches the proxy")
}

func TestWithProxy_PlaintextNeedsAnAllowance(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TLS", "")

	proxy := newForwardProxy(t, "127.0.0.1:1")

	strict, err := NewClient(WithProxy(proxy.url(t)))
	require.NoError(t, err)

	_, err = get(t, strict, "http://example.com/")
	require.ErrorIs(t, err, ErrInsecureScheme)
	assert.Zero(t, proxy.hits.Load(), "a refused scheme never reaches the proxy")

	plaintext, err := NewClient(WithProxy(proxy.url(t)), WithAllowPlaintextHTTP())
	require.NoError(t, err)
	t.Cleanup(plaintext.CloseIdleConnections)

	resp, err := get(t, plaintext, "http://example.com/status?x=1")
	require.NoError(t, err)
	assert.Equal(t, "proxied http://example.com/status?x=1", readBody(t, resp),
		"a plaintext request reaches the proxy in absolute form")
	assert.Equal(t, int32(1), proxy.hits.Load())
}

func TestWithProxy_RedirectThroughTunnelIsRefused(t *testing.T) {
	t.Parallel()

	target, tlsCfg := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/elsewhere", http.StatusFound)
	}))
	proxy := newForwardProxy(t, target.Listener.Addr().String())

	client, err := NewClient(WithProxy(proxy.url(t)), WithTLSConfig(tlsCfg))
	require.NoError(t, err)
	t.Cleanup(client.CloseIdleConnections)

	_, err = get(t, client, "https://example.com/")
	require.ErrorIs(t, err, ErrRedirectRefused)
}

func TestWithProxy_InvalidProxies(t *testing.T) {
	t.Parallel()

	for name, proxyURL := range map[string]*url.URL{
		"socks5":          {Scheme: "socks5", Host: "127.0.0.1:1080"},
		"ftp":             {Scheme: "ftp", Host: "proxy.example:21"},
		"no scheme":       {Host: "proxy.example:3128"},
		"empty host":      {Scheme: "http"},
		"port only":       {Scheme: "http", Host: ":3128"},
		"path":            {Scheme: "http", Host: "proxy.example:3128", Path: "/relay"},
		"query":           {Scheme: "http", Host: "proxy.example:3128", RawQuery: "via=1"},
		"empty query":     {Scheme: "http", Host: "proxy.example:3128", ForceQuery: true},
		"fragment":        {Scheme: "http", Host: "proxy.example:3128", Fragment: "x"},
		"opaque":          {Scheme: "http", Opaque: "proxy.example:3128"},
		"non-ascii host":  {Scheme: "http", Host: "prøxy.example:3128"},
		"port zero":       {Scheme: "http", Host: "proxy.example:0"},
		"port too large":  {Scheme: "http", Host: "proxy.example:65536"},
		"port not number": {Scheme: "http", Host: "proxy.example:http"},
		"space in host":   {Scheme: "http", Host: "proxy example:3128"},
	} {
		_, err := NewClient(WithProxy(proxyURL))
		require.ErrorIs(t, err, ErrInvalidOption, name)

		_, err = NewTransport(WithProxy(proxyURL))
		require.ErrorIs(t, err, ErrInvalidOption, name)
	}

	for _, accepted := range []string{
		"http://proxy.example:3128", "http://proxy.example:3128/", "HTTPS://Proxy.Example",
		"http://[2001:db8::1]:3128", "http://user:pass@10.0.0.5:3128",
	} {
		_, err := NewClient(WithProxy(mustURL(t, accepted)))
		require.NoError(t, err, accepted)
	}
}

func TestWithProxy_RefusesMetadataAndUnroutableProxyAddresses(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"http://169.254.169.254:3128",
		"http://[fe80::1]:3128",
		"http://[fd00:ec2::254]:80",
		"http://[::ffff:169.254.169.254]:3128",
		"http://[64:ff9b::a9fe:a9fe]:3128",
		"http://0.0.0.0:3128",
		"http://[::]:3128",
		"http://224.0.0.1:3128",
	} {
		client, err := NewClient(WithProxy(mustURL(t, raw)))
		require.ErrorIs(t, err, ssrf.ErrBlocked, raw)
		require.ErrorIs(t, err, ErrInvalidOption, raw)
		assert.Nil(t, client)
	}
}

func TestProxyConnectCheck(t *testing.T) {
	t.Parallel()

	for _, address := range []string{
		"169.254.169.254:3128", "[fe80::1]:3128", "[fd00:ec2::254]:80",
		"[::ffff:169.254.169.254]:3128", "[64:ff9b::a9fe:a9fe]:3128", "[2002:a9fe:a9fe::1]:3128",
		"0.0.0.0:3128", "[::]:3128", "224.0.0.1:3128", "[ff02::1]:3128", "proxy.example:3128",
	} {
		require.ErrorIs(t, checkProxyConnectAddr(address), ssrf.ErrBlocked, address)
	}

	for _, address := range []string{
		"127.0.0.1:3128", "[::1]:3128", "10.0.0.5:3128", "192.168.1.1:8080", "172.16.0.1:3128",
		"100.64.0.1:3128", "93.184.216.34:3128",
	} {
		require.NoError(t, checkProxyConnectAddr(address), address)
	}
}

func TestWithProxy_ConflictsWithEnvironmentOption(t *testing.T) {
	clearProxyEnv(t)

	_, err := NewClient(WithProxy(mustURL(t, "http://proxy.example:3128")), WithProxyFromEnvironment())
	require.ErrorIs(t, err, ErrInvalidOption)

	_, err = NewTransport(WithProxyFromEnvironment(), WithProxy(mustURL(t, "http://proxy.example:3128")))
	require.ErrorIs(t, err, ErrInvalidOption)
}

func TestWithProxy_NilIsIgnored(t *testing.T) {
	t.Parallel()

	client, err := NewClient(WithProxy(nil))
	require.NoError(t, err)

	guarded, ok := client.Transport.(*guardedTransport)
	require.True(t, ok)
	assert.Nil(t, guarded.base.Proxy)
}

func TestWithProxy_AuditsWithoutCredentials(t *testing.T) {
	t.Parallel()

	target, tlsCfg := tlsServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	proxy := newForwardProxy(t, target.Listener.Addr().String())

	proxyURL := proxy.url(t)
	proxyURL.User = url.UserPassword("svc-egress", "s3cret-pw")

	logger := &recordingLogger{}

	client, err := NewClient(WithLogger(logger), WithProxy(proxyURL), WithTLSConfig(tlsCfg))
	require.NoError(t, err)
	t.Cleanup(client.CloseIdleConnections)

	_, err = get(t, client, "https://example.com/")
	require.NoError(t, err)

	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("svc-egress:s3cret-pw"))
	assert.Equal(t, []string{wantAuth}, proxy.seenAuth(), "the proxy credentials authenticate the CONNECT")

	warns := logger.withFeature(obs.LevelWarn, "outbound_forward_proxy")
	require.Len(t, warns, 1)
	assert.Equal(t, "option", warns[0]["source"])
	assert.Contains(t, warns[0]["proxy"], proxyURL.Host)
	assert.True(t, logger.has(obs.LevelWarn, "outbound forward proxy active"))

	assertNoSecret(t, logger, "s3cret-pw", "svc-egress")

	_, err = NewClient(WithProxy(mustURL(t, "socks5://svc-egress:s3cret-pw@127.0.0.1:1080")))
	require.ErrorIs(t, err, ErrInvalidOption)
	assert.NotContains(t, err.Error(), "s3cret-pw")
	assert.NotContains(t, err.Error(), "svc-egress")
}

func assertNoSecret(t *testing.T, logger *recordingLogger, secrets ...string) {
	t.Helper()

	logger.mu.Lock()
	defer logger.mu.Unlock()

	for _, entry := range logger.entries {
		line := entry.msg + " " + fmt.Sprint(entry.fields...)
		for _, secret := range secrets {
			assert.NotContains(t, line, secret)
		}
	}
}

// ---------------------------------------------------------------------------
// WithProxyFromEnvironment
// ---------------------------------------------------------------------------

func TestWithProxyFromEnvironment_ReadsOnceAndHonoursNoProxy(t *testing.T) {
	clearProxyEnv(t)

	target, tlsCfg := tlsServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	proxy := newForwardProxy(t, target.Listener.Addr().String())

	t.Setenv("HTTPS_PROXY", proxy.srv.URL)
	t.Setenv("NO_PROXY", "direct.example.net")

	var lookups atomic.Int32

	logger := &recordingLogger{}

	client, err := NewClient(WithLogger(logger), WithProxyFromEnvironment(),
		WithTLSConfig(tlsCfg), WithLookupFunc(blockedLookup(&lookups)))
	require.NoError(t, err)
	t.Cleanup(client.CloseIdleConnections)

	warns := logger.withFeature(obs.LevelWarn, "outbound_forward_proxy")
	require.Len(t, warns, 1)
	assert.Equal(t, "environment", warns[0]["source"])

	_, err = get(t, client, "https://example.com/")
	require.NoError(t, err)
	assert.Equal(t, int32(1), proxy.hits.Load())

	_, err = get(t, client, "https://direct.example.net/")
	require.ErrorIs(t, err, ssrf.ErrBlocked, "a NO_PROXY host takes the direct, SSRF-checked dial")
	assert.Equal(t, int32(1), lookups.Load())
	assert.Equal(t, int32(1), proxy.hits.Load())

	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")

	// Drop the pooled tunnel, so the next request makes a fresh proxy decision.
	client.CloseIdleConnections()

	_, err = get(t, client, "https://example.com/")
	require.NoError(t, err, "the environment is read once, at construction")
	assert.Equal(t, int32(2), proxy.hits.Load())

	_, err = get(t, client, "https://direct.example.net/")
	require.ErrorIs(t, err, ssrf.ErrBlocked)
	assert.Equal(t, int32(2), proxy.hits.Load())
}

func TestWithProxyFromEnvironment_PlaintextUsesHTTPProxy(t *testing.T) {
	clearProxyEnv(t)

	proxy := newForwardProxy(t, "127.0.0.1:1")

	t.Setenv("HTTP_PROXY", strings.TrimPrefix(proxy.srv.URL, "http://"))

	client, err := NewClient(WithProxyFromEnvironment(), WithAllowPlaintextHTTP())
	require.NoError(t, err, "a value without a scheme is read as http://")
	t.Cleanup(client.CloseIdleConnections)

	resp, err := get(t, client, "http://example.com/")
	require.NoError(t, err)
	assert.Equal(t, "proxied http://example.com/", readBody(t, resp))
}

func TestWithProxyFromEnvironment_NothingSetMeansNoProxy(t *testing.T) {
	clearProxyEnv(t)

	logger := &recordingLogger{}

	client, err := NewClient(WithLogger(logger), WithProxyFromEnvironment())
	require.NoError(t, err)

	guarded, ok := client.Transport.(*guardedTransport)
	require.True(t, ok)
	assert.Nil(t, guarded.base.Proxy)
	assert.Empty(t, logger.withFeature(obs.LevelWarn, "outbound_forward_proxy"))
}

func TestWithProxyFromEnvironment_InvalidValues(t *testing.T) {
	for name, env := range map[string][2]string{
		"socks5 https proxy":  {"HTTPS_PROXY", "socks5://svc-egress:s3cret-pw@127.0.0.1:1080"},
		"path in http proxy":  {"HTTP_PROXY", "http://proxy.example:3128/relay"},
		"metadata proxy":      {"HTTPS_PROXY", "169.254.169.254:3128"},
		"unparseable proxy":   {"https_proxy", "http://svc-egress:s3cret-pw@proxy example:3128"},
		"lowercase wins too":  {"http_proxy", "ftp://proxy.example"},
		"bad port in https":   {"HTTPS_PROXY", "http://proxy.example:99999"},
		"query in http proxy": {"HTTP_PROXY", "http://proxy.example:3128?x=1"},
	} {
		t.Run(name, func(t *testing.T) {
			clearProxyEnv(t)
			t.Setenv(env[0], env[1])

			_, err := NewClient(WithProxyFromEnvironment())
			require.ErrorIs(t, err, ErrInvalidOption)
			assert.NotContains(t, err.Error(), "s3cret-pw")
		})
	}
}

func TestWithProxyFromEnvironment_DirectDialToProxyAddressIsRefused(t *testing.T) {
	clearProxyEnv(t)

	t.Setenv("HTTPS_PROXY", "http://Gateway.Example:3128")
	t.Setenv("NO_PROXY", "gateway.example")

	var lookups atomic.Int32

	client, err := NewClient(WithProxyFromEnvironment(), WithLookupFunc(blockedLookup(&lookups)))
	require.NoError(t, err)

	_, err = get(t, client, "https://gateway.example:3128/")
	require.ErrorIs(t, err, ssrf.ErrBlocked,
		"a direct request to the proxy's own address would bypass the target's dial check")
	assert.Zero(t, lookups.Load())
}

func TestNoProxyOption_IgnoresEnvironment(t *testing.T) {
	clearProxyEnv(t)

	target, tlsCfg := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "direct")
	}))
	proxy := newForwardProxy(t, target.Listener.Addr().String())

	t.Setenv("HTTPS_PROXY", proxy.srv.URL)
	t.Setenv("HTTP_PROXY", proxy.srv.URL)

	client, err := NewClient(WithTLSConfig(tlsCfg), WithAllowPrivateNetwork(), WithLookupFunc(loopbackLookup(nil)))
	require.NoError(t, err)
	t.Cleanup(client.CloseIdleConnections)

	guarded, ok := client.Transport.(*guardedTransport)
	require.True(t, ok)
	assert.Nil(t, guarded.base.Proxy)

	resp, err := get(t, client, exampleURL(t, target, "/"))
	require.NoError(t, err)
	assert.Equal(t, "direct", readBody(t, resp))
	assert.Zero(t, proxy.hits.Load(), "without a proxy option the environment proxy is ignored")
}
