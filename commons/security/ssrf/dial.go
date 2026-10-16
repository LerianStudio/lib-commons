package ssrf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"
)

// Default dialer settings used when [DialContext] receives a nil base dialer.
const (
	defaultDialTimeout   = 30 * time.Second
	defaultDialKeepAlive = 30 * time.Second
)

// DialFunc is the signature of [net.Dialer.DialContext] and of
// [net/http.Transport.DialContext].
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// DialContext returns a dial function that enforces the SSRF blocklist at the
// moment of connecting, which closes the DNS-rebinding window that a
// validate-then-connect flow leaves open.
//
// For every dial it:
//  1. Refuses a blocked hostname ([IsBlockedHostname], honouring
//     [WithAllowHostname]).
//  2. Resolves the host once (through [WithLookupFunc] when given) and refuses
//     the dial if ANY resolved address is blocked ([IsBlockedAddr]), unless
//     [WithAllowPrivateNetwork] is set.
//  3. Dials the validated addresses in order, never the hostname, so no second
//     resolution can substitute another address.
//  4. Re-checks, from a [net.Dialer] control hook, the address the socket is
//     actually about to connect to. The hook runs before the connect, so a
//     blocked address never receives a packet.
//
// Every refusal wraps [ErrBlocked]; a failed or empty resolution wraps
// [ErrDNSFailed]; a malformed address or a nil or finished context wraps
// [ErrInvalidURL]. Scheme options ([WithHTTPSOnly]) have no meaning at this
// layer and are ignored.
//
// base is copied, never mutated; nil selects a dialer with a 30s timeout and
// keep-alive. A control hook already on base still runs, after the SSRF check.
// The returned function is safe for concurrent use.
//
// Use it as an [net/http.Transport.DialContext] with the transport's Proxy set
// to nil: behind a proxy the dialed address is the proxy's, not the target's.
// A transport that uses a forward proxy must send only its direct dials here,
// as commons/net/http/outbound does.
func DialContext(base *net.Dialer, opts ...Option) DialFunc {
	cfg := buildConfig(opts)

	dialer := net.Dialer{Timeout: defaultDialTimeout, KeepAlive: defaultDialKeepAlive}
	if base != nil {
		dialer = *base
	}

	dialer.ControlContext = connectControl(cfg, baseControl(base))
	dialer.Control = nil

	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if err := validateContext(ctx); err != nil {
			return nil, err
		}

		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("%w: dial address %q: %w", ErrInvalidURL, address, err)
		}

		addrs, err := resolveDialTargets(ctx, host, cfg)
		if err != nil {
			return nil, err
		}

		var dialErrs []error

		for _, addr := range addrs {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
			if dialErr == nil {
				return conn, nil
			}

			dialErrs = append(dialErrs, dialErr)

			if ctx.Err() != nil {
				break
			}
		}

		return nil, fmt.Errorf("ssrf: dial %s: %w", host, errors.Join(dialErrs...))
	}
}

// resolveDialTargets applies the hostname blocklist, resolves host, and
// returns the addresses to dial, refusing the whole set when any is blocked.
func resolveDialTargets(ctx context.Context, host string, cfg *config) ([]netip.Addr, error) {
	if isBlockedHostnameWithConfig(host, cfg) {
		return nil, fmt.Errorf("%w: hostname %q is blocked", ErrBlocked, host)
	}

	if literal, err := netip.ParseAddr(host); err == nil {
		if !cfg.allowPrivate && IsBlockedAddr(literal) {
			return nil, fmt.Errorf("%w: IP %s is in a blocked range", ErrBlocked, host)
		}

		return []netip.Addr{literal}, nil
	}

	answers, err := lookupHost(ctx, host, cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: lookup failed for %s: %w", ErrDNSFailed, host, err)
	}

	addrs := make([]netip.Addr, 0, len(answers))

	for _, answer := range answers {
		addr, parseErr := netip.ParseAddr(answer)
		if parseErr != nil {
			continue
		}

		if !cfg.allowPrivate && IsBlockedAddr(addr) {
			return nil, fmt.Errorf("%w: resolved IP %s is in a blocked range", ErrBlocked, answer)
		}

		addrs = append(addrs, addr)
	}

	if len(addrs) == 0 {
		return nil, fmt.Errorf("%w: no usable addresses returned for %s", ErrDNSFailed, host)
	}

	return addrs, nil
}

type controlFunc func(ctx context.Context, network, address string, conn syscall.RawConn) error

// baseControl returns the control hook already configured on base, preferring
// ControlContext as net.Dialer does.
func baseControl(base *net.Dialer) controlFunc {
	switch {
	case base == nil:
		return nil
	case base.ControlContext != nil:
		return base.ControlContext
	case base.Control != nil:
		control := base.Control

		return func(_ context.Context, network, address string, conn syscall.RawConn) error {
			return control(network, address, conn)
		}
	default:
		return nil
	}
}

// connectControl judges the address a socket is about to connect to, then runs
// next. An address it cannot parse is refused.
func connectControl(cfg *config, next controlFunc) controlFunc {
	return func(ctx context.Context, network, address string, conn syscall.RawConn) error {
		if !cfg.allowPrivate {
			addrPort, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: connect address %q is not an IP: %w", ErrBlocked, address, err)
			}

			if IsBlockedAddr(addrPort.Addr()) {
				return fmt.Errorf("%w: connect address %s is in a blocked range", ErrBlocked, address)
			}
		}

		if next != nil {
			return next(ctx, network, address, conn)
		}

		return nil
	}
}
