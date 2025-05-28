package observability

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHTTPClientMiddleware(t *testing.T) {
	ctx := context.Background()

	t.Run("with nil provider", func(t *testing.T) {
		middleware := NewHTTPClientMiddleware(nil)
		require.NotNil(t, middleware)

		client := &http.Client{
			Transport: middleware(http.DefaultTransport),
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		}))
		defer server.Close()

		req, err := http.NewRequest("GET", server.URL, nil)
		require.NoError(t, err)

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("with enabled provider", func(t *testing.T) {
		provider, err := New(ctx,
			WithServiceName("test-http-client"),
			WithComponentEnabled(true, true, true),
		)
		require.NoError(t, err)
		defer provider.Shutdown(ctx)

		middleware := NewHTTPClientMiddleware(provider)
		require.NotNil(t, middleware)

		client := &http.Client{
			Transport: middleware(http.DefaultTransport),
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		}))
		defer server.Close()

		req, err := http.NewRequest("GET", server.URL, nil)
		require.NoError(t, err)

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("with disabled provider", func(t *testing.T) {
		provider, err := New(ctx,
			WithServiceName("test-http-client"),
			WithComponentEnabled(false, false, false),
		)
		require.NoError(t, err)
		defer provider.Shutdown(ctx)

		middleware := NewHTTPClientMiddleware(provider)
		require.NotNil(t, middleware)

		client := &http.Client{
			Transport: middleware(http.DefaultTransport),
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		}))
		defer server.Close()

		req, err := http.NewRequest("GET", server.URL, nil)
		require.NoError(t, err)

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestHTTPClientOptions(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-http-client"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	t.Run("WithIgnoreHeaders", func(t *testing.T) {
		t.Run("valid headers", func(t *testing.T) {
			middleware := NewHTTPClientMiddleware(provider,
				WithIgnoreHeaders("x-custom-header", "x-another-header"),
			)
			require.NotNil(t, middleware)
		})

		t.Run("empty headers", func(t *testing.T) {
			middleware := NewHTTPClientMiddleware(provider,
				WithIgnoreHeaders(),
			)
			require.NotNil(t, middleware)
		})

		t.Run("duplicate headers", func(t *testing.T) {
			middleware := NewHTTPClientMiddleware(provider,
				WithIgnoreHeaders("authorization", "Authorization", "AUTHORIZATION"),
			)
			require.NotNil(t, middleware)
		})
	})

	t.Run("WithIgnorePaths", func(t *testing.T) {
		t.Run("valid paths", func(t *testing.T) {
			middleware := NewHTTPClientMiddleware(provider,
				WithIgnorePaths("/health", "/metrics"),
			)
			require.NotNil(t, middleware)
		})

		t.Run("empty paths", func(t *testing.T) {
			middleware := NewHTTPClientMiddleware(provider,
				WithIgnorePaths(),
			)
			require.NotNil(t, middleware)
		})
	})

	t.Run("WithMaskedParams", func(t *testing.T) {
		t.Run("valid params", func(t *testing.T) {
			middleware := NewHTTPClientMiddleware(provider,
				WithMaskedParams("secret", "password"),
			)
			require.NotNil(t, middleware)
		})

		t.Run("empty params", func(t *testing.T) {
			middleware := NewHTTPClientMiddleware(provider,
				WithMaskedParams(),
			)
			require.NotNil(t, middleware)
		})
	})

	t.Run("WithHideRequestBody", func(t *testing.T) {
		middleware := NewHTTPClientMiddleware(provider,
			WithHideRequestBody(true),
		)
		require.NotNil(t, middleware)
	})

	t.Run("WithDefaultSensitiveHeaders", func(t *testing.T) {
		middleware := NewHTTPClientMiddleware(provider,
			WithDefaultSensitiveHeaders(),
		)
		require.NotNil(t, middleware)
	})

	t.Run("WithDefaultSensitiveParams", func(t *testing.T) {
		middleware := NewHTTPClientMiddleware(provider,
			WithDefaultSensitiveParams(),
		)
		require.NotNil(t, middleware)
	})

	t.Run("WithSecurityDefaults", func(t *testing.T) {
		middleware := NewHTTPClientMiddleware(provider,
			WithSecurityDefaults(),
		)
		require.NotNil(t, middleware)
	})

	t.Run("multiple options", func(t *testing.T) {
		middleware := NewHTTPClientMiddleware(provider,
			WithIgnoreHeaders("x-custom"),
			WithIgnorePaths("/health"),
			WithMaskedParams("secret"),
			WithHideRequestBody(true),
		)
		require.NotNil(t, middleware)
	})
}

func TestHTTPClientMiddlewareRequestHandling(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-http-client"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	t.Run("successful request", func(t *testing.T) {
		middleware := NewHTTPClientMiddleware(provider)
		client := &http.Client{
			Transport: middleware(http.DefaultTransport),
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status": "ok"}`))
		}))
		defer server.Close()

		req, err := http.NewRequest("GET", server.URL+"/api/test", nil)
		require.NoError(t, err)
		req.Header.Set("User-Agent", "test-client")

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	})

	t.Run("error response", func(t *testing.T) {
		middleware := NewHTTPClientMiddleware(provider)
		client := &http.Client{
			Transport: middleware(http.DefaultTransport),
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Internal Server Error"))
		}))
		defer server.Close()

		req, err := http.NewRequest("POST", server.URL+"/api/error", nil)
		require.NoError(t, err)

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	})

	t.Run("with custom headers", func(t *testing.T) {
		middleware := NewHTTPClientMiddleware(provider,
			WithIgnoreHeaders("authorization"),
		)
		client := &http.Client{
			Transport: middleware(http.DefaultTransport),
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer token123", r.Header.Get("Authorization"))
			assert.Equal(t, "custom-value", r.Header.Get("X-Custom-Header"))
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		req, err := http.NewRequest("GET", server.URL, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer token123")
		req.Header.Set("X-Custom-Header", "custom-value")

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestHTTPClientMiddlewareIgnorePaths(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-http-client"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	middleware := NewHTTPClientMiddleware(provider,
		WithIgnorePaths("/health", "/metrics"),
	)
	client := &http.Client{
		Transport: middleware(http.DefaultTransport),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	tests := []struct {
		path    string
		ignored bool
	}{
		{"/health", true},
		{"/health/check", true},
		{"/metrics", true},
		{"/metrics/prometheus", true},
		{"/api/users", false},
		{"/status", false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("path %s", tt.path), func(t *testing.T) {
			req, err := http.NewRequest("GET", server.URL+tt.path, nil)
			require.NoError(t, err)

			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
		})
	}
}

func TestHTTPClientMiddlewareSanitizeURL(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-http-client"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	middleware := NewHTTPClientMiddleware(provider,
		WithMaskedParams("secret", "password", "token"),
	)
	client := &http.Client{
		Transport: middleware(http.DefaultTransport),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tests := []struct {
		name string
		url  string
	}{
		{
			name: "with secret parameter",
			url:  server.URL + "/api?secret=mysecret&other=value",
		},
		{
			name: "with password parameter",
			url:  server.URL + "/api?password=mypassword",
		},
		{
			name: "with token parameter",
			url:  server.URL + "/api?token=mytoken&id=123",
		},
		{
			name: "without sensitive parameters",
			url:  server.URL + "/api?id=123&name=test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", tt.url, nil)
			require.NoError(t, err)

			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
		})
	}
}

func TestHTTPClientMiddlewareTracing(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-http-client"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	middleware := NewHTTPClientMiddleware(provider)
	client := &http.Client{
		Transport: middleware(http.DefaultTransport),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Traceparent"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, span := provider.Tracer().Start(ctx, "test-operation")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/test", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHTTPClientMiddlewareMetrics(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-http-client"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	middleware := NewHTTPClientMiddleware(provider)
	client := &http.Client{
		Transport: middleware(http.DefaultTransport),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	req, err := http.NewRequest("GET", server.URL+"/api/test", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHTTPClientMiddlewareErrorHandling(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-http-client"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	middleware := NewHTTPClientMiddleware(provider)
	client := &http.Client{
		Transport: middleware(http.DefaultTransport),
		Timeout:   1 * time.Millisecond,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	req, err := http.NewRequest("GET", server.URL, nil)
	require.NoError(t, err)

	_, err = client.Do(req)
	assert.Error(t, err)
}

func TestHTTPClientMiddlewareSecurityDefaults(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-http-client"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	middleware := NewHTTPClientMiddleware(provider,
		WithSecurityDefaults(),
	)
	client := &http.Client{
		Transport: middleware(http.DefaultTransport),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "session=abc123")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	req, err := http.NewRequest("GET", server.URL+"?access_token=secret123", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer token123")
	req.Header.Set("X-API-Key", "apikey123")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestIsIgnoredHeader(t *testing.T) {
	m := &httpClientMiddleware{
		ignoreHeaders: []string{"authorization", "cookie", "x-api-key"},
	}

	tests := []struct {
		header   string
		expected bool
	}{
		{"authorization", true},
		{"Authorization", true},
		{"AUTHORIZATION", true},
		{"cookie", true},
		{"Cookie", true},
		{"x-api-key", true},
		{"X-API-Key", true},
		{"content-type", false},
		{"user-agent", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("header %s", tt.header), func(t *testing.T) {
			result := m.isIgnoredHeader(tt.header)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSanitizeURL(t *testing.T) {
	m := &httpClientMiddleware{
		maskedParams: []string{"secret", "password", "token"},
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no sensitive params",
			input:    "https://api.example.com/users?id=123&name=test",
			expected: "https://api.example.com/users?id=123&name=test",
		},
		{
			name:     "with secret param",
			input:    "https://api.example.com/auth?secret=mysecret&id=123",
			expected: "https://api.example.com/auth?secret=[REDACTED]&id=123",
		},
		{
			name:     "with password param",
			input:    "https://api.example.com/login?password=mypassword",
			expected: "https://api.example.com/login?password=[REDACTED]",
		},
		{
			name:     "with token param at end",
			input:    "https://api.example.com/api?id=123&token=mytoken",
			expected: "https://api.example.com/api?id=123&token=[REDACTED]",
		},
		{
			name:     "with multiple sensitive params",
			input:    "https://api.example.com/api?secret=sec&password=pass&token=tok",
			expected: "https://api.example.com/api?secret=[REDACTED]&password=[REDACTED]&token=[REDACTED]",
		},
		{
			name:     "with fragment",
			input:    "https://api.example.com/api?secret=mysecret#section",
			expected: "https://api.example.com/api?secret=[REDACTED]#section",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := m.sanitizeURL(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTLSVersionString(t *testing.T) {
	tests := []struct {
		version  uint16
		expected string
	}{
		{0x0301, "TLS 1.0"},
		{0x0302, "TLS 1.1"},
		{0x0303, "TLS 1.2"},
		{0x0304, "TLS 1.3"},
		{0x9999, "unknown (0x9999)"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("version 0x%04x", tt.version), func(t *testing.T) {
			result := tlsVersionString(tt.version)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTLSCipherSuiteString(t *testing.T) {
	tests := []struct {
		cipherSuite uint16
		expected    string
	}{
		{0x1301, "TLS_AES_128_GCM_SHA256"},
		{0x1302, "TLS_AES_256_GCM_SHA384"},
		{0x1303, "TLS_CHACHA20_POLY1305_SHA256"},
		{0xc02b, "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"},
		{0xc02c, "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384"},
		{0xc02f, "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"},
		{0xc030, "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384"},
		{0x9999, "unknown (0x9999)"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("cipher suite 0x%04x", tt.cipherSuite), func(t *testing.T) {
			result := tlsCipherSuiteString(tt.cipherSuite)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRoundTripperFunc(t *testing.T) {
	called := false
	fn := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
		}, nil
	})

	req, err := http.NewRequest("GET", "http://example.com", nil)
	require.NoError(t, err)

	resp, err := fn.RoundTrip(req)
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHTTPClientMiddlewareIntegration(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-integration"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	middleware := NewHTTPClientMiddleware(provider,
		WithSecurityDefaults(),
		WithIgnorePaths("/health"),
	)

	client := &http.Client{
		Transport: middleware(http.DefaultTransport),
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		case "/api/users":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id": 1, "name": "John"}]`))
		case "/api/error":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Internal Server Error"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client.Transport = middleware(client.Transport)

	tests := []struct {
		name           string
		path           string
		expectedStatus int
	}{
		{"health check", "/health", http.StatusOK},
		{"api users", "/api/users", http.StatusOK},
		{"api error", "/api/error", http.StatusInternalServerError},
		{"not found", "/api/notfound", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(ctx, "GET", server.URL+tt.path, nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer secret-token")

			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tt.expectedStatus, resp.StatusCode)
		})
	}
}

func BenchmarkHTTPClientMiddleware(b *testing.B) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("bench-http-client"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(b, err)
	defer provider.Shutdown(ctx)

	middleware := NewHTTPClientMiddleware(provider)
	client := &http.Client{
		Transport: middleware(http.DefaultTransport),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req, _ := http.NewRequest("GET", server.URL, nil)
			resp, err := client.Do(req)
			if err != nil {
				b.Fatal(err)
			}
			resp.Body.Close()
		}
	})
}
