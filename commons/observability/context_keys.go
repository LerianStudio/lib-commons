package observability

import (
	"context"
)

// Context keys for storing metadata in context
const (
	ServiceMetadataKey  ContextKey = "observability-service-metadata"
	RequestMetadataKey  ContextKey = "observability-request-metadata"
	BusinessMetadataKey ContextKey = "observability-business-metadata"
	SecurityMetadataKey ContextKey = "observability-security-metadata"
)

// WithServiceMetadataInContext adds service metadata to context
func WithServiceMetadataInContext(ctx context.Context, metadata *ServiceMetadata) context.Context {
	return context.WithValue(ctx, ServiceMetadataKey, metadata)
}

// GetServiceMetadataFromContext retrieves service metadata from context
func GetServiceMetadataFromContext(ctx context.Context) *ServiceMetadata {
	if metadata, ok := ctx.Value(ServiceMetadataKey).(*ServiceMetadata); ok {
		return metadata
	}
	return nil
}

// WithRequestMetadataInContext adds request metadata to context
func WithRequestMetadataInContext(ctx context.Context, metadata *RequestMetadata) context.Context {
	return context.WithValue(ctx, RequestMetadataKey, metadata)
}

// GetRequestMetadataFromContext retrieves request metadata from context
func GetRequestMetadataFromContext(ctx context.Context) *RequestMetadata {
	if metadata, ok := ctx.Value(RequestMetadataKey).(*RequestMetadata); ok {
		return metadata
	}
	return nil
}

// WithBusinessMetadataInContext adds business metadata to context
func WithBusinessMetadataInContext(
	ctx context.Context,
	metadata *BusinessMetadata,
) context.Context {
	return context.WithValue(ctx, BusinessMetadataKey, metadata)
}

// GetBusinessMetadataFromContext retrieves business metadata from context
func GetBusinessMetadataFromContext(ctx context.Context) *BusinessMetadata {
	if metadata, ok := ctx.Value(BusinessMetadataKey).(*BusinessMetadata); ok {
		return metadata
	}
	return nil
}

// WithSecurityMetadataInContext adds security metadata to context
func WithSecurityMetadataInContext(
	ctx context.Context,
	metadata *SecurityMetadata,
) context.Context {
	return context.WithValue(ctx, SecurityMetadataKey, metadata)
}

// GetSecurityMetadataFromContext retrieves security metadata from context
func GetSecurityMetadataFromContext(ctx context.Context) *SecurityMetadata {
	if metadata, ok := ctx.Value(SecurityMetadataKey).(*SecurityMetadata); ok {
		return metadata
	}
	return nil
}

// EnrichmentOption defines options for context enrichment
type EnrichmentOption func(*enrichmentOptions)

type enrichmentOptions struct {
	request  *RequestMetadata
	business *BusinessMetadata
	security *SecurityMetadata
	custom   map[string]interface{}
}

// WithRequestMetadata adds request metadata to enrichment options
func WithRequestMetadata(metadata *RequestMetadata) EnrichmentOption {
	return func(opts *enrichmentOptions) {
		opts.request = metadata
	}
}

// WithBusinessMetadata adds business metadata to enrichment options
func WithBusinessMetadata(metadata *BusinessMetadata) EnrichmentOption {
	return func(opts *enrichmentOptions) {
		opts.business = metadata
	}
}

// WithSecurityMetadata adds security metadata to enrichment options
func WithSecurityMetadata(metadata *SecurityMetadata) EnrichmentOption {
	return func(opts *enrichmentOptions) {
		opts.security = metadata
	}
}

// WithCustomMetadata adds custom metadata to enrichment options
func WithCustomMetadata(metadata map[string]interface{}) EnrichmentOption {
	return func(opts *enrichmentOptions) {
		opts.custom = metadata
	}
}

// Convenience functions for creating metadata

// NewRequestMetadata creates a new RequestMetadata instance
func NewRequestMetadata() *RequestMetadata {
	return &RequestMetadata{
		Headers: make(map[string]string),
		Custom:  make(map[string]string),
	}
}

// NewBusinessMetadata creates a new BusinessMetadata instance
func NewBusinessMetadata() *BusinessMetadata {
	return &BusinessMetadata{
		Custom: make(map[string]string),
	}
}

// NewSecurityMetadata creates a new SecurityMetadata instance
func NewSecurityMetadata() *SecurityMetadata {
	return &SecurityMetadata{
		Permissions:     make([]string, 0),
		Roles:           make([]string, 0),
		ComplianceFlags: make([]string, 0),
		Custom:          make(map[string]string),
	}
}

// Builder patterns for metadata

// RequestMetadataBuilder provides a builder for RequestMetadata
type RequestMetadataBuilder struct {
	metadata *RequestMetadata
}

// NewRequestMetadataBuilder creates a new RequestMetadataBuilder
func NewRequestMetadataBuilder() *RequestMetadataBuilder {
	return &RequestMetadataBuilder{
		metadata: NewRequestMetadata(),
	}
}

// WithID sets the request ID
func (b *RequestMetadataBuilder) WithID(id string) *RequestMetadataBuilder {
	b.metadata.ID = id
	return b
}

// WithMethod sets the HTTP method
func (b *RequestMetadataBuilder) WithMethod(method string) *RequestMetadataBuilder {
	b.metadata.Method = method
	return b
}

// WithPath sets the request path
func (b *RequestMetadataBuilder) WithPath(path string) *RequestMetadataBuilder {
	b.metadata.Path = path
	return b
}

// WithUserAgent sets the user agent
func (b *RequestMetadataBuilder) WithUserAgent(userAgent string) *RequestMetadataBuilder {
	b.metadata.UserAgent = userAgent
	return b
}

// WithUserID sets the user ID
func (b *RequestMetadataBuilder) WithUserID(userID string) *RequestMetadataBuilder {
	b.metadata.UserID = userID
	return b
}

// WithTenantID sets the tenant ID
func (b *RequestMetadataBuilder) WithTenantID(tenantID string) *RequestMetadataBuilder {
	b.metadata.TenantID = tenantID
	return b
}

// WithSessionID sets the session ID
func (b *RequestMetadataBuilder) WithSessionID(sessionID string) *RequestMetadataBuilder {
	b.metadata.SessionID = sessionID
	return b
}

// WithCorrelationID sets the correlation ID
func (b *RequestMetadataBuilder) WithCorrelationID(correlationID string) *RequestMetadataBuilder {
	b.metadata.CorrelationID = correlationID
	return b
}

// WithHeader adds a header
func (b *RequestMetadataBuilder) WithHeader(key, value string) *RequestMetadataBuilder {
	b.metadata.Headers[key] = value
	return b
}

// WithCustomField adds a custom field
func (b *RequestMetadataBuilder) WithCustomField(key, value string) *RequestMetadataBuilder {
	b.metadata.Custom[key] = value
	return b
}

// Build returns the built RequestMetadata
func (b *RequestMetadataBuilder) Build() *RequestMetadata {
	return b.metadata
}

// BusinessMetadataBuilder provides a builder for BusinessMetadata
type BusinessMetadataBuilder struct {
	metadata *BusinessMetadata
}

// NewBusinessMetadataBuilder creates a new BusinessMetadataBuilder
func NewBusinessMetadataBuilder() *BusinessMetadataBuilder {
	return &BusinessMetadataBuilder{
		metadata: NewBusinessMetadata(),
	}
}

// WithOperation sets the operation
func (b *BusinessMetadataBuilder) WithOperation(operation string) *BusinessMetadataBuilder {
	b.metadata.Operation = operation
	return b
}

// WithResource sets the resource
func (b *BusinessMetadataBuilder) WithResource(resource string) *BusinessMetadataBuilder {
	b.metadata.Resource = resource
	return b
}

// WithAction sets the action
func (b *BusinessMetadataBuilder) WithAction(action string) *BusinessMetadataBuilder {
	b.metadata.Action = action
	return b
}

// WithEntity sets the entity
func (b *BusinessMetadataBuilder) WithEntity(entity string) *BusinessMetadataBuilder {
	b.metadata.Entity = entity
	return b
}

// WithEntityID sets the entity ID
func (b *BusinessMetadataBuilder) WithEntityID(entityID string) *BusinessMetadataBuilder {
	b.metadata.EntityID = entityID
	return b
}

// WithFeature sets the feature
func (b *BusinessMetadataBuilder) WithFeature(feature string) *BusinessMetadataBuilder {
	b.metadata.Feature = feature
	return b
}

// WithExperiment sets the experiment
func (b *BusinessMetadataBuilder) WithExperiment(experiment string) *BusinessMetadataBuilder {
	b.metadata.Experiment = experiment
	return b
}

// WithCampaign sets the campaign
func (b *BusinessMetadataBuilder) WithCampaign(campaign string) *BusinessMetadataBuilder {
	b.metadata.Campaign = campaign
	return b
}

// WithSource sets the source
func (b *BusinessMetadataBuilder) WithSource(source string) *BusinessMetadataBuilder {
	b.metadata.Source = source
	return b
}

// WithClientType sets the client type
func (b *BusinessMetadataBuilder) WithClientType(clientType string) *BusinessMetadataBuilder {
	b.metadata.ClientType = clientType
	return b
}

// WithClientVersion sets the client version
func (b *BusinessMetadataBuilder) WithClientVersion(clientVersion string) *BusinessMetadataBuilder {
	b.metadata.ClientVersion = clientVersion
	return b
}

// WithCustomField adds a custom field
func (b *BusinessMetadataBuilder) WithCustomField(key, value string) *BusinessMetadataBuilder {
	b.metadata.Custom[key] = value
	return b
}

// Build returns the built BusinessMetadata
func (b *BusinessMetadataBuilder) Build() *BusinessMetadata {
	return b.metadata
}

// SecurityMetadataBuilder provides a builder for SecurityMetadata
type SecurityMetadataBuilder struct {
	metadata *SecurityMetadata
}

// NewSecurityMetadataBuilder creates a new SecurityMetadataBuilder
func NewSecurityMetadataBuilder() *SecurityMetadataBuilder {
	return &SecurityMetadataBuilder{
		metadata: NewSecurityMetadata(),
	}
}

// WithAuthMethod sets the authentication method
func (b *SecurityMetadataBuilder) WithAuthMethod(authMethod string) *SecurityMetadataBuilder {
	b.metadata.AuthMethod = authMethod
	return b
}

// WithAuthProvider sets the authentication provider
func (b *SecurityMetadataBuilder) WithAuthProvider(authProvider string) *SecurityMetadataBuilder {
	b.metadata.AuthProvider = authProvider
	return b
}

// WithSecurityLevel sets the security level
func (b *SecurityMetadataBuilder) WithSecurityLevel(securityLevel string) *SecurityMetadataBuilder {
	b.metadata.SecurityLevel = securityLevel
	return b
}

// WithRiskLevel sets the risk level
func (b *SecurityMetadataBuilder) WithRiskLevel(riskLevel string) *SecurityMetadataBuilder {
	b.metadata.RiskLevel = riskLevel
	return b
}

// WithThreatScore sets the threat score
func (b *SecurityMetadataBuilder) WithThreatScore(threatScore float64) *SecurityMetadataBuilder {
	b.metadata.ThreatScore = threatScore
	return b
}

// WithRole adds a role
func (b *SecurityMetadataBuilder) WithRole(role string) *SecurityMetadataBuilder {
	b.metadata.Roles = append(b.metadata.Roles, role)
	return b
}

// WithPermission adds a permission
func (b *SecurityMetadataBuilder) WithPermission(permission string) *SecurityMetadataBuilder {
	b.metadata.Permissions = append(b.metadata.Permissions, permission)
	return b
}

// WithComplianceFlag adds a compliance flag
func (b *SecurityMetadataBuilder) WithComplianceFlag(flag string) *SecurityMetadataBuilder {
	b.metadata.ComplianceFlags = append(b.metadata.ComplianceFlags, flag)
	return b
}

// WithAuditRequired sets audit requirement
func (b *SecurityMetadataBuilder) WithAuditRequired(required bool) *SecurityMetadataBuilder {
	b.metadata.AuditRequired = required
	return b
}

// WithPII sets PII flag
func (b *SecurityMetadataBuilder) WithPII(pii bool) *SecurityMetadataBuilder {
	b.metadata.PII = pii
	return b
}

// WithSensitive sets sensitive flag
func (b *SecurityMetadataBuilder) WithSensitive(sensitive bool) *SecurityMetadataBuilder {
	b.metadata.Sensitive = sensitive
	return b
}

// WithCustomField adds a custom field
func (b *SecurityMetadataBuilder) WithCustomField(key, value string) *SecurityMetadataBuilder {
	b.metadata.Custom[key] = value
	return b
}

// Build returns the built SecurityMetadata
func (b *SecurityMetadataBuilder) Build() *SecurityMetadata {
	return b.metadata
}
