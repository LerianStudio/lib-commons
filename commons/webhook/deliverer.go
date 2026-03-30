package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/LerianStudio/lib-commons/v4/commons/backoff"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/runtime"
)

// SignatureVersion controls the HMAC signing format used for X-Webhook-Signature.
type SignatureVersion int

const (
	// SignatureV0 produces legacy payload-only signatures: "sha256=<hex(HMAC(payload))>".
	// This is the default for backward compatibility with existing consumers.
	SignatureV0 SignatureVersion = iota

	// SignatureV1 produces versioned timestamp-bound signatures:
	// "v1,sha256=<hex(HMAC(v1:<timestamp>.<payload>))>".
	// The timestamp is included in the HMAC input to prevent replay attacks.
	// Receivers must verify freshness (e.g., reject timestamps older than 5 minutes).
	SignatureV1
)

// Defaults for Deliverer configuration.
const (
	defaultMaxConcurrency = 20
	defaultMaxRetries     = 3
	defaultBaseDelay      = time.Second
	defaultHTTPTimeout    = 10 * time.Second
	defaultMaxIdleConns   = 100
	defaultIdlePerHost    = 10
	defaultIdleTimeout    = 90 * time.Second
)

// Deliverer sends webhook events to registered endpoints with SSRF protection,
// HMAC-SHA256 signing, and exponential backoff retries.
//
// Create one with NewDeliverer and reuse it across the service lifetime —
// the internal HTTP client maintains a connection pool.
type Deliverer struct {
	lister     EndpointLister
	logger     log.Logger
	tracer     trace.Tracer
	metrics    DeliveryMetrics
	client     *http.Client
	decryptor  SecretDecryptor
	maxConc    int
	maxRetries int
	sigVersion SignatureVersion
}

// Option configures a Deliverer at construction time.
type Option func(*Deliverer)

// WithLogger attaches a structured logger. Nil values are ignored.
func WithLogger(l log.Logger) Option {
	return func(d *Deliverer) {
		if l != nil {
			d.logger = l
		}
	}
}

// WithTracer attaches an OpenTelemetry tracer for span creation. Nil values are ignored.
func WithTracer(t trace.Tracer) Option {
	return func(d *Deliverer) {
		if t != nil {
			d.tracer = t
		}
	}
}

// WithMetrics attaches a metrics recorder for delivery outcomes. Nil values are ignored.
func WithMetrics(m DeliveryMetrics) Option {
	return func(d *Deliverer) {
		if m != nil {
			d.metrics = m
		}
	}
}

// WithMaxConcurrency sets the maximum number of concurrent endpoint deliveries.
// Values ≤ 0 are ignored and the default (20) is used.
func WithMaxConcurrency(n int) Option {
	return func(d *Deliverer) {
		if n > 0 {
			d.maxConc = n
		}
	}
}

// WithMaxRetries sets the maximum number of retry attempts per endpoint.
// Values ≤ 0 are ignored and the default (3) is used.
func WithMaxRetries(n int) Option {
	return func(d *Deliverer) {
		if n > 0 {
			d.maxRetries = n
		}
	}
}

// WithHTTPClient replaces the default HTTP client. Use this to customize
// timeouts, TLS configuration, or proxy settings. Redirect blocking is
// always enforced regardless of the provided client's CheckRedirect
// setting to preserve SSRF protection.
func WithHTTPClient(c *http.Client) Option {
	return func(d *Deliverer) {
		if c != nil {
			clone := *c
			clone.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			}
			d.client = &clone
		}
	}
}

// WithSecretDecryptor sets a function for decrypting endpoint secrets that
// carry the "enc:" prefix. When nil, encrypted secrets cause delivery to
// be skipped with an error (fail-closed).
func WithSecretDecryptor(fn SecretDecryptor) Option {
	return func(d *Deliverer) {
		d.decryptor = fn
	}
}

// WithSignatureVersion selects the HMAC signing format for X-Webhook-Signature.
// The default is SignatureV0 (payload-only) for backward compatibility.
// SignatureV1 produces a versioned "v1,sha256=..." signature string that binds
// the event timestamp into the HMAC input, enabling replay protection.
// Receivers can enforce freshness using [VerifySignatureWithFreshness], or
// perform basic signature verification using [VerifySignature].
//
// Migration path: switch to SignatureV1 only after all consumers have been
// updated to verify the "v1,sha256=..." format.
func WithSignatureVersion(v SignatureVersion) Option {
	return func(d *Deliverer) {
		d.sigVersion = v
	}
}

// defaultHTTPClient creates an http.Client optimized for webhook delivery.
// Connection pooling avoids TCP+TLS handshake overhead on repeated deliveries
// to the same endpoint — critical at scale where hundreds of webhooks per
// second would otherwise exhaust ephemeral ports and TLS session caches.
func defaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: defaultHTTPTimeout,
		Transport: &http.Transport{
			MaxIdleConns:        defaultMaxIdleConns,
			MaxIdleConnsPerHost: defaultIdlePerHost,
			IdleConnTimeout:     defaultIdleTimeout,
		},
		// Block all redirects. Webhook endpoints must respond directly — following
		// redirects would bypass the SSRF pre-check on the initial URL, allowing
		// an attacker to 302 to internal addresses (e.g., cloud metadata services).
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// NewDeliverer creates a webhook deliverer that loads endpoints from lister.
// Functional options configure logging, tracing, concurrency, and retries.
// Returns nil when lister is nil — callers that hold a nil *Deliverer are
// safe because Deliver() and DeliverWithResults() already guard against a
// nil receiver and return ErrNilDeliverer / nil respectively.
func NewDeliverer(lister EndpointLister, opts ...Option) *Deliverer {
	if lister == nil {
		return nil
	}

	d := &Deliverer{
		lister:     lister,
		logger:     log.NewNop(),
		client:     defaultHTTPClient(),
		maxConc:    defaultMaxConcurrency,
		maxRetries: defaultMaxRetries,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}

	return d
}

// Deliver sends the event to all active endpoints concurrently.
// It returns an error only for pre-flight failures (nil deliverer, nil event,
// endpoint listing errors). Individual endpoint delivery failures are logged
// and recorded via metrics but do not cause Deliver to return an error.
func (d *Deliverer) Deliver(ctx context.Context, event *Event) error {
	if d == nil {
		return ErrNilDeliverer
	}

	if event == nil {
		return errors.New("webhook: nil event")
	}

	ctx, span := d.startSpan(ctx, "webhook.Deliver",
		attribute.String("webhook.event_type", event.Type),
	)
	defer span.End()

	endpoints, err := d.lister.ListActiveEndpoints(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "endpoint listing failed")

		return fmt.Errorf("webhook: list endpoints: %w", err)
	}

	// Defensive filter: EndpointLister contract guarantees active-only,
	// but guard against faulty implementations.
	active := filterActive(endpoints)
	if len(active) == 0 {
		d.log(ctx, log.LevelDebug, "no active endpoints for event",
			log.String("event_type", event.Type),
		)

		return nil
	}

	d.fanOut(ctx, active, event)

	return nil
}

// DeliverWithResults sends the event to all active endpoints and returns
// per-endpoint delivery results. Useful for callers that need to inspect
// or persist individual outcomes.
func (d *Deliverer) DeliverWithResults(ctx context.Context, event *Event) []DeliveryResult {
	if d == nil || event == nil {
		return nil
	}

	ctx, span := d.startSpan(ctx, "webhook.DeliverWithResults",
		attribute.String("webhook.event_type", event.Type),
	)
	defer span.End()

	endpoints, err := d.lister.ListActiveEndpoints(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "endpoint listing failed")

		return []DeliveryResult{{
			Error: fmt.Errorf("webhook: list endpoints: %w", err),
		}}
	}

	// Defensive filter: EndpointLister contract guarantees active-only,
	// but guard against faulty implementations.
	active := filterActive(endpoints)
	if len(active) == 0 {
		return nil
	}

	return d.fanOutWithResults(ctx, active, event)
}

// fanOut delivers to all endpoints concurrently, capped by the semaphore.
// Individual failures are logged but not collected. The call blocks until
// every goroutine has completed, preventing orphaned goroutines during
// graceful shutdown.
func (d *Deliverer) fanOut(ctx context.Context, endpoints []Endpoint, event *Event) {
	sem := make(chan struct{}, d.maxConc)

	var wg sync.WaitGroup

	for i := range endpoints {
		ep := endpoints[i]

		wg.Add(1)

		sem <- struct{}{}

		dlvCtx := context.WithoutCancel(ctx)

		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer runtime.RecoverWithPolicyAndContext(
				dlvCtx, d.logger, "webhook", "deliver-to-"+ep.ID, runtime.KeepRunning,
			)

			d.deliverToEndpoint(dlvCtx, ep, event)
		}()
	}

	wg.Wait()
}

// fanOutWithResults delivers to all endpoints and collects per-endpoint results.
func (d *Deliverer) fanOutWithResults(
	ctx context.Context,
	endpoints []Endpoint,
	event *Event,
) []DeliveryResult {
	sem := make(chan struct{}, d.maxConc)
	results := make([]DeliveryResult, len(endpoints))

	var wg sync.WaitGroup

	for i := range endpoints {
		ep := endpoints[i]
		idx := i

		wg.Add(1)

		sem <- struct{}{}

		dlvCtx := context.WithoutCancel(ctx)

		// Pre-populate so callers always see which endpoint was attempted,
		// even if deliverToEndpoint panics before writing the result.
		results[idx] = DeliveryResult{EndpointID: ep.ID}

		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer runtime.RecoverWithPolicyAndContext(
				dlvCtx, d.logger, "webhook", "deliver-to-"+ep.ID, runtime.KeepRunning,
			)

			results[idx] = d.deliverToEndpoint(dlvCtx, ep, event)
		}()
	}

	wg.Wait()

	return results
}

// deliverToEndpoint performs the SSRF check, DNS pinning, and retry loop
// for a single endpoint. Returns the delivery result.
func (d *Deliverer) deliverToEndpoint(
	ctx context.Context,
	ep Endpoint,
	event *Event,
) DeliveryResult {
	ctx, span := d.startSpan(ctx, "webhook.DeliverToEndpoint",
		attribute.String("webhook.endpoint_id", ep.ID),
	)
	defer span.End()

	result := DeliveryResult{EndpointID: ep.ID}

	// --- SSRF validation + DNS pinning (single lookup, eliminates TOCTOU) ---
	pinnedURL, originalAuthority, sniHostname, ssrfErr := resolveAndValidateIP(ctx, ep.URL)
	if ssrfErr != nil {
		span.RecordError(ssrfErr)
		span.SetStatus(codes.Error, "SSRF blocked")

		d.log(ctx, log.LevelError, "webhook delivery blocked by SSRF check",
			log.String("url", sanitizeURL(ep.URL)),
			log.Err(ssrfErr),
		)

		result.Error = fmt.Errorf("%w: %w", ErrSSRFBlocked, ssrfErr)

		return result
	}

	// --- Resolve signing secret once before the retry loop ---
	secret, secretErr := d.resolveSecret(ep.Secret)
	if secretErr != nil {
		span.RecordError(secretErr)
		span.SetStatus(codes.Error, "secret decryption failed")

		d.log(ctx, log.LevelError, "webhook secret decryption failed, skipping delivery",
			log.String("endpoint_id", ep.ID),
			log.Err(secretErr),
		)

		result.Error = secretErr

		return result
	}

	// --- Retry loop ---
	for attempt := range d.maxRetries + 1 {
		result.Attempts = attempt + 1

		if attempt > 0 {
			delay := backoff.ExponentialWithJitter(defaultBaseDelay, attempt-1)
			if err := backoff.WaitContext(ctx, delay); err != nil {
				result.Error = fmt.Errorf("webhook: context cancelled during backoff: %w", err)

				return result
			}
		}

		statusCode, err := d.doHTTP(ctx, pinnedURL, originalAuthority, sniHostname, event, secret)
		result.StatusCode = statusCode

		if err != nil {
			d.log(ctx, log.LevelWarn, "webhook delivery failed",
				log.String("url", sanitizeURL(ep.URL)),
				log.Int("attempt", attempt+1),
				log.Err(err),
			)

			continue
		}

		if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices {
			result.Success = true
			d.recordMetrics(ctx, ep.ID, true, statusCode, result.Attempts)

			d.log(ctx, log.LevelInfo, "webhook delivered",
				log.String("url", sanitizeURL(ep.URL)),
				log.String("event_type", event.Type),
				log.Int("status", statusCode),
			)

			return result
		}

		// Non-retryable client errors — break immediately (except 429 Too Many Requests).
		if statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError && statusCode != http.StatusTooManyRequests {
			result.Error = fmt.Errorf("webhook: non-retryable status %d", statusCode)
			d.recordMetrics(ctx, ep.ID, false, statusCode, result.Attempts)

			return result
		}

		d.log(ctx, log.LevelWarn, "webhook non-2xx response",
			log.String("url", sanitizeURL(ep.URL)),
			log.Int("status", statusCode),
			log.Int("attempt", attempt+1),
		)
	}

	// Exhausted all retries.
	result.Error = fmt.Errorf("%w: exhausted %d attempts for %s", ErrDeliveryFailed, d.maxRetries+1, sanitizeURL(ep.URL))
	d.recordMetrics(ctx, ep.ID, false, result.StatusCode, result.Attempts)

	span.RecordError(result.Error)
	span.SetStatus(codes.Error, "delivery exhausted retries")

	d.log(ctx, log.LevelError, "webhook delivery exhausted retries",
		log.String("url", sanitizeURL(ep.URL)),
		log.String("event_type", event.Type),
		log.Int("attempts", result.Attempts),
	)

	return result
}

// doHTTP builds and executes a single HTTP request to the (possibly pinned) URL.
// Returns the status code and any transport-level error.
//
// originalAuthority is the full host:port authority from the original URL, used
// as the HTTP Host header to preserve explicit non-default ports.
// sniHostname is the bare hostname (port stripped), used for TLS SNI and
// certificate verification when the URL has been rewritten to a pinned IP.
func (d *Deliverer) doHTTP(
	ctx context.Context,
	pinnedURL string,
	originalAuthority string,
	sniHostname string,
	event *Event,
	secret string,
) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pinnedURL, bytes.NewReader(event.Payload))
	if err != nil {
		return 0, fmt.Errorf("webhook: build request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Event", event.Type)
	req.Header.Set("X-Webhook-Timestamp", strconv.FormatInt(event.Timestamp, 10))

	// When the URL was rewritten to use the pinned IP, set the Host header
	// with the original authority (host:port) for virtual hosting, and use
	// TLSClientConfig.ServerName with the bare hostname for TLS SNI and
	// certificate verification (not the IP).
	if originalAuthority != "" {
		req.Host = originalAuthority
	}

	if secret != "" {
		sig := d.computeSignature(event.Payload, event.Timestamp, secret)
		req.Header.Set("X-Webhook-Signature", sig)
	}

	client := d.client

	if sniHostname != "" && strings.HasPrefix(pinnedURL, "https://") {
		client = d.httpsClientForPinnedIP(sniHostname)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("webhook: http request: %w", err)
	}

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()

	return resp.StatusCode, nil
}

// resolveSecret decrypts the endpoint secret if it carries the "enc:" prefix.
// Plaintext secrets and empty strings pass through unchanged.
func (d *Deliverer) resolveSecret(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	if !strings.HasPrefix(raw, "enc:") {
		return raw, nil
	}

	if d.decryptor == nil {
		return "", errors.New("webhook: encrypted secret but no decryptor configured")
	}

	plaintext, err := d.decryptor(raw[4:]) // strip "enc:" prefix
	if err != nil {
		return "", fmt.Errorf("webhook: decrypt secret: %w", err)
	}

	return plaintext, nil
}

// computeSignature dispatches to the appropriate HMAC format based on the
// configured signature version.
func (d *Deliverer) computeSignature(payload []byte, timestamp int64, secret string) string {
	switch d.sigVersion {
	case SignatureV1:
		return computeHMACv1(payload, timestamp, secret)
	default:
		return "sha256=" + computeHMAC(payload, secret)
	}
}

// computeHMAC returns the hex-encoded HMAC-SHA256 of payload using the given secret.
// This is the legacy (v0) format that signs the raw payload only.
//
// Design note — timestamp not included in v0 signature (by intent):
// The signature covers the raw payload only, not the X-Webhook-Timestamp value.
// This format is maintained for backward compatibility. New deployments should
// prefer SignatureV1 (via WithSignatureVersion) which binds the timestamp into
// the HMAC input to enable replay protection.
func computeHMAC(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)

	return hex.EncodeToString(mac.Sum(nil))
}

// computeHMACv1 returns a versioned signature string:
//
//	"v1,sha256=<hex(HMAC-SHA256("v1:<timestamp>.<payload>", secret))>"
//
// The version prefix "v1:" followed by the decimal timestamp and a dot separator
// are prepended to the payload before computing the HMAC, binding the timestamp
// to the signature. Receivers must parse the "v1," prefix, extract the timestamp
// from the X-Webhook-Timestamp header, reconstruct the signing input, and verify
// the HMAC before accepting the webhook.
func computeHMACv1(payload []byte, timestamp int64, secret string) string {
	ts := strconv.FormatInt(timestamp, 10)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v1:"))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(payload)

	return "v1,sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature verifies a webhook signature by auto-detecting the version
// from the signature string format. Returns nil on valid signature, or an error
// describing the mismatch.
//
// Supported formats:
//   - "sha256=<hex>"          — v0 (payload-only)
//   - "v1,sha256=<hex>"       — v1 (timestamp-bound)
//
// For v1 signatures, the timestamp parameter is required to reconstruct the
// signing input. For v0 signatures, the timestamp is ignored.
func VerifySignature(payload []byte, timestamp int64, secret, signature string) error {
	switch {
	case strings.HasPrefix(signature, "v1,"):
		expected := computeHMACv1(payload, timestamp, secret)
		if !hmac.Equal([]byte(signature), []byte(expected)) {
			return errors.New("webhook: v1 signature mismatch")
		}

		return nil

	case strings.HasPrefix(signature, "sha256="):
		expected := "sha256=" + computeHMAC(payload, secret)
		if !hmac.Equal([]byte(signature), []byte(expected)) {
			return errors.New("webhook: v0 signature mismatch")
		}

		return nil

	default:
		return errors.New("webhook: unrecognized signature format")
	}
}

// VerifySignatureWithFreshness verifies a v1 webhook signature and additionally
// checks that the timestamp is within the given tolerance window from now.
// This provides replay protection: even if an attacker captures a valid
// payload+signature pair, it becomes invalid after the tolerance window expires.
//
// For v0 ("sha256=...") signatures, freshness cannot be enforced because the
// timestamp is not covered by the HMAC. Callers receiving v0 signatures should
// use VerifySignature and implement replay protection independently (e.g.,
// idempotency keys or event-ID tracking).
func VerifySignatureWithFreshness(payload []byte, timestamp int64, secret, signature string, tolerance time.Duration) error {
	if err := VerifySignature(payload, timestamp, secret, signature); err != nil {
		return err
	}

	// Freshness check only applies to v1 where the timestamp is signed.
	if strings.HasPrefix(signature, "v1,") {
		eventTime := time.Unix(timestamp, 0)
		delta := time.Since(eventTime)

		if delta < 0 {
			delta = -delta
		}

		if delta > tolerance {
			return fmt.Errorf("webhook: timestamp outside tolerance window (%s > %s)", delta.Truncate(time.Second), tolerance)
		}
	}

	return nil
}

// filterActive returns only endpoints where Active is true.
func filterActive(endpoints []Endpoint) []Endpoint {
	active := make([]Endpoint, 0, len(endpoints))

	for i := range endpoints {
		if endpoints[i].Active {
			active = append(active, endpoints[i])
		}
	}

	return active
}

// startSpan creates an OTel span if a tracer is configured, or returns a
// no-op span otherwise.
func (d *Deliverer) startSpan(
	ctx context.Context,
	name string,
	attrs ...attribute.KeyValue,
) (context.Context, trace.Span) {
	if d.tracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}

	ctx, span := d.tracer.Start(ctx, name, trace.WithAttributes(attrs...)) //nolint:spancheck // span.End is called by the caller

	return ctx, span //nolint:spancheck // callers defer span.End() immediately after startSpan
}

// log emits a structured log entry if a logger is configured.
func (d *Deliverer) log(ctx context.Context, level log.Level, msg string, fields ...log.Field) {
	if d.logger == nil {
		return
	}

	d.logger.Log(ctx, level, msg, fields...)
}

// recordMetrics delegates to the configured DeliveryMetrics, if any.
func (d *Deliverer) recordMetrics(ctx context.Context, endpointID string, success bool, statusCode, attempts int) {
	if d.metrics == nil {
		return
	}

	d.metrics.RecordDelivery(ctx, endpointID, success, statusCode, attempts)
}

// httpsClientForPinnedIP returns an HTTP client whose TLS config uses the
// given hostname for SNI and certificate verification. This is necessary
// when the request URL has been rewritten to an IP address for DNS pinning
// (SSRF protection) — without this, Go would try to verify the TLS cert
// against the IP, which fails for hostname-based certificates.
func (d *Deliverer) httpsClientForPinnedIP(originalHost string) *http.Client {
	baseTransport := d.client.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}

	transport, ok := baseTransport.(*http.Transport)
	if !ok {
		// Non-standard transport — fall back to the default client and let
		// the caller's transport handle TLS.
		return d.client
	}

	pinned := transport.Clone()
	if pinned.TLSClientConfig == nil {
		pinned.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else if pinned.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		pinned.TLSClientConfig.MinVersion = tls.VersionTLS12
	}

	pinned.TLSClientConfig.ServerName = originalHost

	clone := *d.client
	clone.Transport = pinned

	return &clone
}

// sanitizeURL strips query parameters and userinfo from a URL before logging
// to prevent credential leakage. Webhook URLs may carry tokens in query params
// (e.g., ?token=..., ?api_key=...) or credentials in the userinfo component
// (e.g., https://user:pass@host/...) that must not appear in log output.
// On parse failure a safe placeholder is returned instead of the raw input
// to avoid leaking credentials embedded in malformed URLs.
func sanitizeURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "[invalid-url]"
	}

	u.RawQuery = ""
	u.User = nil
	u.Fragment = ""

	return u.String()
}
