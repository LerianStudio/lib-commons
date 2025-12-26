package poolmanager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Extractor defines the interface for extracting tenant IDs from various sources.
type Extractor interface {
	// ExtractFromJWT extracts the tenant ID from a JWT token's claims.
	// It parses the token payload and retrieves the value of the configured claim key.
	// Returns an error if the token is invalid or the claim is missing/empty.
	ExtractFromJWT(token string) (string, error)

	// ExtractFromContext extracts the tenant ID from the context.
	// It looks for the TenantContextKey in the context and returns its value.
	// Returns an error if the context is nil or the tenant ID is missing/invalid.
	ExtractFromContext(ctx context.Context) (string, error)
}

// extractorImpl is the default implementation of the Extractor interface.
type extractorImpl struct {
	claimKey string
}

// NewExtractor creates a new Extractor with the specified claim key.
// If claimKey is empty, it defaults to "tenantId".
func NewExtractor(claimKey string) Extractor {
	if claimKey == "" {
		claimKey = "tenantId"
	}

	return &extractorImpl{
		claimKey: claimKey,
	}
}

// ExtractFromJWT extracts the tenant ID from a JWT token's claims.
func (e *extractorImpl) ExtractFromJWT(token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("empty token provided")
	}

	// JWT format: header.payload.signature
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid JWT format: expected 3 parts, got %d", len(parts))
	}

	// Decode the payload (second part)
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("failed to decode payload: %w", err)
	}

	// Parse the claims
	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return "", fmt.Errorf("failed to parse claims: %w", err)
	}

	// Extract the tenant ID from the configured claim key
	claimValue, ok := claims[e.claimKey]
	if !ok {
		return "", fmt.Errorf("claim not found: %s", e.claimKey)
	}

	// Ensure the claim value is a string
	tenantID, ok := claimValue.(string)
	if !ok {
		return "", fmt.Errorf("claim %s is not a string: got %T", e.claimKey, claimValue)
	}

	// Validate that the tenant ID is not empty
	if strings.TrimSpace(tenantID) == "" {
		return "", fmt.Errorf("tenant ID is empty")
	}

	return tenantID, nil
}

// ExtractFromContext extracts the tenant ID from the context.
func (e *extractorImpl) ExtractFromContext(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("nil context provided")
	}

	// Get the tenant ID from context
	value := ctx.Value(TenantContextKey)
	if value == nil {
		return "", ErrTenantContextMissing
	}

	// Ensure the value is a string
	tenantID, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("tenant ID in context is not a string: got %T", value)
	}

	// Validate that the tenant ID is not empty
	if strings.TrimSpace(tenantID) == "" {
		return "", fmt.Errorf("tenant ID is empty")
	}

	return tenantID, nil
}
