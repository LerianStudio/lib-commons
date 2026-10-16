//go:build unit

package ssrf

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errNoConnect is returned by the test dialer's Control hook so a dial that
// passed every SSRF check stops before a single packet leaves the host.
var errNoConnect = errors.New("test: connect suppressed")

// suppressingDialer returns a dialer whose Control aborts every connect after
// the SSRF checks ran, counting how many connects were attempted.
func suppressingDialer(attempts *atomic.Int32) *net.Dialer {
	return &net.Dialer{
		Control: func(_, _ string, _ syscall.RawConn) error {
			attempts.Add(1)

			return errNoConnect
		},
	}
}

func staticLookup(ips ...string) LookupFunc {
	return func(context.Context, string) ([]string, error) { return ips, nil }
}

func TestDialContext_RefusesBlockedIPLiteral(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	dial := DialContext(suppressingDialer(&attempts))

	conn, err := dial(context.Background(), "tcp", "127.0.0.1:443")
	require.ErrorIs(t, err, ErrBlocked)
	assert.Nil(t, conn)
	assert.Zero(t, attempts.Load(), "a blocked literal must never reach connect")
}

func TestDialContext_RefusesBlockedResolutionWithoutConnecting(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	dial := DialContext(suppressingDialer(&attempts), WithLookupFunc(staticLookup("10.0.0.1")))

	conn, err := dial(context.Background(), "tcp", "rails.example.com:443")
	require.ErrorIs(t, err, ErrBlocked)
	assert.Nil(t, conn)
	assert.Zero(t, attempts.Load())
}

func TestDialContext_RefusesWhenAnyResolvedIPIsBlocked(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	dial := DialContext(suppressingDialer(&attempts),
		WithLookupFunc(staticLookup("93.184.216.34", "169.254.169.254")))

	_, err := dial(context.Background(), "tcp", "rails.example.com:443")
	require.ErrorIs(t, err, ErrBlocked)
	assert.Zero(t, attempts.Load())
}

func TestDialContext_RebindingBetweenDialsIsRefused(t *testing.T) {
	t.Parallel()

	var (
		attempts atomic.Int32
		lookups  atomic.Int32
	)

	rebinding := func(context.Context, string) ([]string, error) {
		if lookups.Add(1) == 1 {
			return []string{"93.184.216.34"}, nil
		}

		return []string{"127.0.0.1"}, nil
	}

	dial := DialContext(suppressingDialer(&attempts), WithLookupFunc(rebinding))

	_, err := dial(context.Background(), "tcp", "rebind.example.com:443")
	require.ErrorIs(t, err, errNoConnect, "the public answer passes the check and reaches connect")
	assert.Equal(t, int32(1), attempts.Load())

	_, err = dial(context.Background(), "tcp", "rebind.example.com:443")
	require.ErrorIs(t, err, ErrBlocked, "the rebound loopback answer is refused at dial time")
	assert.Equal(t, int32(1), attempts.Load(), "the rebound dial never reaches connect")
}

func TestDialContext_BlockedHostname(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	dial := DialContext(suppressingDialer(&attempts), WithLookupFunc(staticLookup("93.184.216.34")))

	for _, host := range []string{"localhost:80", "metadata.google.internal:80", "svc.cluster.local:80"} {
		_, err := dial(context.Background(), "tcp", host)
		require.ErrorIs(t, err, ErrBlocked, host)
	}

	assert.Zero(t, attempts.Load())
}

func TestDialContext_AllowHostnameExemptsHostnameCheckOnly(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	dial := DialContext(suppressingDialer(&attempts),
		WithAllowHostname("vault.corp.internal"),
		WithLookupFunc(staticLookup("93.184.216.34")))

	_, err := dial(context.Background(), "tcp", "vault.corp.internal:443")
	require.ErrorIs(t, err, errNoConnect)

	blockedIP := DialContext(suppressingDialer(&attempts),
		WithAllowHostname("vault.corp.internal"),
		WithLookupFunc(staticLookup("10.1.2.3")))

	_, err = blockedIP(context.Background(), "tcp", "vault.corp.internal:443")
	require.ErrorIs(t, err, ErrBlocked, "an allowed hostname still resolves through the IP blocklist")
}

func TestDialContext_DNSFailures(t *testing.T) {
	t.Parallel()

	failing := func(context.Context, string) ([]string, error) { return nil, errors.New("nxdomain") }

	_, err := DialContext(nil, WithLookupFunc(failing))(context.Background(), "tcp", "nx.example.com:443")
	require.ErrorIs(t, err, ErrDNSFailed)

	_, err = DialContext(nil, WithLookupFunc(staticLookup()))(context.Background(), "tcp", "empty.example.com:443")
	require.ErrorIs(t, err, ErrDNSFailed)

	_, err = DialContext(nil, WithLookupFunc(staticLookup("not-an-ip")))(context.Background(), "tcp", "junk.example.com:443")
	require.ErrorIs(t, err, ErrDNSFailed)
}

func TestDialContext_InvalidInput(t *testing.T) {
	t.Parallel()

	dial := DialContext(nil)

	_, err := dial(context.Background(), "tcp", "no-port")
	require.ErrorIs(t, err, ErrInvalidURL)

	_, err = dial(context.Background(), "tcp", ":443")
	require.ErrorIs(t, err, ErrBlocked, "an empty host is refused")

	//nolint:staticcheck // a nil context is the invalid input under test
	_, err = dial(nil, "tcp", "93.184.216.34:443")
	require.ErrorIs(t, err, ErrInvalidURL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = dial(ctx, "tcp", "93.184.216.34:443")
	require.ErrorIs(t, err, context.Canceled)
}

func TestDialContext_AllowPrivateNetworkConnectsToLoopback(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = ln.Close() })

	accepted := make(chan struct{})

	go func() {
		defer close(accepted)

		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()

	conn, err := DialContext(nil, WithAllowPrivateNetwork())(context.Background(), "tcp", ln.Addr().String())
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	<-accepted
}

func TestDialContext_AllowPrivateNetworkKeepsHostnameBlocklist(t *testing.T) {
	t.Parallel()

	_, err := DialContext(nil, WithAllowPrivateNetwork(), WithLookupFunc(staticLookup("127.0.0.1")))(
		context.Background(), "tcp", "localhost:80")
	require.ErrorIs(t, err, ErrBlocked)
}

func TestDialContext_TriesValidatedIPsInOrder(t *testing.T) {
	t.Parallel()

	var dialed []string

	base := &net.Dialer{
		Control: func(_, address string, _ syscall.RawConn) error {
			dialed = append(dialed, address)

			return errNoConnect
		},
	}

	dial := DialContext(base, WithLookupFunc(staticLookup("93.184.216.34", "93.184.216.35")))

	_, err := dial(context.Background(), "tcp4", "multi.example.com:8443")
	require.ErrorIs(t, err, errNoConnect)
	assert.Equal(t, []string{"93.184.216.34:8443", "93.184.216.35:8443"}, dialed)
}

func TestDialContext_KeepsBaseControlContext(t *testing.T) {
	t.Parallel()

	var called atomic.Bool

	base := &net.Dialer{
		ControlContext: func(context.Context, string, string, syscall.RawConn) error {
			called.Store(true)

			return errNoConnect
		},
	}

	_, err := DialContext(base, WithLookupFunc(staticLookup("93.184.216.34")))(
		context.Background(), "tcp", "ctl.example.com:443")
	require.ErrorIs(t, err, errNoConnect)
	assert.True(t, called.Load())
	assert.Nil(t, base.Control, "the caller's dialer is not mutated")
}

// The connect-time check is the last line: it judges the address the socket is
// actually about to connect to, whatever produced it.
func TestConnectControl_RefusesBlockedConnectAddress(t *testing.T) {
	t.Parallel()

	check := connectControl(&config{}, nil)

	require.ErrorIs(t, check(context.Background(), "tcp", "127.0.0.1:443", nil), ErrBlocked)
	require.ErrorIs(t, check(context.Background(), "tcp", "[::ffff:10.0.0.1]:443", nil), ErrBlocked)
	require.ErrorIs(t, check(context.Background(), "tcp", "garbage", nil), ErrBlocked)
	require.NoError(t, check(context.Background(), "tcp", "93.184.216.34:443", nil))

	allowPrivate := connectControl(&config{allowPrivate: true}, nil)
	require.NoError(t, allowPrivate(context.Background(), "tcp", "127.0.0.1:443", nil))
}

func TestDialContext_PlainBaseDialerReportsDialFailure(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	address := ln.Addr().String()
	require.NoError(t, ln.Close(), "a closed loopback port refuses the connect")

	_, err = DialContext(&net.Dialer{}, WithAllowPrivateNetwork())(context.Background(), "tcp", address)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrBlocked)
}

func TestDialContext_StopsTryingAddressesOnceContextEnds(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var attempts atomic.Int32

	base := &net.Dialer{
		Control: func(_, _ string, _ syscall.RawConn) error {
			attempts.Add(1)
			cancel()

			return errNoConnect
		},
	}

	_, err := DialContext(base, WithLookupFunc(staticLookup("93.184.216.34", "93.184.216.35")))(
		ctx, "tcp", "multi.example.com:443")
	require.Error(t, err)
	assert.Equal(t, int32(1), attempts.Load(), "a cancelled dial does not move on to the next address")
}

func TestDialContext_RefusesNonCanonicalIPv4BeforeLookup(t *testing.T) {
	t.Parallel()

	lookups := 0
	dial := DialContext(nil, WithAllowPrivateNetwork(), WithLookupFunc(func(context.Context, string) ([]string, error) {
		lookups++

		return []string{"127.0.0.1"}, nil
	}))

	for _, host := range []string{"2130706433", "127.1", "0x7f000001"} {
		_, err := dial(context.Background(), "tcp", net.JoinHostPort(host, "80"))
		require.ErrorIs(t, err, ErrBlocked, host)
	}

	assert.Zero(t, lookups)
}
