package poolmanager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	libLog "github.com/LerianStudio/lib-commons/v2/commons/log"
)

// Extractor defines the interface for extracting tenant IDs from various sources.
type Extractor interface {
	// ExtractFromJWT extracts the tenant ID from a JWT token's claims.
	// It parses the token payload and retrieves the value of the configured claim key.
	// Returns an error if the token is invalid or the claim is missing/empty.
	ExtractFromJWT(token string) (string, error)

	// ExtractFromContext extracts the tenant ID from the context.
	// It looks for the TenantIDContextKey in the context and returns its value.
	// Returns an error if the context is nil or the tenant ID is missing/invalid.
	ExtractFromContext(ctx context.Context) (string, error)
}

// extractorImpl is the default implementation of the Extractor interface.
type extractorImpl struct {
	claimKey string
	logger   libLog.Logger
}

// ExtractorOption is a function that configures an Extractor.
type ExtractorOption func(*extractorImpl)

// WithExtractorLogger sets the logger for the extractor.
func WithExtractorLogger(logger libLog.Logger) ExtractorOption {
	return func(e *extractorImpl) {
		e.logger = logger
	}
}

// NewExtractor creates a new Extractor with the specified claim key.
// If claimKey is empty, it defaults to "tenantId".
func NewExtractor(claimKey string, opts ...ExtractorOption) Extractor {
	if claimKey == "" {
		claimKey = "tenantId"
	}

	e := &extractorImpl{
		claimKey: claimKey,
	}

	for _, opt := range opts {
		opt(e)
	}

	return e
}

// ExtractFromJWT extracts the tenant ID from a JWT token's claims.
func (e *extractorImpl) ExtractFromJWT(token string) (string, error) {
	if token == "" {
		if e.logger != nil {
			e.logger.Warn("ExtractFromJWT: empty token provided")
		}
		return "", fmt.Errorf("empty token provided")
	}

	// JWT format: header.payload.signature
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		if e.logger != nil {
			e.logger.Warnf("ExtractFromJWT: invalid JWT format, expected 3 parts, got %d", len(parts))
		}
		return "", fmt.Errorf("invalid JWT format: expected 3 parts, got %d", len(parts))
	}

	// Decode the payload (second part)
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		if e.logger != nil {
			e.logger.Warnf("ExtractFromJWT: failed to decode payload: %v", err)
		}
		return "", fmt.Errorf("failed to decode payload: %w", err)
	}

	// Parse the claims
	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		if e.logger != nil {
			e.logger.Warnf("ExtractFromJWT: failed to parse claims: %v", err)
		}
		return "", fmt.Errorf("failed to parse claims: %w", err)
	}

	// Extract the tenant ID from the configured claim key
	// Support dot notation for nested claims (e.g., "properties.tenantId")
	claimValue, err := e.getNestedClaim(claims, e.claimKey)
	if err != nil {
		if e.logger != nil {
			e.logger.Warnf("ExtractFromJWT: %v", err)
		}
		return "", err
	}

	// Ensure the claim value is a string
	tenantID, ok := claimValue.(string)
	if !ok {
		if e.logger != nil {
			e.logger.Warnf("ExtractFromJWT: claim %s is not a string: got %T", e.claimKey, claimValue)
		}
		return "", fmt.Errorf("claim %s is not a string: got %T", e.claimKey, claimValue)
	}

	// Validate that the tenant ID is not empty
	if strings.TrimSpace(tenantID) == "" {
		if e.logger != nil {
			e.logger.Warn("ExtractFromJWT: tenant ID is empty")
		}
		return "", fmt.Errorf("tenant ID is empty")
	}

	if e.logger != nil {
		e.logger.Infof("ExtractFromJWT: successfully extracted tenant ID %s", tenantID)
	}

	return tenantID, nil
}

// getNestedClaim retrieves a claim value supporting dot notation for nested paths.
// For example, "properties.tenantId" will navigate claims["properties"]["tenantId"].
func (e *extractorImpl) getNestedClaim(claims map[string]any, claimKey string) (any, error) {
	keys := strings.Split(claimKey, ".")

	var current any = claims
	for i, key := range keys {
		currentMap, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("claim path %s: expected object at %s, got %T",
				claimKey, strings.Join(keys[:i], "."), current)
		}

		value, exists := currentMap[key]
		if !exists {
			return nil, fmt.Errorf("claim not found: %s", claimKey)
		}

		current = value
	}

	return current, nil
}

// ExtractFromContext extracts the tenant ID from the context.
func (e *extractorImpl) ExtractFromContext(ctx context.Context) (string, error) {
	if ctx == nil {
		if e.logger != nil {
			e.logger.Warn("ExtractFromContext: nil context provided")
		}
		return "", fmt.Errorf("nil context provided")
	}

	// Get the tenant ID from context
	value := ctx.Value(TenantIDContextKey)
	if value == nil {
		if e.logger != nil {
			e.logger.Warn("ExtractFromContext: tenant context key missing")
		}
		return "", ErrTenantContextMissing
	}

	// Ensure the value is a string
	tenantID, ok := value.(string)
	if !ok {
		if e.logger != nil {
			e.logger.Warnf("ExtractFromContext: tenant ID is not a string: got %T", value)
		}
		return "", fmt.Errorf("tenant ID in context is not a string: got %T", value)
	}

	// Validate that the tenant ID is not empty
	if strings.TrimSpace(tenantID) == "" {
		if e.logger != nil {
			e.logger.Warn("ExtractFromContext: tenant ID is empty")
		}
		return "", fmt.Errorf("tenant ID is empty")
	}

	if e.logger != nil {
		e.logger.Infof("ExtractFromContext: successfully extracted tenant ID %s", tenantID)
	}

	return tenantID, nil
}
