//go:build unit

package webhook

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	commons "github.com/LerianStudio/lib-commons/v5/commons"
	libSSRF "github.com/LerianStudio/lib-commons/v5/commons/security/ssrf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// WithAllowPrivateNetwork — tests for the per-Deliverer SSRF loosening option
// ---------------------------------------------------------------------------

// TestWithAllowPrivateNetwork_OptionSetsFlag verifies that the option toggles
// the internal flag without affecting any other Deliverer state. Field-level
// access is acceptable here because the test lives in the same package and
// the field is the contract for downstream ssrfOptions().
func TestWithAllowPrivateNetwork_OptionSetsFlag(t *testing.T) {
	t.Setenv(commons.EnvSecurityTier, commons.TierPermissive.String())

	t.Run("default is strict (flag false)", func(t *testing.T) {
		d := NewDeliverer(&mockLister{})
		require.NotNil(t, d)
		assert.False(t, d.allowPrivateNetwork,
			"allowPrivateNetwork must default to false to preserve v5.1.0 strict SSRF behavior")
	})

	t.Run("WithAllowPrivateNetwork flips flag to true", func(t *testing.T) {
		d := NewDeliverer(&mockLister{}, WithAllowPrivateNetwork())
		require.NotNil(t, d)
		assert.True(t, d.allowPrivateNetwork,
			"WithAllowPrivateNetwork() must set allowPrivateNetwork to true")
	})
}

func TestWithAllowPrivateNetwork_StrictTierRequiresExplicitOverride(t *testing.T) {
	t.Setenv(commons.EnvSecurityTier, commons.TierStrict.String())
	t.Setenv(commons.EnvAllowWebhookPrivateNet, "")

	d := NewDeliverer(&mockLister{}, WithAllowPrivateNetwork())
	require.NotNil(t, d)
	assert.False(t, d.allowPrivateNetwork, "strict tier must fail closed without ALLOW_WEBHOOK_PRIVATE_NETWORK")
}

func TestWithAllowPrivateNetwork_StrictTierAllowsDocumentedOverride(t *testing.T) {
	t.Setenv(commons.EnvSecurityTier, commons.TierStrict.String())
	t.Setenv(commons.EnvAllowWebhookPrivateNet, "local E2E receiver")

	d := NewDeliverer(&mockLister{}, WithAllowPrivateNetwork())
	require.NotNil(t, d)
	assert.True(t, d.allowPrivateNetwork, "explicit ALLOW_WEBHOOK_PRIVATE_NETWORK reason should enable the unsafe option")
}

// TestDeliverer_SSRFOptions verifies the helper that materializes Deliverer
// flags into a libSSRF.Option slice for the call site.
func TestDeliverer_SSRFOptions(t *testing.T) {
	t.Parallel()

	t.Run("strict default returns empty options", func(t *testing.T) {
		t.Parallel()

		d := NewDeliverer(&mockLister{})
		require.NotNil(t, d)
		assert.Empty(t, d.ssrfOptions(),
			"strict default must produce zero SSRF options so libSSRF behaves identically to v5.1.0")
	})

	t.Run("allowPrivateNetwork=true returns empty raw SSRF options", func(t *testing.T) {
		t.Parallel()

		d := &Deliverer{allowPrivateNetwork: true}
		require.NotNil(t, d)
		opts := d.ssrfOptions()
		assert.Empty(t, opts,
			"private-network bypass is now applied only after URL host classification")
	})

	t.Run("nil Deliverer returns empty options", func(t *testing.T) {
		t.Parallel()

		var d *Deliverer
		assert.Empty(t, d.ssrfOptions(),
			"nil Deliverer must be safe to call ssrfOptions on")
	})
}

// ---------------------------------------------------------------------------
// End-to-end behavior: loopback delivery with and without the option
// ---------------------------------------------------------------------------

// startLoopbackServer returns an httptest.Server bound to 127.0.0.1 and the
// server's actual URL (e.g., http://127.0.0.1:NNNN). Unlike startTestServer
// in deliverer_test.go, this returns the genuine loopback URL — exactly the
// shape that consumers running E2E mock lanes will use.
func startLoopbackServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// Sanity-check the address is actually loopback so the regression-guard
	// test isn't accidentally passing because httptest picked a public IP.
	host, _, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	require.True(t, net.ParseIP(host).IsLoopback(),
		"httptest.NewServer must bind to a loopback address for these tests")

	return srv
}

func startIPv6LoopbackServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	listener, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback is unavailable on this host: %v", err)
	}

	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = listener
	srv.Start()
	t.Cleanup(srv.Close)

	host, _, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	require.True(t, net.ParseIP(host).IsLoopback(),
		"test server must bind to IPv6 loopback")

	return srv
}

// TestDeliver_WithAllowPrivateNetwork_DeliversToLoopback verifies that with
// the option enabled, a webhook targeting the real loopback URL of an
// httptest.NewServer is delivered successfully and the receiver sees the
// payload.
func TestDeliver_WithAllowPrivateNetwork_DeliversToLoopback(t *testing.T) {
	t.Setenv(commons.EnvSecurityTier, commons.TierPermissive.String())

	var hitCount atomic.Int32

	srv := startLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))

	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "ep-loopback", URL: srv.URL, Secret: "", Active: true},
		},
	}

	d := NewDeliverer(lister,
		WithAllowPrivateNetwork(),
		WithMaxRetries(0),
	)
	require.NotNil(t, d)

	results := d.DeliverWithResults(context.Background(), newTestEvent())
	require.Len(t, results, 1, "expected exactly one delivery result")

	res := results[0]
	assert.True(t, res.Success,
		"delivery to %s must succeed when WithAllowPrivateNetwork is enabled (got error: %v)",
		srv.URL, res.Error)
	assert.Equal(t, http.StatusOK, res.StatusCode,
		"expected 200 OK from the loopback receiver")
	assert.NoError(t, res.Error,
		"successful delivery must carry a nil Error")
	assert.Equal(t, "ep-loopback", res.EndpointID)
	assert.EqualValues(t, 1, hitCount.Load(),
		"loopback server must have been hit exactly once")
}

// TestDeliver_WithoutAllowPrivateNetwork_BlocksLoopback is the regression
// guard for the default-strict contract: a Deliverer constructed WITHOUT
// WithAllowPrivateNetwork must reject the same loopback URL with
// ErrSSRFBlocked. If this test ever passes a successful delivery, the
// backward-compatibility contract has been broken.
func TestDeliver_WithoutAllowPrivateNetwork_BlocksLoopback(t *testing.T) {
	t.Parallel()

	var hitCount atomic.Int32

	srv := startLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))

	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "ep-blocked", URL: srv.URL, Secret: "", Active: true},
		},
	}

	// No WithAllowPrivateNetwork() — must inherit strict v5.1.0 behavior.
	d := NewDeliverer(lister, WithMaxRetries(0))
	require.NotNil(t, d)

	results := d.DeliverWithResults(context.Background(), newTestEvent())
	require.Len(t, results, 1)

	res := results[0]
	require.Error(t, res.Error,
		"strict default must reject loopback URL %s", srv.URL)
	assert.ErrorIs(t, res.Error, ErrSSRFBlocked,
		"default strict mode must surface ErrSSRFBlocked for loopback targets")
	assert.False(t, res.Success)
	assert.Equal(t, 0, res.StatusCode,
		"no HTTP request should have been issued — status code must remain zero")
	assert.EqualValues(t, 0, hitCount.Load(),
		"the loopback server must NOT have been contacted")
}

func TestDeliver_WithAllowPrivateNetwork_IPv6Loopback(t *testing.T) {
	t.Setenv(commons.EnvSecurityTier, commons.TierPermissive.String())

	tests := []struct {
		name      string
		withAllow bool
		wantHit   int32
		wantErr   bool
	}{
		{
			name:      "allowed with explicit private-network option",
			withAllow: true,
			wantHit:   1,
		},
		{
			name:    "blocked without explicit private-network option",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var hitCount atomic.Int32

			srv := startIPv6LoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hitCount.Add(1)
				w.WriteHeader(http.StatusOK)
			}))

			lister := &mockLister{
				endpoints: []Endpoint{
					{ID: "ep-ipv6-loopback", URL: srv.URL, Secret: "", Active: true},
				},
			}

			opts := []Option{WithMaxRetries(0)}
			if tt.withAllow {
				opts = append(opts, WithAllowPrivateNetwork())
			}

			d := NewDeliverer(lister, opts...)
			require.NotNil(t, d)

			results := d.DeliverWithResults(context.Background(), newTestEvent())
			require.Len(t, results, 1)

			res := results[0]
			assert.EqualValues(t, tt.wantHit, hitCount.Load())
			if tt.wantErr {
				require.Error(t, res.Error)
				assert.ErrorIs(t, res.Error, ErrSSRFBlocked)
				assert.False(t, res.Success)

				return
			}

			require.NoError(t, res.Error)
			assert.True(t, res.Success)
			assert.Equal(t, http.StatusOK, res.StatusCode)
		})
	}
}

// TestDeliver_WithAllowPrivateNetwork_StillBlocksHostnames verifies that the
// option only relaxes IP-range blocking — hostname-based blocking (cloud
// metadata endpoints, the bare "localhost" hostname) still produces
// ErrSSRFBlocked. This is the security envelope of WithAllowPrivateNetwork:
// the option must never accidentally re-enable cloud-metadata SSRF.
//
// Note on naming: the upstream prompt references "*.localhost" as a blocked
// suffix. In the canonical lib-commons hostname blocklist, the bare hostname
// "localhost" is exact-blocked (commons/security/ssrf/hostname.go), and the
// dangerous suffix is ".local" (mDNS / Bonjour). We test both the exact-match
// blocklist and the cloud-metadata blocklist — the union covers the prompt's
// intent that hostname blocking is preserved.
func TestDeliver_WithAllowPrivateNetwork_StillBlocksHostnames(t *testing.T) {
	t.Setenv(commons.EnvSecurityTier, commons.TierPermissive.String())

	tests := []struct {
		name string
		url  string
	}{
		{
			name: "bare localhost hostname remains blocked",
			url:  "http://localhost:9999/hook",
		},
		{
			name: "GCP cloud metadata hostname remains blocked",
			url:  "http://metadata.google.internal/computeMetadata/v1/",
		},
		{
			name: "Azure metadata hostname remains blocked",
			url:  "http://metadata.azure.internal/metadata/instance",
		},
		{
			name: "AWS metadata IP-as-hostname remains blocked",
			url:  "http://169.254.169.254/latest/meta-data/",
		},
		{
			name: ".local suffix (mDNS) remains blocked",
			url:  "http://my-service.local/hook",
		},
		{
			name: ".internal suffix remains blocked",
			url:  "http://my-service.internal/hook",
		},
		{
			name: ".cluster.local suffix (k8s internal DNS) remains blocked",
			url:  "http://svc.namespace.cluster.local/hook",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lister := &mockLister{
				endpoints: []Endpoint{
					{ID: "ep-hostname-blocked", URL: tt.url, Secret: "", Active: true},
				},
			}

			d := NewDeliverer(lister,
				WithAllowPrivateNetwork(), // option ON
				WithMaxRetries(0),
			)
			require.NotNil(t, d)

			results := d.DeliverWithResults(context.Background(), newTestEvent())
			require.Len(t, results, 1)

			res := results[0]
			require.Error(t, res.Error,
				"hostname-blocking must apply even when WithAllowPrivateNetwork is enabled (url=%s)",
				tt.url)
			assert.ErrorIs(t, res.Error, ErrSSRFBlocked,
				"hostname-blocked target must surface ErrSSRFBlocked (url=%s)", tt.url)
			assert.False(t, res.Success)
			assert.Equal(t, 0, res.StatusCode,
				"hostname blocking happens before HTTP; status code must remain zero (url=%s)", tt.url)
		})
	}
}

func TestResolveAndValidateWebhookTarget_PrivateOverrideDoesNotAllowHostnameResolvedPrivate(t *testing.T) {
	t.Parallel()

	_, _, _, err := resolveAndValidateWebhookTarget(
		context.Background(),
		"http://public-looking.example/hook",
		true,
		libSSRF.WithLookupFunc(func(context.Context, string) ([]string, error) {
			return []string{"10.0.0.7"}, nil
		}),
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSSRFBlocked,
		"private-network override must not allow public-looking hostnames that resolve private")
}

// TestResolveAndValidateIP_PassesOptionsThrough verifies that the package
// helper [resolveAndValidateIP] forwards its variadic libSSRF options to the
// underlying [libSSRF.ResolveAndValidate]. This is the seam that lets
// [Deliverer.ssrfOptions] influence behavior without changing the helper's
// 4-value return signature.
func TestResolveAndValidateIP_PassesOptionsThrough(t *testing.T) {
	t.Parallel()

	t.Run("loopback IP literal blocked without option", func(t *testing.T) {
		t.Parallel()

		_, _, _, err := resolveAndValidateIP(context.Background(), "http://127.0.0.1:9999/hook")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSSRFBlocked,
			"127.0.0.1 must be blocked when no libSSRF options are passed")
	})

	t.Run("loopback IP literal allowed with WithAllowPrivateNetwork", func(t *testing.T) {
		t.Parallel()

		pinnedURL, authority, sni, err := resolveAndValidateIP(
			context.Background(),
			"http://127.0.0.1:9999/hook",
			libSSRF.WithAllowPrivateNetwork(),
		)
		require.NoError(t, err,
			"WithAllowPrivateNetwork must allow 127.0.0.1 through the IP gate")
		assert.NotEmpty(t, pinnedURL,
			"pinned URL must be returned on success")
		assert.NotEmpty(t, authority,
			"authority (host:port) must be returned on success")
		// SNI hostname is empty when the input is already an IP literal —
		// no DNS rewrite happened, so TLS SNI is left to the HTTP client.
		_ = sni
	})

	t.Run("unique local IPv6 literal allowed with WithAllowPrivateNetwork", func(t *testing.T) {
		t.Parallel()

		pinnedURL, authority, _, err := resolveAndValidateIP(
			context.Background(),
			"http://[fd00::1]:9999/hook",
			libSSRF.WithAllowPrivateNetwork(),
		)
		require.NoError(t, err,
			"WithAllowPrivateNetwork must allow fd00::/8 IP literals through the IP gate")
		assert.NotEmpty(t, pinnedURL)
		assert.NotEmpty(t, authority)
	})
}
