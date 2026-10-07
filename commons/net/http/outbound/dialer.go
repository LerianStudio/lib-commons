package outbound

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/security/ssrf"
)

var (
	// ErrProxyConnect is returned by [Dialer.DialTLS] when the forward proxy
	// does not open the tunnel: it answers the CONNECT with a status other
	// than 200, with no valid reply, or with bytes after its 200. The error
	// names the status and the proxy's scheme and host, never the reply body
	// or the proxy credentials.
	ErrProxyConnect = errors.New("outbound: forward proxy refused the tunnel")

	// ErrNilDialer is returned by [Dialer.DialTLS] on a nil receiver.
	ErrNilDialer = errors.New("outbound: nil dialer")
)

// pastDeadline interrupts every pending read and write on a connection.
var pastDeadline = time.Unix(1, 0)

// Dialer opens TLS connections under the same egress rules as [NewClient],
// without writing an HTTP request to the target: a readiness probe that only
// needs to reach the upstream and complete the TLS handshake uses it where
// the client's forward proxy is the only way out. It is safe for concurrent
// use.
type Dialer struct {
	egress  *egress
	timeout time.Duration
}

// NewDialer returns a [Dialer] built from the same options as [NewClient],
// validated and audited the same way: the forward-proxy line and the
// security-bypass lines for a private network or skipped certificate
// verification. [WithTimeout] bounds each [Dialer.DialTLS] call (default
// 30s). [WithRedirects] is validated and has no effect. The plaintext
// allowances, [WithAllowPlaintextHTTP] and [WithAllowInsecureHTTP], have no
// effect either: the dialer speaks only TLS.
func NewDialer(opts ...Option) (*Dialer, error) {
	cfg, err := buildConfig(opts)
	if err != nil {
		return nil, err
	}

	e, err := newEgress(context.Background(), cfg)
	if err != nil {
		return nil, err
	}

	return &Dialer{egress: e, timeout: cfg.timeout}, nil
}

// DialTLS connects to the host and port of rawURL, an https URL (path and
// query are ignored), and returns the connection after a completed TLS
// handshake. The caller closes it.
//
// The target passes the client's checks before anything is dialed: an
// https-only scheme ([ErrInsecureScheme] for any other, whatever plaintext
// allowance the dialer was built with), the hostname blocklist, the
// IP-literal check including non-canonical IPv4 spellings ([ssrf.ErrBlocked])
// and, for a proxied target, the client-side resolution ([ssrf.ErrDNSFailed]
// unless [WithProxyUnresolvedTargets]). The proxy is chosen as the client
// chooses it, NO_PROXY included. A direct target is dialed through the
// dial-time SSRF check. A proxied target is reached through a CONNECT tunnel,
// with the proxy credentials, if any, in Proxy-Authorization; a proxy that
// does not open the tunnel is [ErrProxyConnect].
//
// The handshake verifies the target's certificate with the dialer's TLS
// config under the URL hostname, unless that config sets a ServerName. Its
// errors are wrapped, so [errors.As] still reaches a
// *[tls.CertificateVerificationError] or a *[net.OpError]. The call is bounded
// by ctx and by [WithTimeout]; a nil ctx is refused ([ssrf.ErrInvalidURL]),
// and a call cut short by ctx returns an error that [errors.Is] matches with
// ctx's error.
func (d *Dialer) DialTLS(ctx context.Context, rawURL string) (*tls.Conn, error) {
	if d == nil || d.egress == nil {
		return nil, ErrNilDialer
	}

	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ssrf.ErrInvalidURL)
	}

	target, err := url.Parse(rawURL)
	if err != nil {
		// The parse error repeats the URL, which may carry credentials.
		return nil, fmt.Errorf("%w: target URL does not parse", ssrf.ErrInvalidURL)
	}

	if !strings.EqualFold(target.Scheme, schemeHTTPS) {
		return nil, fmt.Errorf("%w: %q", ErrInsecureScheme, target.Scheme)
	}

	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	if err := d.egress.admit(ctx, target); err != nil {
		return nil, err
	}

	var proxyURL *url.URL

	if d.egress.proxy != nil {
		if proxyURL, err = d.egress.proxy(target); err != nil {
			return nil, err
		}
	}

	addr := targetAddr(target)

	conn, err := d.dial(ctx, proxyURL, addr)
	if err != nil {
		return nil, err
	}

	// Interrupt the CONNECT exchange and the handshake when ctx ends.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(pastDeadline) })

	tlsConn, err := d.handshake(ctx, conn, proxyURL, addr)
	if !stop() && err == nil {
		err = fmt.Errorf("outbound: dial %s: %w", addr, context.Cause(ctx))
	}

	if err != nil {
		_ = conn.Close()

		return nil, err
	}

	return tlsConn, nil
}

// dial opens the connection the handshake runs over: to the target through
// the SSRF-checked dialer, or to the proxy (TLS included for an https://
// proxy) through the proxy route.
func (d *Dialer) dial(ctx context.Context, proxyURL *url.URL, addr string) (net.Conn, error) {
	if proxyURL == nil {
		return d.egress.direct(ctx, "tcp", addr)
	}

	conn, err := d.egress.dial(ctx, "tcp", targetAddr(proxyURL))
	if err != nil {
		return nil, fmt.Errorf("outbound: dial forward proxy %s: %w", describeProxy(proxyURL), err)
	}

	return conn, nil
}

// handshake opens the tunnel when there is a proxy, then runs the TLS
// handshake with the target over conn.
func (d *Dialer) handshake(ctx context.Context, conn net.Conn, proxyURL *url.URL, addr string) (*tls.Conn, error) {
	if proxyURL != nil {
		if err := openTunnel(ctx, conn, proxyURL, addr); err != nil {
			return nil, err
		}
	}

	tlsConfig := d.egress.tlsConfig.Clone()
	if tlsConfig.ServerName == "" {
		host, _, _ := net.SplitHostPort(addr)
		tlsConfig.ServerName = host
	}

	tlsConn := tls.Client(conn, tlsConfig)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("outbound: TLS handshake with %s: %w", addr, interrupted(ctx, err))
	}

	return tlsConn, nil
}

// openTunnel sends CONNECT addr to the proxy over conn, as net/http does, and
// reads the reply. Only a 200 with nothing after it opens the tunnel.
func openTunnel(ctx context.Context, conn net.Conn, proxyURL *url.URL, addr string) error {
	where := describeProxy(proxyURL)

	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}

	if user := proxyURL.User; user != nil {
		password, _ := user.Password()
		req.Header.Set("Proxy-Authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(user.Username()+":"+password)))
	}

	if err := req.Write(conn); err != nil {
		return fmt.Errorf("outbound: CONNECT to forward proxy %s: %w", where, interrupted(ctx, err))
	}

	reader := bufio.NewReader(conn)

	// The body of a 200 is the tunnel itself, so it is never read or closed;
	// the caller closes conn on every failure.
	resp, err := http.ReadResponse(reader, req) //nolint:bodyclose // see above
	if err != nil {
		return replyError(ctx, err, where)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: status %d from %s for %s", ErrProxyConnect, resp.StatusCode, where, addr)
	}

	if n := reader.Buffered(); n > 0 {
		return fmt.Errorf("%w: %s sent %d bytes after its reply", ErrProxyConnect, where, n)
	}

	return nil
}

// replyError classifies a failure to read the CONNECT reply: ctx ending and
// a network error keep their error; anything else is a reply the proxy did
// not form, which is not repeated, since it may hold whatever the proxy sent.
func replyError(ctx context.Context, err error, where string) error {
	var netErr net.Error

	if ctx.Err() == nil && !errors.As(err, &netErr) {
		return fmt.Errorf("%w: no valid reply from %s", ErrProxyConnect, where)
	}

	return fmt.Errorf("outbound: CONNECT reply from forward proxy %s: %w", where, interrupted(ctx, err))
}

// interrupted adds ctx's error to err when ctx has ended, so a deadline that
// surfaced as an I/O timeout still matches [context.DeadlineExceeded].
func interrupted(ctx context.Context, err error) error {
	ctxErr := context.Cause(ctx)
	if ctxErr == nil || errors.Is(err, ctxErr) {
		return err
	}

	return fmt.Errorf("%w: %w", ctxErr, err)
}
