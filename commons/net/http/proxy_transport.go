package http

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
)

// ssrfSafeTransport wraps an http.Transport with a DialContext that validates
// resolved IP addresses against the SSRF policy at connection time.
// This prevents DNS rebinding attacks where a hostname resolves to a safe IP
// during validation but a private IP at connection time.
//
// It also implements http.RoundTripper so each outbound request is re-validated
// immediately before dialing with the current proxy policy.
type ssrfSafeTransport struct {
	policy ReverseProxyPolicy
	base   *http.Transport
}

// newSSRFSafeTransport creates a transport that enforces the given proxy policy
// on DNS resolution (via DialContext) and on each outbound request validated by RoundTrip.
func newSSRFSafeTransport(policy ReverseProxyPolicy) *ssrfSafeTransport {
	return newSSRFSafeTransportWithDeps(policy, net.DefaultResolver.LookupIPAddr)
}

func newSSRFSafeTransportWithDeps(
	policy ReverseProxyPolicy,
	lookupIPAddr func(context.Context, string) ([]net.IPAddr, error),
) *ssrfSafeTransport {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
	}

	if !policy.AllowUnsafeDestinations {
		policyLogger := policy.Logger

		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
			}

			ips, err := lookupIPAddr(ctx, host)
			if err != nil {
				if !nilcheck.Interface(policyLogger) {
					policyLogger.Log(ctx, log.LevelWarn, "proxy DNS resolution failed",
						log.String("host", host),
						log.Err(err),
					)
				}

				return nil, fmt.Errorf("%w: %w", ErrDNSResolutionFailed, err)
			}

			safeIP, err := validateResolvedIPs(ctx, ips, host, policyLogger)
			if err != nil {
				return nil, err
			}

			if safeIP != nil && port != "" {
				addr = net.JoinHostPort(safeIP.String(), port)
			} else if safeIP != nil {
				addr = safeIP.String()
			}

			return dialer.DialContext(ctx, network, addr)
		}
	} else {
		transport.DialContext = dialer.DialContext
	}

	return &ssrfSafeTransport{
		policy: policy,
		base:   transport,
	}
}

// RoundTrip validates each outbound request against the proxy policy before forwarding.
func (t *ssrfSafeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateProxyTarget(req.URL, t.policy); err != nil {
		return nil, err
	}

	return t.base.RoundTrip(req)
}

// validateResolvedIPs checks all resolved IPs against the SSRF policy.
// Returns the first safe IP for use in the connection, or an error if any IP
// is unsafe or if no IPs were resolved.
func validateResolvedIPs(ctx context.Context, ips []net.IPAddr, host string, logger log.Logger) (net.IP, error) {
	if len(ips) == 0 {
		if !nilcheck.Interface(logger) {
			logger.Log(ctx, log.LevelWarn, "proxy target resolved to no IPs",
				log.String("host", host),
			)
		}

		return nil, ErrNoResolvedIPs
	}

	var safeIP net.IP

	for _, ipAddr := range ips {
		if isUnsafeIP(ipAddr.IP) {
			if !nilcheck.Interface(logger) {
				logger.Log(ctx, log.LevelWarn, "proxy target resolved to unsafe IP",
					log.String("host", host),
				)
			}

			return nil, ErrUnsafeProxyDestination
		}

		if safeIP == nil {
			safeIP = ipAddr.IP
		}
	}

	return safeIP, nil
}
