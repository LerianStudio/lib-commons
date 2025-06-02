// Package main demonstrates observability context enrichment with automatic
// metadata injection, request correlation, and comprehensive tracing patterns.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/LerianStudio/lib-commons/commons/observability"
	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel/attribute"
)

func main() {
	fmt.Println("Observability Context Enrichment Demo")
	fmt.Println("====================================")

	// Set up environment for demo
	setupDemoEnvironment()

	// Example 1: Basic context enrichment
	fmt.Println("\n1. Basic Context Enrichment")
	basicEnrichmentExample()

	// Example 2: HTTP request enrichment
	fmt.Println("\n2. HTTP Request Enrichment")
	httpRequestEnrichmentExample()

	// Example 3: Business context enrichment
	fmt.Println("\n3. Business Context Enrichment")
	businessContextExample()

	// Example 4: Security context enrichment
	fmt.Println("\n4. Security Context Enrichment")
	securityContextExample()

	// Example 5: Custom enrichers
	fmt.Println("\n5. Custom Context Enrichers")
	customEnrichersExample()

	// Example 6: Full microservice with enrichment
	fmt.Println("\n6. Full Microservice with Enrichment")
	microserviceEnrichmentExample()

	// Example 7: Cross-service correlation
	fmt.Println("\n7. Cross-Service Correlation")
	crossServiceCorrelationExample()
}

// setupDemoEnvironment sets up environment variables for demonstration
func setupDemoEnvironment() {
	_ = os.Setenv("SERVICE_NAME", "observability-demo")
	_ = os.Setenv("SERVICE_VERSION", "v1.2.3")
	_ = os.Setenv("ENVIRONMENT", "production")
	_ = os.Setenv("NAMESPACE", "financial-services")
	_ = os.Setenv("TEAM", "platform-team")
	_ = os.Setenv("OWNER", "jane.doe@company.com")
	_ = os.Setenv("AWS_REGION", "us-east-1")
	_ = os.Setenv("AWS_AVAILABILITY_ZONE", "us-east-1a")
}

// basicEnrichmentExample demonstrates basic context enrichment
func basicEnrichmentExample() {
	fmt.Println("Setting up basic context enrichment...")

	// Create enrichment configuration
	config := observability.EnrichmentConfig{
		EnableServiceMetadata:  true,
		EnableRequestMetadata:  true,
		EnableBusinessMetadata: true,
		EnableSecurityMetadata: true,
		EnableRuntimeMetadata:  true,
		AutoEnrichSpans:        true,
		AutoEnrichLogs:         true,
		AutoEnrichBaggage:      true,
		MaxCustomAttributes:    50,
		AttributePrefix:        "app",
		HeadersToCapture:       []string{"User-Agent", "Authorization", "X-Forwarded-For"},
		HeadersToRedact:        []string{"Authorization", "X-API-Key"},
		SensitiveFields:        []string{"password", "secret", "token"},
	}

	// Create enrichment manager
	enrichmentManager := observability.NewContextEnrichmentManager(config)

	// Create base context
	ctx := context.Background()

	// Enrich context with service metadata
	enrichedCtx := enrichmentManager.EnrichContext(ctx)

	// Verify enrichment
	serviceMetadata := observability.GetServiceMetadataFromContext(enrichedCtx)
	if serviceMetadata != nil {
		fmt.Printf("✅ Service metadata enriched:\n")
		fmt.Printf("   Name: %s\n", serviceMetadata.Name)
		fmt.Printf("   Version: %s\n", serviceMetadata.Version)
		fmt.Printf("   Environment: %s\n", serviceMetadata.Environment)
		fmt.Printf("   Team: %s\n", serviceMetadata.Team)
		fmt.Printf("   Instance: %s\n", serviceMetadata.Instance)
		fmt.Printf(
			"   Runtime: %s %s\n",
			serviceMetadata.Runtime.Language,
			serviceMetadata.Runtime.Version,
		)
		fmt.Printf("   Goroutines: %d\n", serviceMetadata.Runtime.NumGoroutine)
	}

	// Create a span to see enrichment in action
	provider, _ := observability.NewProvider(observability.ProviderConfig{
		ServiceName:    "enrichment-demo",
		ServiceVersion: "1.0.0",
		Environment:    "test",
	})
	provider.Start(enrichedCtx)
	defer provider.Stop(enrichedCtx)

	tracer := provider.GetTracerProvider().Tracer("enrichment-demo")
	spanCtx, span := tracer.Start(enrichedCtx, "basic-enrichment-operation")

	// Enrich the span with context metadata
	enrichmentManager.EnrichSpan(spanCtx, span)

	// Simulate some work
	time.Sleep(10 * time.Millisecond)

	span.End()

	fmt.Println("✅ Basic enrichment example completed")
}

// httpRequestEnrichmentExample demonstrates HTTP request enrichment
func httpRequestEnrichmentExample() {
	fmt.Println("Demonstrating HTTP request enrichment...")

	// Create enrichment manager
	config := observability.EnrichmentConfig{
		EnableServiceMetadata: true,
		EnableRequestMetadata: true,
		AutoEnrichSpans:       true,
		AutoEnrichBaggage:     true,
		HeadersToCapture: []string{
			"User-Agent",
			"X-Correlation-ID",
			"X-User-ID",
			"X-Tenant-ID",
		},
		HeadersToRedact: []string{"Authorization", "Cookie"},
	}

	enrichmentManager := observability.NewContextEnrichmentManager(config)

	// Create a mock HTTP request
	req := httptest.NewRequest("POST", "/api/v1/users", nil)
	req.Header.Set("User-Agent", "MyApp/1.0.0 (iOS 15.0)")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Correlation-ID", "abc123def456")
	req.Header.Set("X-User-ID", "user-789")
	req.Header.Set("X-Tenant-ID", "tenant-456")
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("X-Forwarded-For", "203.0.113.1, 198.51.100.2")
	req.RemoteAddr = "198.51.100.2:54321"

	// Enrich context from HTTP request
	ctx := context.Background()
	enrichedCtx := enrichmentManager.EnrichFromHTTPRequest(ctx, req)

	// Verify request metadata enrichment
	requestMetadata := observability.GetRequestMetadataFromContext(enrichedCtx)
	if requestMetadata != nil {
		fmt.Printf("✅ Request metadata enriched:\n")
		fmt.Printf("   ID: %s\n", requestMetadata.ID)
		fmt.Printf("   Method: %s\n", requestMetadata.Method)
		fmt.Printf("   Path: %s\n", requestMetadata.Path)
		fmt.Printf("   User Agent: %s\n", requestMetadata.UserAgent)
		fmt.Printf("   Remote Addr: %s\n", requestMetadata.RemoteAddr)
		fmt.Printf("   Real IP: %s\n", requestMetadata.RealIP)
		fmt.Printf("   Correlation ID: %s\n", requestMetadata.CorrelationID)
		fmt.Printf("   User ID: %s\n", requestMetadata.UserID)
		fmt.Printf("   Tenant ID: %s\n", requestMetadata.TenantID)
		fmt.Printf("   Headers captured: %d\n", len(requestMetadata.Headers))

		// Show headers (redacted ones should be masked)
		for key, value := range requestMetadata.Headers {
			fmt.Printf("     %s: %s\n", key, value)
		}
	}

	// Check baggage enrichment
	baggage := observability.GetBaggageItem(enrichedCtx, "request.correlation_id")
	if baggage != "" {
		fmt.Printf("✅ Baggage enriched with correlation ID: %s\n", baggage)
	}

	fmt.Println("✅ HTTP request enrichment example completed")
}

// businessContextExample demonstrates business context enrichment
func businessContextExample() {
	fmt.Println("Demonstrating business context enrichment...")

	// Create business metadata
	businessMetadata := observability.NewBusinessMetadataBuilder().
		WithOperation("user-registration").
		WithResource("user").
		WithAction("create").
		WithEntity("user").
		WithEntityID("user-12345").
		WithFeature("registration-v2").
		WithExperiment("onboarding-flow-a").
		WithCampaign("summer-promotion").
		WithSource("mobile-app").
		WithClientType("ios").
		WithClientVersion("2.1.0").
		WithCustomField("signup_method", "social_login").
		WithCustomField("referral_code", "FRIEND20").
		Build()

	// Create enrichment manager
	config := observability.EnrichmentConfig{
		EnableServiceMetadata:  true,
		EnableBusinessMetadata: true,
		AutoEnrichSpans:        true,
		AutoEnrichBaggage:      true,
	}

	enrichmentManager := observability.NewContextEnrichmentManager(config)

	// Enrich context with business metadata
	ctx := context.Background()
	enrichedCtx := enrichmentManager.EnrichContext(ctx,
		observability.WithBusinessMetadata(businessMetadata),
	)

	// Verify business metadata enrichment
	retrievedMetadata := observability.GetBusinessMetadataFromContext(enrichedCtx)
	if retrievedMetadata != nil {
		fmt.Printf("✅ Business metadata enriched:\n")
		fmt.Printf("   Operation: %s\n", retrievedMetadata.Operation)
		fmt.Printf("   Resource: %s\n", retrievedMetadata.Resource)
		fmt.Printf("   Action: %s\n", retrievedMetadata.Action)
		fmt.Printf("   Entity: %s (ID: %s)\n", retrievedMetadata.Entity, retrievedMetadata.EntityID)
		fmt.Printf("   Feature: %s\n", retrievedMetadata.Feature)
		fmt.Printf("   Experiment: %s\n", retrievedMetadata.Experiment)
		fmt.Printf("   Campaign: %s\n", retrievedMetadata.Campaign)
		fmt.Printf("   Source: %s\n", retrievedMetadata.Source)
		fmt.Printf(
			"   Client: %s v%s\n",
			retrievedMetadata.ClientType,
			retrievedMetadata.ClientVersion,
		)
		fmt.Printf("   Custom fields: %d\n", len(retrievedMetadata.Custom))

		for key, value := range retrievedMetadata.Custom {
			fmt.Printf("     %s: %s\n", key, value)
		}
	}

	// Simulate business operation with enriched context
	fmt.Println("\n🔄 Simulating business operation...")
	simulateBusinessOperation(enrichedCtx, "user-registration")

	fmt.Println("✅ Business context enrichment example completed")
}

// securityContextExample demonstrates security context enrichment
func securityContextExample() {
	fmt.Println("Demonstrating security context enrichment...")

	// Create security metadata
	securityMetadata := observability.NewSecurityMetadataBuilder().
		WithAuthMethod("oauth2").
		WithAuthProvider("auth0").
		WithSecurityLevel("high").
		WithRiskLevel("medium").
		WithThreatScore(0.3).
		WithRole("admin").
		WithRole("user").
		WithPermission("users:read").
		WithPermission("users:write").
		WithPermission("admin:dashboard").
		WithComplianceFlag("gdpr").
		WithComplianceFlag("pci-dss").
		WithAuditRequired(true).
		WithPII(true).
		WithSensitive(false).
		WithCustomField("mfa_verified", "true").
		WithCustomField("last_password_change", "2024-01-15").
		Build()

	// Create enrichment manager
	config := observability.EnrichmentConfig{
		EnableServiceMetadata:  true,
		EnableSecurityMetadata: true,
		AutoEnrichSpans:        true,
		// Note: Security metadata is NOT added to baggage for security reasons
		AutoEnrichBaggage: false,
	}

	enrichmentManager := observability.NewContextEnrichmentManager(config)

	// Enrich context with security metadata
	ctx := context.Background()
	enrichedCtx := enrichmentManager.EnrichContext(ctx,
		observability.WithSecurityMetadata(securityMetadata),
	)

	// Verify security metadata enrichment
	retrievedMetadata := observability.GetSecurityMetadataFromContext(enrichedCtx)
	if retrievedMetadata != nil {
		fmt.Printf("✅ Security metadata enriched:\n")
		fmt.Printf("   Auth Method: %s\n", retrievedMetadata.AuthMethod)
		fmt.Printf("   Auth Provider: %s\n", retrievedMetadata.AuthProvider)
		fmt.Printf("   Security Level: %s\n", retrievedMetadata.SecurityLevel)
		fmt.Printf(
			"   Risk Level: %s (Score: %.2f)\n",
			retrievedMetadata.RiskLevel,
			retrievedMetadata.ThreatScore,
		)
		fmt.Printf("   Roles: %v\n", retrievedMetadata.Roles)
		fmt.Printf("   Permissions: %v\n", retrievedMetadata.Permissions)
		fmt.Printf("   Compliance: %v\n", retrievedMetadata.ComplianceFlags)
		fmt.Printf("   Audit Required: %t\n", retrievedMetadata.AuditRequired)
		fmt.Printf("   Contains PII: %t\n", retrievedMetadata.PII)
		fmt.Printf("   Sensitive: %t\n", retrievedMetadata.Sensitive)
		fmt.Printf("   Custom fields: %d\n", len(retrievedMetadata.Custom))

		for key, value := range retrievedMetadata.Custom {
			fmt.Printf("     %s: %s\n", key, value)
		}
	}

	// Simulate security-sensitive operation
	fmt.Println("\n🔒 Simulating security-sensitive operation...")
	simulateSecurityOperation(enrichedCtx, "admin-user-access")

	fmt.Println("✅ Security context enrichment example completed")
}

// customEnrichersExample demonstrates custom context enrichers
func customEnrichersExample() {
	fmt.Println("Demonstrating custom context enrichers...")

	// Create custom enrichers
	analyticsEnricher := &AnalyticsEnricher{
		SessionID:   "sess-abc123",
		PageURL:     "/dashboard",
		UserSegment: "premium",
		ABTestGroup: "variant-b",
	}

	geolocationEnricher := &GeolocationEnricher{
		Country:   "United States",
		Region:    "California",
		City:      "San Francisco",
		Timezone:  "America/Los_Angeles",
		ISP:       "Example ISP",
		Latitude:  37.7749,
		Longitude: -122.4194,
	}

	performanceEnricher := &PerformanceEnricher{
		RequestStartTime: time.Now(),
		UpstreamLatency:  50 * time.Millisecond,
		CacheHitRate:     0.85,
		DatabaseQueries:  3,
	}

	// Create enrichment configuration with custom enrichers
	config := observability.EnrichmentConfig{
		EnableServiceMetadata: true,
		AutoEnrichSpans:       true,
		CustomEnrichers: []observability.ContextEnricher{
			analyticsEnricher,
			geolocationEnricher,
			performanceEnricher,
		},
	}

	enrichmentManager := observability.NewContextEnrichmentManager(config)

	// Enrich context
	ctx := context.Background()
	customMetadata := map[string]interface{}{
		"feature_flags": []string{"new-ui", "beta-api"},
		"client_build":  "2024.1.15",
		"device_type":   "mobile",
	}

	enrichedCtx := enrichmentManager.EnrichContext(ctx,
		observability.WithCustomMetadata(customMetadata),
	)

	fmt.Printf("✅ Custom enrichers applied:\n")
	fmt.Printf("   Analytics enricher: Priority %d\n", analyticsEnricher.Priority())
	fmt.Printf("   Geolocation enricher: Priority %d\n", geolocationEnricher.Priority())
	fmt.Printf("   Performance enricher: Priority %d\n", performanceEnricher.Priority())

	// Check baggage for custom enriched data
	if sessionID := observability.GetBaggageItem(enrichedCtx, "analytics.session_id"); sessionID != "" {
		fmt.Printf("   Analytics session ID in baggage: %s\n", sessionID)
	}

	if country := observability.GetBaggageItem(enrichedCtx, "geo.country"); country != "" {
		fmt.Printf("   Geolocation country in baggage: %s\n", country)
	}

	// Simulate operation with custom enriched context
	fmt.Println("\n⚡ Simulating operation with custom enrichment...")
	simulateCustomEnrichedOperation(enrichedCtx)

	fmt.Println("✅ Custom enrichers example completed")
}

// microserviceEnrichmentExample demonstrates full microservice integration
func microserviceEnrichmentExample() {
	fmt.Println("Demonstrating full microservice enrichment integration...")

	// Create Fiber app
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})

	// Create enrichment manager
	config := observability.EnrichmentConfig{
		EnableServiceMetadata:  true,
		EnableRequestMetadata:  true,
		EnableBusinessMetadata: true,
		AutoEnrichSpans:        true,
		AutoEnrichBaggage:      true,
		HeadersToCapture:       []string{"User-Agent", "X-Correlation-ID", "X-User-ID"},
		HeadersToRedact:        []string{"Authorization"},
	}

	enrichmentManager := observability.NewContextEnrichmentManager(config)

	// Add enrichment middleware
	app.Use(func(c *fiber.Ctx) error {
		// Enrich context from HTTP request
		enrichedCtx := enrichmentManager.EnrichFromHTTPRequest(c.Context(), c.Request())

		// Add business context for this endpoint
		if c.Path() == "/api/users" && c.Method() == "POST" {
			businessMetadata := observability.NewBusinessMetadataBuilder().
				WithOperation("create-user").
				WithResource("user").
				WithAction("create").
				WithFeature("user-management").
				Build()

			enrichedCtx = enrichmentManager.EnrichContext(enrichedCtx,
				observability.WithBusinessMetadata(businessMetadata),
			)
		}

		// Set enriched context
		c.SetUserContext(enrichedCtx)

		return c.Next()
	})

	// API endpoints
	app.Post("/api/users", func(c *fiber.Ctx) error {
		ctx := c.UserContext()

		// Log enriched context information
		logEnrichedContext(ctx, "Creating user")

		// Simulate user creation
		time.Sleep(50 * time.Millisecond)

		return c.JSON(fiber.Map{
			"id":      "user-12345",
			"status":  "created",
			"message": "User created successfully",
		})
	})

	app.Get("/api/users/:id", func(c *fiber.Ctx) error {
		ctx := c.UserContext()
		userID := c.Params("id")

		// Add business context for this specific operation
		businessMetadata := observability.NewBusinessMetadataBuilder().
			WithOperation("get-user").
			WithResource("user").
			WithAction("read").
			WithEntityID(userID).
			Build()

		enrichedCtx := enrichmentManager.EnrichContext(ctx,
			observability.WithBusinessMetadata(businessMetadata),
		)

		// Log enriched context information
		logEnrichedContext(enrichedCtx, fmt.Sprintf("Getting user %s", userID))

		// Simulate user retrieval
		time.Sleep(25 * time.Millisecond)

		return c.JSON(fiber.Map{
			"id":     userID,
			"name":   "John Doe",
			"email":  "john@example.com",
			"status": "active",
		})
	})

	// Start server in background
	go func() {
		fmt.Println("🚀 Microservice started on :8081")
		if err := app.Listen(":8081"); err != nil {
			log.Printf("Server error: %v", err)
		}
	}()

	// Simulate API requests
	time.Sleep(500 * time.Millisecond)
	fmt.Println("\n🧪 Testing enriched endpoints...")

	client := &http.Client{Timeout: 5 * time.Second}

	// Test user creation
	req, _ := http.NewRequest("POST", "http://localhost:8081/api/users", nil)
	req.Header.Set("User-Agent", "EnrichmentDemo/1.0")
	req.Header.Set("X-Correlation-ID", "test-corr-123")
	req.Header.Set("X-User-ID", "requester-456")
	req.Header.Set("Authorization", "Bearer secret-token")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ POST request failed: %v\n", err)
	} else {
		fmt.Printf("✅ POST /api/users: Status %d\n", resp.StatusCode)
		_ = resp.Body.Close()
	}

	// Test user retrieval
	req, _ = http.NewRequest("GET", "http://localhost:8081/api/users/user-789", nil)
	req.Header.Set("User-Agent", "EnrichmentDemo/1.0")
	req.Header.Set("X-Correlation-ID", "test-corr-456")

	resp, err = client.Do(req)
	if err != nil {
		fmt.Printf("❌ GET request failed: %v\n", err)
	} else {
		fmt.Printf("✅ GET /api/users/user-789: Status %d\n", resp.StatusCode)
		_ = resp.Body.Close()
	}

	// Cleanup
	_ = app.Shutdown()
	fmt.Println("✅ Microservice enrichment example completed")
}

// crossServiceCorrelationExample demonstrates cross-service correlation
func crossServiceCorrelationExample() {
	fmt.Println("Demonstrating cross-service correlation...")

	// Create enrichment manager
	config := observability.EnrichmentConfig{
		EnableServiceMetadata: true,
		EnableRequestMetadata: true,
		AutoEnrichBaggage:     true,
	}

	enrichmentManager := observability.NewContextEnrichmentManager(config)

	// Simulate service A receiving initial request
	fmt.Println("\n📡 Service A: Receiving initial request")
	serviceACtx := simulateServiceA(enrichmentManager)

	// Simulate service A calling service B
	fmt.Println("\n📡 Service B: Processing forwarded request")
	serviceBCtx := simulateServiceB(serviceACtx, enrichmentManager)

	// Simulate service B calling service C
	fmt.Println("\n📡 Service C: Processing final request")
	simulateServiceC(serviceBCtx, enrichmentManager)

	// Show correlation across services
	fmt.Println("\n🔗 Cross-service correlation:")

	// Extract correlation data from each service context
	if reqMetaA := observability.GetRequestMetadataFromContext(serviceACtx); reqMetaA != nil {
		fmt.Printf("   Service A - Request ID: %s, Correlation: %s\n",
			reqMetaA.ID, reqMetaA.CorrelationID)
	}

	if reqMetaB := observability.GetRequestMetadataFromContext(serviceBCtx); reqMetaB != nil {
		fmt.Printf("   Service B - Request ID: %s, Correlation: %s\n",
			reqMetaB.ID, reqMetaB.CorrelationID)
	}

	fmt.Println("✅ Cross-service correlation example completed")
}

// Custom enricher implementations

// AnalyticsEnricher enriches context with analytics data
type AnalyticsEnricher struct {
	SessionID   string
	PageURL     string
	UserSegment string
	ABTestGroup string
}

func (ae *AnalyticsEnricher) Name() string  { return "analytics" }
func (ae *AnalyticsEnricher) Priority() int { return 100 }

func (ae *AnalyticsEnricher) EnrichContext(
	ctx context.Context,
	metadata map[string]interface{},
) context.Context {
	// Add analytics data to baggage
	if sessionID := ae.SessionID; sessionID != "" {
		ctx, _ = observability.WithBaggageItem(ctx, "analytics.session_id", sessionID)
	}
	if userSegment := ae.UserSegment; userSegment != "" {
		ctx, _ = observability.WithBaggageItem(ctx, "analytics.user_segment", userSegment)
	}
	if abTestGroup := ae.ABTestGroup; abTestGroup != "" {
		ctx, _ = observability.WithBaggageItem(ctx, "analytics.ab_test_group", abTestGroup)
	}

	// Add analytics data as span attributes
	observability.AddSpanAttributes(ctx,
		attribute.String("analytics.session_id", ae.SessionID),
		attribute.String("analytics.page_url", ae.PageURL),
		attribute.String("analytics.user_segment", ae.UserSegment),
		attribute.String("analytics.ab_test_group", ae.ABTestGroup),
	)

	return ctx
}

// GeolocationEnricher enriches context with geolocation data
type GeolocationEnricher struct {
	Country   string
	Region    string
	City      string
	Timezone  string
	ISP       string
	Latitude  float64
	Longitude float64
}

func (ge *GeolocationEnricher) Name() string  { return "geolocation" }
func (ge *GeolocationEnricher) Priority() int { return 80 }

func (ge *GeolocationEnricher) EnrichContext(
	ctx context.Context,
	metadata map[string]interface{},
) context.Context {
	// Add geolocation data to baggage
	if country := ge.Country; country != "" {
		ctx, _ = observability.WithBaggageItem(ctx, "geo.country", country)
	}
	if region := ge.Region; region != "" {
		ctx, _ = observability.WithBaggageItem(ctx, "geo.region", region)
	}
	if city := ge.City; city != "" {
		ctx, _ = observability.WithBaggageItem(ctx, "geo.city", city)
	}

	// Add geolocation data as span attributes
	observability.AddSpanAttributes(ctx,
		attribute.String("geo.country", ge.Country),
		attribute.String("geo.region", ge.Region),
		attribute.String("geo.city", ge.City),
		attribute.String("geo.timezone", ge.Timezone),
		attribute.String("geo.isp", ge.ISP),
		attribute.Float64("geo.latitude", ge.Latitude),
		attribute.Float64("geo.longitude", ge.Longitude),
	)

	return ctx
}

// PerformanceEnricher enriches context with performance data
type PerformanceEnricher struct {
	RequestStartTime time.Time
	UpstreamLatency  time.Duration
	CacheHitRate     float64
	DatabaseQueries  int
}

func (pe *PerformanceEnricher) Name() string  { return "performance" }
func (pe *PerformanceEnricher) Priority() int { return 60 }

func (pe *PerformanceEnricher) EnrichContext(
	ctx context.Context,
	metadata map[string]interface{},
) context.Context {
	// Add performance data as span attributes
	observability.AddSpanAttributes(ctx,
		attribute.Int64("perf.request_start_time", pe.RequestStartTime.Unix()),
		attribute.Int64("perf.upstream_latency_ms", pe.UpstreamLatency.Milliseconds()),
		attribute.Float64("perf.cache_hit_rate", pe.CacheHitRate),
		attribute.Int("perf.database_queries", pe.DatabaseQueries),
	)

	return ctx
}

// Helper functions

func simulateBusinessOperation(ctx context.Context, operation string) {
	// Extract business metadata
	bizMeta := observability.GetBusinessMetadataFromContext(ctx)
	if bizMeta != nil {
		fmt.Printf("   🎯 Executing %s for entity %s\n", operation, bizMeta.EntityID)
		fmt.Printf("   📊 Feature: %s, Experiment: %s\n", bizMeta.Feature, bizMeta.Experiment)
	}

	// Simulate work with tracing
	observability.AddSpanEvent(ctx, "business.operation.start",
		attribute.String("operation", operation),
	)

	time.Sleep(20 * time.Millisecond)

	observability.AddSpanEvent(ctx, "business.operation.complete",
		attribute.String("operation", operation),
		attribute.String("result", "success"),
	)
}

func simulateSecurityOperation(ctx context.Context, operation string) {
	// Extract security metadata
	secMeta := observability.GetSecurityMetadataFromContext(ctx)
	if secMeta != nil {
		fmt.Printf("   🔐 Executing %s with %s auth\n", operation, secMeta.AuthMethod)
		fmt.Printf(
			"   🛡️  Security level: %s, Risk: %s\n",
			secMeta.SecurityLevel,
			secMeta.RiskLevel,
		)
		fmt.Printf("   👤 Roles: %v\n", secMeta.Roles)

		if secMeta.AuditRequired {
			fmt.Printf("   📝 Audit required for this operation\n")
		}
	}

	// Add security-related span events
	observability.AddSpanEvent(ctx, "security.authorization.start")
	time.Sleep(5 * time.Millisecond)
	observability.AddSpanEvent(ctx, "security.authorization.success")

	observability.AddSpanEvent(ctx, "security.operation.start",
		attribute.String("operation", operation),
	)
	time.Sleep(15 * time.Millisecond)
	observability.AddSpanEvent(ctx, "security.operation.complete")
}

func simulateCustomEnrichedOperation(ctx context.Context) {
	// Check for custom enriched data
	if sessionID := observability.GetBaggageItem(ctx, "analytics.session_id"); sessionID != "" {
		fmt.Printf("   📈 Analytics session: %s\n", sessionID)
	}

	if country := observability.GetBaggageItem(ctx, "geo.country"); country != "" {
		fmt.Printf("   🌍 Location: %s\n", country)
	}

	// Add operation event
	observability.AddSpanEvent(ctx, "custom.operation.execute",
		attribute.String("operation_type", "enriched"),
	)

	time.Sleep(30 * time.Millisecond)
}

func logEnrichedContext(ctx context.Context, operation string) {
	fmt.Printf("   🔍 %s with enriched context:\n", operation)

	// Log service metadata
	if svcMeta := observability.GetServiceMetadataFromContext(ctx); svcMeta != nil {
		fmt.Printf(
			"     Service: %s v%s (%s)\n",
			svcMeta.Name,
			svcMeta.Version,
			svcMeta.Environment,
		)
	}

	// Log request metadata
	if reqMeta := observability.GetRequestMetadataFromContext(ctx); reqMeta != nil {
		fmt.Printf("     Request: %s %s (ID: %s)\n", reqMeta.Method, reqMeta.Path, reqMeta.ID)
		if reqMeta.CorrelationID != "" {
			fmt.Printf("     Correlation: %s\n", reqMeta.CorrelationID)
		}
		if reqMeta.UserID != "" {
			fmt.Printf("     User: %s\n", reqMeta.UserID)
		}
	}

	// Log business metadata
	if bizMeta := observability.GetBusinessMetadataFromContext(ctx); bizMeta != nil {
		fmt.Printf("     Business: %s %s on %s\n", bizMeta.Action, bizMeta.Resource, bizMeta.Entity)
	}
}

func simulateServiceA(enrichmentManager *observability.ContextEnrichmentManager) context.Context {
	// Create initial request context
	req := httptest.NewRequest("GET", "/api/orders/12345", nil)
	req.Header.Set("X-Correlation-ID", "corr-abc123")
	req.Header.Set("X-User-ID", "user-456")

	ctx := context.Background()
	enrichedCtx := enrichmentManager.EnrichFromHTTPRequest(ctx, req)

	// Add service A business context
	businessMetadata := observability.NewBusinessMetadataBuilder().
		WithOperation("process-order").
		WithResource("order").
		WithEntityID("order-12345").
		Build()

	enrichedCtx = enrichmentManager.EnrichContext(enrichedCtx,
		observability.WithBusinessMetadata(businessMetadata),
	)

	if reqMeta := observability.GetRequestMetadataFromContext(enrichedCtx); reqMeta != nil {
		fmt.Printf("   Request ID: %s, Correlation: %s\n", reqMeta.ID, reqMeta.CorrelationID)
	}

	return enrichedCtx
}

func simulateServiceB(
	parentCtx context.Context,
	enrichmentManager *observability.ContextEnrichmentManager,
) context.Context {
	// Service B creates its own request context but inherits correlation
	parentReqMeta := observability.GetRequestMetadataFromContext(parentCtx)

	requestMetadata := observability.NewRequestMetadataBuilder().
		WithMethod("GET").
		WithPath("/internal/inventory/check").
		WithCorrelationID(parentReqMeta.CorrelationID). // Inherit correlation ID
		WithUserID(parentReqMeta.UserID).               // Inherit user ID
		Build()

	enrichedCtx := enrichmentManager.EnrichContext(parentCtx,
		observability.WithRequestMetadata(requestMetadata),
	)

	// Add service B business context
	businessMetadata := observability.NewBusinessMetadataBuilder().
		WithOperation("check-inventory").
		WithResource("inventory").
		WithEntityID("item-67890").
		Build()

	enrichedCtx = enrichmentManager.EnrichContext(enrichedCtx,
		observability.WithBusinessMetadata(businessMetadata),
	)

	if reqMeta := observability.GetRequestMetadataFromContext(enrichedCtx); reqMeta != nil {
		fmt.Printf("   Request ID: %s, Correlation: %s\n", reqMeta.ID, reqMeta.CorrelationID)
	}

	return enrichedCtx
}

func simulateServiceC(
	parentCtx context.Context,
	enrichmentManager *observability.ContextEnrichmentManager,
) {
	// Service C inherits context and adds its own business logic
	parentReqMeta := observability.GetRequestMetadataFromContext(parentCtx)

	requestMetadata := observability.NewRequestMetadataBuilder().
		WithMethod("POST").
		WithPath("/internal/notifications/send").
		WithCorrelationID(parentReqMeta.CorrelationID). // Inherit correlation ID
		WithUserID(parentReqMeta.UserID).               // Inherit user ID
		Build()

	enrichedCtx := enrichmentManager.EnrichContext(parentCtx,
		observability.WithRequestMetadata(requestMetadata),
	)

	if reqMeta := observability.GetRequestMetadataFromContext(enrichedCtx); reqMeta != nil {
		fmt.Printf("   Request ID: %s, Correlation: %s\n", reqMeta.ID, reqMeta.CorrelationID)
	}
}
