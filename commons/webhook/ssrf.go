package webhook

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"

	libSSRF "github.com/LerianStudio/lib-commons/v5/commons/security/ssrf"
)

// resolveAndValidateIP delegates to the canonical [libSSRF.ResolveAndValidate]
// function and maps its result back to the 4-return-value signature expected by
// [deliverToEndpoint]. This keeps the webhook package's internal call-sites
// stable while removing the duplicated SSRF implementation.
//
// Caller-supplied opts are forwarded verbatim to [libSSRF.ResolveAndValidate]
// so per-Deliverer configuration (e.g., [WithAllowPrivateNetwork]) can loosen
// or tighten the underlying validation without changing this signature.
//
// On success it returns:
//   - pinnedURL         — original URL with the hostname replaced by the first safe resolved IP.
//   - originalAuthority — the original host:port authority, suitable for the HTTP Host header.
//   - sniHostname       — the bare hostname (port stripped), suitable for TLS SNI / certificate verification.
//
// Error mapping:
//   - [libSSRF.ErrBlocked]    → [ErrSSRFBlocked]
//   - [libSSRF.ErrDNSFailed]  → [ErrSSRFBlocked]
//   - [libSSRF.ErrInvalidURL] → [ErrInvalidURL]
func resolveAndValidateIP(ctx context.Context, rawURL string, opts ...libSSRF.Option) (pinnedURL, originalAuthority, sniHostname string, err error) {
	result, resolveErr := libSSRF.ResolveAndValidate(ctx, rawURL, opts...)
	if resolveErr != nil {
		return "", "", "", mapSSRFError(resolveErr)
	}

	return result.PinnedURL, result.Authority, result.SNIHostname, nil
}

func resolveAndValidateWebhookTarget(ctx context.Context, rawURL string, allowPrivateNetwork bool, opts ...libSSRF.Option) (pinnedURL, originalAuthority, sniHostname string, err error) {
	if !allowPrivateNetwork || !isPrivateNetworkIPLiteral(rawURL) {
		return resolveAndValidateIP(ctx, rawURL, opts...)
	}

	allowOpts := append([]libSSRF.Option{}, opts...)
	allowOpts = append(allowOpts, libSSRF.WithAllowPrivateNetwork())

	return resolveAndValidateIP(ctx, rawURL, allowOpts...)
}

func isPrivateNetworkIPLiteral(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	ip := net.ParseIP(parsed.Hostname())
	if ip == nil {
		return false
	}

	return ip.IsLoopback() || ip.IsPrivate()
}

// mapSSRFError translates sentinel errors from the canonical ssrf package into
// the webhook package's error types so that existing callers (and tests) that
// check [errors.Is] against [ErrSSRFBlocked] / [ErrInvalidURL] continue to
// work without modification.
func mapSSRFError(err error) error {
	switch {
	case errors.Is(err, libSSRF.ErrInvalidURL):
		return fmt.Errorf("%w: %w", ErrInvalidURL, err)
	case errors.Is(err, libSSRF.ErrBlocked):
		return fmt.Errorf("%w: %w", ErrSSRFBlocked, err)
	case errors.Is(err, libSSRF.ErrDNSFailed):
		return fmt.Errorf("%w: %w", ErrSSRFBlocked, err)
	default:
		return fmt.Errorf("%w: %w", ErrSSRFBlocked, err)
	}
}
