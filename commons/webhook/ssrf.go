package webhook

import (
	"context"
	"errors"
	"fmt"

	libSSRF "github.com/LerianStudio/lib-commons/v5/commons/security/ssrf"
)

// resolveAndValidateIP delegates to the canonical [libSSRF.ResolveAndValidate]
// function and maps its result back to the 4-return-value signature expected by
// [deliverToEndpoint]. This keeps the webhook package's internal call-sites
// stable while removing the duplicated SSRF implementation.
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
func resolveAndValidateIP(ctx context.Context, rawURL string) (pinnedURL, originalAuthority, sniHostname string, err error) {
	result, resolveErr := libSSRF.ResolveAndValidate(ctx, rawURL)
	if resolveErr != nil {
		return "", "", "", mapSSRFError(resolveErr)
	}

	return result.PinnedURL, result.Authority, result.SNIHostname, nil
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
