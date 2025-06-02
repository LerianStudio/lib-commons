package observability

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"
)

// ServiceMetadata contains service-level metadata for context enrichment
type ServiceMetadata struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Environment string            `json:"environment"`
	Namespace   string            `json:"namespace"`
	Team        string            `json:"team"`
	Owner       string            `json:"owner"`
	Region      string            `json:"region"`
	Zone        string            `json:"zone"`
	Instance    string            `json:"instance"`
	Hostname    string            `json:"hostname"`
	Process     ProcessMetadata   `json:"process"`
	Runtime     RuntimeMetadata   `json:"runtime"`
	Custom      map[string]string `json:"custom"`
}

// ProcessMetadata contains process-level information
type ProcessMetadata struct {
	PID         int       `json:"pid"`
	StartTime   time.Time `json:"start_time"`
	ExecutePath string    `json:"execute_path"`
	WorkingDir  string    `json:"working_dir"`
	Args        []string  `json:"args"`
}

// RuntimeMetadata contains runtime information
type RuntimeMetadata struct {
	Language     string `json:"language"`
	Version      string `json:"version"`
	Platform     string `json:"platform"`
	Compiler     string `json:"compiler"`
	NumCPU       int    `json:"num_cpu"`
	NumGoroutine int    `json:"num_goroutine"`
}

// RequestMetadata contains request-specific metadata
type RequestMetadata struct {
	ID               string            `json:"id"`
	Method           string            `json:"method"`
	Path             string            `json:"path"`
	UserAgent        string            `json:"user_agent"`
	RemoteAddr       string            `json:"remote_addr"`
	Headers          map[string]string `json:"headers"`
	ContentType      string            `json:"content_type"`
	ContentLength    int64             `json:"content_length"`
	AcceptLanguage   string            `json:"accept_language"`
	AcceptEncoding   string            `json:"accept_encoding"`
	Referer          string            `json:"referer"`
	Origin           string            `json:"origin"`
	ForwardedFor     string            `json:"forwarded_for"`
	RealIP           string            `json:"real_ip"`
	SessionID        string            `json:"session_id"`
	UserID           string            `json:"user_id"`
	TenantID         string            `json:"tenant_id"`
	TraceParent      string            `json:"trace_parent"`
	TraceState       string            `json:"trace_state"`
	CorrelationID    string            `json:"correlation_id"`
	RequestStartTime time.Time         `json:"request_start_time"`
	Custom           map[string]string `json:"custom"`
}

// BusinessMetadata contains business logic context
type BusinessMetadata struct {
	Operation     string            `json:"operation"`
	Resource      string            `json:"resource"`
	Action        string            `json:"action"`
	Entity        string            `json:"entity"`
	EntityID      string            `json:"entity_id"`
	Feature       string            `json:"feature"`
	Experiment    string            `json:"experiment"`
	Cohort        string            `json:"cohort"`
	Campaign      string            `json:"campaign"`
	Source        string            `json:"source"`
	Medium        string            `json:"medium"`
	Channel       string            `json:"channel"`
	Partner       string            `json:"partner"`
	ClientType    string            `json:"client_type"`
	ClientVersion string            `json:"client_version"`
	Custom        map[string]string `json:"custom"`
}

// SecurityMetadata contains security-related context
type SecurityMetadata struct {
	AuthMethod      string            `json:"auth_method"`
	AuthProvider    string            `json:"auth_provider"`
	Permissions     []string          `json:"permissions"`
	Roles           []string          `json:"roles"`
	SecurityLevel   string            `json:"security_level"`
	ThreatScore     float64           `json:"threat_score"`
	RiskLevel       string            `json:"risk_level"`
	ComplianceFlags []string          `json:"compliance_flags"`
	AuditRequired   bool              `json:"audit_required"`
	PII             bool              `json:"pii"`
	Sensitive       bool              `json:"sensitive"`
	Custom          map[string]string `json:"custom"`
}

// EnrichmentConfig configures context enrichment behavior
type EnrichmentConfig struct {
	EnableServiceMetadata  bool              `json:"enable_service_metadata"`
	EnableRequestMetadata  bool              `json:"enable_request_metadata"`
	EnableBusinessMetadata bool              `json:"enable_business_metadata"`
	EnableSecurityMetadata bool              `json:"enable_security_metadata"`
	EnableRuntimeMetadata  bool              `json:"enable_runtime_metadata"`
	AutoEnrichSpans        bool              `json:"auto_enrich_spans"`
	AutoEnrichLogs         bool              `json:"auto_enrich_logs"`
	AutoEnrichBaggage      bool              `json:"auto_enrich_baggage"`
	MaxCustomAttributes    int               `json:"max_custom_attributes"`
	AttributePrefix        string            `json:"attribute_prefix"`
	HeadersToCapture       []string          `json:"headers_to_capture"`
	HeadersToRedact        []string          `json:"headers_to_redact"`
	SensitiveFields        []string          `json:"sensitive_fields"`
	CustomEnrichers        []ContextEnricher `json:"-"`
}

// ContextEnricher defines the interface for custom context enrichers
type ContextEnricher interface {
	EnrichContext(ctx context.Context, metadata map[string]interface{}) context.Context
	Name() string
	Priority() int
}

// ContextEnrichmentManager manages context enrichment
type ContextEnrichmentManager struct {
	config          EnrichmentConfig
	serviceMetadata *ServiceMetadata
	enrichers       []ContextEnricher
}

// NewContextEnrichmentManager creates a new context enrichment manager
func NewContextEnrichmentManager(config EnrichmentConfig) *ContextEnrichmentManager {
	manager := &ContextEnrichmentManager{
		config:    config,
		enrichers: make([]ContextEnricher, 0),
	}

	// Initialize service metadata
	if config.EnableServiceMetadata {
		manager.serviceMetadata = manager.buildServiceMetadata()
	}

	// Add custom enrichers
	for _, enricher := range config.CustomEnrichers {
		manager.AddEnricher(enricher)
	}

	return manager
}

// AddEnricher adds a custom context enricher
func (cem *ContextEnrichmentManager) AddEnricher(enricher ContextEnricher) {
	cem.enrichers = append(cem.enrichers, enricher)

	// Sort by priority (higher priority first)
	for i := len(cem.enrichers) - 1; i > 0; i-- {
		if cem.enrichers[i].Priority() > cem.enrichers[i-1].Priority() {
			cem.enrichers[i], cem.enrichers[i-1] = cem.enrichers[i-1], cem.enrichers[i]
		}
	}
}

// EnrichContext enriches a context with observability metadata
func (cem *ContextEnrichmentManager) EnrichContext(
	ctx context.Context,
	options ...EnrichmentOption,
) context.Context {
	// Apply enrichment options
	opts := &enrichmentOptions{}
	for _, option := range options {
		option(opts)
	}

	enrichedCtx := ctx

	// Enrich with service metadata
	if cem.config.EnableServiceMetadata && cem.serviceMetadata != nil {
		enrichedCtx = cem.enrichWithServiceMetadata(enrichedCtx)
	}

	// Enrich with request metadata
	if cem.config.EnableRequestMetadata && opts.request != nil {
		enrichedCtx = cem.enrichWithRequestMetadata(enrichedCtx, opts.request)
	}

	// Enrich with business metadata
	if cem.config.EnableBusinessMetadata && opts.business != nil {
		enrichedCtx = cem.enrichWithBusinessMetadata(enrichedCtx, opts.business)
	}

	// Enrich with security metadata
	if cem.config.EnableSecurityMetadata && opts.security != nil {
		enrichedCtx = cem.enrichWithSecurityMetadata(enrichedCtx, opts.security)
	}

	// Enrich with runtime metadata
	if cem.config.EnableRuntimeMetadata {
		enrichedCtx = cem.enrichWithRuntimeMetadata(enrichedCtx)
	}

	// Apply custom enrichers
	metadata := make(map[string]interface{})
	if opts.custom != nil {
		metadata = opts.custom
	}

	for _, enricher := range cem.enrichers {
		enrichedCtx = enricher.EnrichContext(enrichedCtx, metadata)
	}

	return enrichedCtx
}

// EnrichSpan enriches a span with metadata from context
func (cem *ContextEnrichmentManager) EnrichSpan(ctx context.Context, span trace.Span) {
	if !cem.config.AutoEnrichSpans {
		return
	}

	// Add service metadata as span attributes
	if cem.config.EnableServiceMetadata && cem.serviceMetadata != nil {
		attrs := cem.serviceMetadataToAttributes()
		span.SetAttributes(attrs...)
	}

	// Add request metadata from context
	if reqMeta := GetRequestMetadataFromContext(ctx); reqMeta != nil {
		attrs := cem.requestMetadataToAttributes(reqMeta)
		span.SetAttributes(attrs...)
	}

	// Add business metadata from context
	if bizMeta := GetBusinessMetadataFromContext(ctx); bizMeta != nil {
		attrs := cem.businessMetadataToAttributes(bizMeta)
		span.SetAttributes(attrs...)
	}

	// Add security metadata (filtered for sensitivity)
	if secMeta := GetSecurityMetadataFromContext(ctx); secMeta != nil {
		attrs := cem.securityMetadataToAttributes(secMeta)
		span.SetAttributes(attrs...)
	}
}

// EnrichFromHTTPRequest enriches context from an HTTP request
func (cem *ContextEnrichmentManager) EnrichFromHTTPRequest(
	ctx context.Context,
	req *http.Request,
) context.Context {
	if !cem.config.EnableRequestMetadata {
		return ctx
	}

	requestMeta := cem.extractRequestMetadata(req)
	return cem.EnrichContext(ctx, WithRequestMetadata(requestMeta))
}

// buildServiceMetadata constructs service metadata from environment
func (cem *ContextEnrichmentManager) buildServiceMetadata() *ServiceMetadata {
	hostname, _ := os.Hostname()
	wd, _ := os.Getwd()

	return &ServiceMetadata{
		Name:        getEnvOrDefault("SERVICE_NAME", "unknown"),
		Version:     getEnvOrDefault("SERVICE_VERSION", "unknown"),
		Environment: getEnvOrDefault("ENVIRONMENT", "unknown"),
		Namespace:   getEnvOrDefault("NAMESPACE", "default"),
		Team:        getEnvOrDefault("TEAM", "unknown"),
		Owner:       getEnvOrDefault("OWNER", "unknown"),
		Region:      getEnvOrDefault("AWS_REGION", getEnvOrDefault("GCP_REGION", "unknown")),
		Zone: getEnvOrDefault(
			"AWS_AVAILABILITY_ZONE",
			getEnvOrDefault("GCP_ZONE", "unknown"),
		),
		Instance: getEnvOrDefault("INSTANCE_ID", hostname),
		Hostname: hostname,
		Process: ProcessMetadata{
			PID:         os.Getpid(),
			StartTime:   time.Now(), // This would be captured at startup
			ExecutePath: getExecutablePath(),
			WorkingDir:  wd,
			Args:        os.Args,
		},
		Runtime: RuntimeMetadata{
			Language:     "go",
			Version:      runtime.Version(),
			Platform:     fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
			Compiler:     runtime.Compiler,
			NumCPU:       runtime.NumCPU(),
			NumGoroutine: runtime.NumGoroutine(),
		},
		Custom: make(map[string]string),
	}
}

// extractRequestMetadata extracts metadata from HTTP request
func (cem *ContextEnrichmentManager) extractRequestMetadata(req *http.Request) *RequestMetadata {
	headers := make(map[string]string)

	// Capture specified headers
	for _, headerName := range cem.config.HeadersToCapture {
		if value := req.Header.Get(headerName); value != "" {
			// Check if header should be redacted
			shouldRedact := false
			for _, redactHeader := range cem.config.HeadersToRedact {
				if strings.EqualFold(headerName, redactHeader) {
					headers[headerName] = "[REDACTED]"
					shouldRedact = true
					break
				}
			}
			if !shouldRedact {
				headers[headerName] = value
			}
		}
	}

	return &RequestMetadata{
		ID:               generateRequestID(),
		Method:           req.Method,
		Path:             req.URL.Path,
		UserAgent:        req.Header.Get("User-Agent"),
		RemoteAddr:       req.RemoteAddr,
		Headers:          headers,
		ContentType:      req.Header.Get("Content-Type"),
		ContentLength:    req.ContentLength,
		AcceptLanguage:   req.Header.Get("Accept-Language"),
		AcceptEncoding:   req.Header.Get("Accept-Encoding"),
		Referer:          req.Header.Get("Referer"),
		Origin:           req.Header.Get("Origin"),
		ForwardedFor:     req.Header.Get("X-Forwarded-For"),
		RealIP:           getRealIP(req),
		TraceParent:      req.Header.Get("traceparent"),
		TraceState:       req.Header.Get("tracestate"),
		CorrelationID:    req.Header.Get("X-Correlation-ID"),
		RequestStartTime: time.Now(),
		Custom:           make(map[string]string),
	}
}

// enrichWithServiceMetadata adds service metadata to context
func (cem *ContextEnrichmentManager) enrichWithServiceMetadata(
	ctx context.Context,
) context.Context {
	ctx = WithServiceMetadataInContext(ctx, cem.serviceMetadata)

	if cem.config.AutoEnrichBaggage {
		ctx = cem.addServiceMetadataToBaggage(ctx)
	}

	return ctx
}

// enrichWithRequestMetadata adds request metadata to context
func (cem *ContextEnrichmentManager) enrichWithRequestMetadata(
	ctx context.Context,
	reqMeta *RequestMetadata,
) context.Context {
	ctx = WithRequestMetadataInContext(ctx, reqMeta)

	if cem.config.AutoEnrichBaggage {
		ctx = cem.addRequestMetadataToBaggage(ctx)
	}

	return ctx
}

// enrichWithBusinessMetadata adds business metadata to context
func (cem *ContextEnrichmentManager) enrichWithBusinessMetadata(
	ctx context.Context,
	bizMeta *BusinessMetadata,
) context.Context {
	ctx = WithBusinessMetadataInContext(ctx, bizMeta)

	if cem.config.AutoEnrichBaggage {
		ctx = cem.addBusinessMetadataToBaggage(ctx)
	}

	return ctx
}

// enrichWithSecurityMetadata adds security metadata to context
func (cem *ContextEnrichmentManager) enrichWithSecurityMetadata(
	ctx context.Context,
	secMeta *SecurityMetadata,
) context.Context {
	ctx = WithSecurityMetadataInContext(ctx, secMeta)

	// Note: Security metadata is not added to baggage for security reasons

	return ctx
}

// enrichWithRuntimeMetadata adds runtime metadata to context
func (cem *ContextEnrichmentManager) enrichWithRuntimeMetadata(
	ctx context.Context,
) context.Context {
	// Update runtime info
	if cem.serviceMetadata != nil {
		cem.serviceMetadata.Runtime.NumGoroutine = runtime.NumGoroutine()
	}

	return ctx
}

// addServiceMetadataToBaggage adds service metadata to baggage
func (cem *ContextEnrichmentManager) addServiceMetadataToBaggage(
	ctx context.Context,
) context.Context {
	if cem.serviceMetadata == nil {
		return ctx
	}

	b := baggage.FromContext(ctx)

	// Add key service metadata to baggage
	pairs := []struct{ key, value string }{
		{"service.name", cem.serviceMetadata.Name},
		{"service.version", cem.serviceMetadata.Version},
		{"service.environment", cem.serviceMetadata.Environment},
		{"service.namespace", cem.serviceMetadata.Namespace},
		{"service.instance", cem.serviceMetadata.Instance},
	}

	for _, pair := range pairs {
		if pair.value != "" && pair.value != "unknown" {
			member, err := baggage.NewMember(pair.key, pair.value)
			if err == nil {
				b, _ = b.SetMember(member)
			}
		}
	}

	return baggage.ContextWithBaggage(ctx, b)
}

// addRequestMetadataToBaggage adds request metadata to baggage
func (cem *ContextEnrichmentManager) addRequestMetadataToBaggage(
	ctx context.Context,
) context.Context {
	reqMeta := GetRequestMetadataFromContext(ctx)
	if reqMeta == nil {
		return ctx
	}

	b := baggage.FromContext(ctx)

	// Add key request metadata to baggage
	pairs := []struct{ key, value string }{
		{"request.id", reqMeta.ID},
		{"request.method", reqMeta.Method},
		{"request.path", reqMeta.Path},
		{"request.user_agent", reqMeta.UserAgent},
		{"request.correlation_id", reqMeta.CorrelationID},
		{"request.user_id", reqMeta.UserID},
		{"request.tenant_id", reqMeta.TenantID},
	}

	for _, pair := range pairs {
		if pair.value != "" {
			member, err := baggage.NewMember(pair.key, pair.value)
			if err == nil {
				b, _ = b.SetMember(member)
			}
		}
	}

	return baggage.ContextWithBaggage(ctx, b)
}

// addBusinessMetadataToBaggage adds business metadata to baggage
func (cem *ContextEnrichmentManager) addBusinessMetadataToBaggage(
	ctx context.Context,
) context.Context {
	bizMeta := GetBusinessMetadataFromContext(ctx)
	if bizMeta == nil {
		return ctx
	}

	b := baggage.FromContext(ctx)

	// Add key business metadata to baggage
	pairs := []struct{ key, value string }{
		{"business.operation", bizMeta.Operation},
		{"business.resource", bizMeta.Resource},
		{"business.action", bizMeta.Action},
		{"business.entity", bizMeta.Entity},
		{"business.entity_id", bizMeta.EntityID},
		{"business.feature", bizMeta.Feature},
	}

	for _, pair := range pairs {
		if pair.value != "" {
			member, err := baggage.NewMember(pair.key, pair.value)
			if err == nil {
				b, _ = b.SetMember(member)
			}
		}
	}

	return baggage.ContextWithBaggage(ctx, b)
}

// Convert metadata to OpenTelemetry attributes

func (cem *ContextEnrichmentManager) serviceMetadataToAttributes() []attribute.KeyValue {
	if cem.serviceMetadata == nil {
		return nil
	}

	attrs := []attribute.KeyValue{
		attribute.String(KeyServiceName, cem.serviceMetadata.Name),
		attribute.String(KeyServiceVersion, cem.serviceMetadata.Version),
		attribute.String("service.environment", cem.serviceMetadata.Environment),
		attribute.String("service.namespace", cem.serviceMetadata.Namespace),
		attribute.String("service.instance", cem.serviceMetadata.Instance),
		attribute.String("service.team", cem.serviceMetadata.Team),
		attribute.String("service.owner", cem.serviceMetadata.Owner),
		attribute.String("service.region", cem.serviceMetadata.Region),
		attribute.String("service.zone", cem.serviceMetadata.Zone),
		attribute.String("service.hostname", cem.serviceMetadata.Hostname),
		attribute.Int("process.pid", cem.serviceMetadata.Process.PID),
		attribute.String("runtime.name", cem.serviceMetadata.Runtime.Language),
		attribute.String("runtime.version", cem.serviceMetadata.Runtime.Version),
		attribute.String("runtime.platform", cem.serviceMetadata.Runtime.Platform),
		attribute.Int("runtime.cpu_count", cem.serviceMetadata.Runtime.NumCPU),
		attribute.Int("runtime.goroutine_count", cem.serviceMetadata.Runtime.NumGoroutine),
	}

	// Add custom attributes
	for key, value := range cem.serviceMetadata.Custom {
		attrs = append(attrs, attribute.String("service.custom."+key, value))
	}

	return attrs
}

func (cem *ContextEnrichmentManager) requestMetadataToAttributes(
	reqMeta *RequestMetadata,
) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(KeyHTTPMethod, reqMeta.Method),
		attribute.String(KeyHTTPPath, reqMeta.Path),
		attribute.String("http.user_agent", reqMeta.UserAgent),
		attribute.String("http.request_id", reqMeta.ID),
		attribute.String("http.remote_addr", reqMeta.RemoteAddr),
		attribute.String("http.content_type", reqMeta.ContentType),
		attribute.Int64("http.content_length", reqMeta.ContentLength),
		attribute.String("http.referer", reqMeta.Referer),
		attribute.String("http.origin", reqMeta.Origin),
		attribute.String("http.real_ip", reqMeta.RealIP),
		attribute.String("http.correlation_id", reqMeta.CorrelationID),
		attribute.String("http.user_id", reqMeta.UserID),
		attribute.String("http.tenant_id", reqMeta.TenantID),
	}

	// Add custom attributes
	for key, value := range reqMeta.Custom {
		attrs = append(attrs, attribute.String("http.custom."+key, value))
	}

	return attrs
}

func (cem *ContextEnrichmentManager) businessMetadataToAttributes(
	bizMeta *BusinessMetadata,
) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("business.operation", bizMeta.Operation),
		attribute.String("business.resource", bizMeta.Resource),
		attribute.String("business.action", bizMeta.Action),
		attribute.String("business.entity", bizMeta.Entity),
		attribute.String("business.entity_id", bizMeta.EntityID),
		attribute.String("business.feature", bizMeta.Feature),
		attribute.String("business.experiment", bizMeta.Experiment),
		attribute.String("business.cohort", bizMeta.Cohort),
		attribute.String("business.campaign", bizMeta.Campaign),
		attribute.String("business.source", bizMeta.Source),
		attribute.String("business.medium", bizMeta.Medium),
		attribute.String("business.channel", bizMeta.Channel),
		attribute.String("business.partner", bizMeta.Partner),
		attribute.String("business.client_type", bizMeta.ClientType),
		attribute.String("business.client_version", bizMeta.ClientVersion),
	}

	// Add custom attributes
	for key, value := range bizMeta.Custom {
		attrs = append(attrs, attribute.String("business.custom."+key, value))
	}

	return attrs
}

func (cem *ContextEnrichmentManager) securityMetadataToAttributes(
	secMeta *SecurityMetadata,
) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("security.auth_method", secMeta.AuthMethod),
		attribute.String("security.auth_provider", secMeta.AuthProvider),
		attribute.String("security.security_level", secMeta.SecurityLevel),
		attribute.String("security.risk_level", secMeta.RiskLevel),
		attribute.Float64("security.threat_score", secMeta.ThreatScore),
		attribute.Bool("security.audit_required", secMeta.AuditRequired),
		attribute.Bool("security.pii", secMeta.PII),
		attribute.Bool("security.sensitive", secMeta.Sensitive),
	}

	// Add roles and permissions (limited for security)
	if len(secMeta.Roles) > 0 {
		attrs = append(attrs, attribute.StringSlice("security.roles", secMeta.Roles))
	}

	// Don't add sensitive custom attributes to spans

	return attrs
}

// Helper functions

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getExecutablePath() string {
	if path, err := os.Executable(); err == nil {
		return path
	}
	return ""
}

func generateRequestID() string {
	// In production, use a proper UUID library
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

func getRealIP(req *http.Request) string {
	// Check X-Real-IP header first
	if realIP := req.Header.Get("X-Real-IP"); realIP != "" {
		return realIP
	}

	// Check X-Forwarded-For header
	if forwardedFor := req.Header.Get("X-Forwarded-For"); forwardedFor != "" {
		// Take the first IP if multiple are present
		if idx := strings.Index(forwardedFor, ","); idx > 0 {
			return strings.TrimSpace(forwardedFor[:idx])
		}
		return strings.TrimSpace(forwardedFor)
	}

	// Fall back to RemoteAddr
	if remoteAddr := req.RemoteAddr; remoteAddr != "" {
		// Remove port if present
		if idx := strings.LastIndex(remoteAddr, ":"); idx > 0 {
			return remoteAddr[:idx]
		}
		return remoteAddr
	}

	return ""
}
