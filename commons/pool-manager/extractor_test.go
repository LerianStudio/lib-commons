package poolmanager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestJWT creates a test JWT token with the given claims.
// Note: This is for testing purposes only and does not create cryptographically valid signatures.
func createTestJWT(claims map[string]any) string {
	// JWT Header (typ: JWT, alg: HS256)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"HS256"}`))

	// JWT Payload (claims)
	payloadBytes, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)

	// Fake signature (not cryptographically valid, but sufficient for parsing tests)
	signature := base64.RawURLEncoding.EncodeToString([]byte("test-signature"))

	return header + "." + payload + "." + signature
}

func TestNewExtractor(t *testing.T) {
	tests := []struct {
		name        string
		claimKey    string
		expectedKey string
	}{
		{
			name:        "Should use provided claim key",
			claimKey:    "tenant_id",
			expectedKey: "tenant_id",
		},
		{
			name:        "Should use 'tenantId' as default when empty claim key provided",
			claimKey:    "",
			expectedKey: "tenantId",
		},
		{
			name:        "Should use custom claim key - organization_id",
			claimKey:    "organization_id",
			expectedKey: "organization_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := NewExtractor(tt.claimKey)
			require.NotNil(t, extractor, "Extractor should not be nil")

			// Verify the extractor was created with the expected claim key
			impl, ok := extractor.(*extractorImpl)
			require.True(t, ok, "Should return *extractorImpl")
			assert.Equal(t, tt.expectedKey, impl.claimKey, "Claim key should match expected")
		})
	}
}

func TestExtractor_ExtractFromJWT(t *testing.T) {
	tests := []struct {
		name        string
		claimKey    string
		token       string
		wantTenant  string
		wantErr     bool
		errContains string
	}{
		{
			name:     "Should extract tenant from 'owner' claim",
			claimKey: "owner",
			token: createTestJWT(map[string]any{
				"sub":   "user123",
				"owner": "tenant-abc-123",
				"exp":   1735689600,
			}),
			wantTenant: "tenant-abc-123",
			wantErr:    false,
		},
		{
			name:     "Should extract tenant from custom 'tenant_id' claim",
			claimKey: "tenant_id",
			token: createTestJWT(map[string]any{
				"sub":       "user456",
				"tenant_id": "tenant-xyz-789",
				"exp":       1735689600,
			}),
			wantTenant: "tenant-xyz-789",
			wantErr:    false,
		},
		{
			name:     "Should extract tenant from 'organization_id' claim",
			claimKey: "organization_id",
			token: createTestJWT(map[string]any{
				"sub":             "user789",
				"organization_id": "org-def-456",
				"exp":             1735689600,
			}),
			wantTenant: "org-def-456",
			wantErr:    false,
		},
		{
			name:        "Should return error for empty token",
			claimKey:    "owner",
			token:       "",
			wantTenant:  "",
			wantErr:     true,
			errContains: "empty token",
		},
		{
			name:        "Should return error for invalid JWT format (no dots)",
			claimKey:    "owner",
			token:       "invalid-token-without-dots",
			wantTenant:  "",
			wantErr:     true,
			errContains: "invalid JWT format",
		},
		{
			name:        "Should return error for invalid JWT format (only one dot)",
			claimKey:    "owner",
			token:       "header.payload",
			wantTenant:  "",
			wantErr:     true,
			errContains: "invalid JWT format",
		},
		{
			name:        "Should return error for invalid base64 in payload",
			claimKey:    "owner",
			token:       "header.!!!invalid-base64!!!.signature",
			wantTenant:  "",
			wantErr:     true,
			errContains: "decode payload",
		},
		{
			name:        "Should return error for invalid JSON in payload",
			claimKey:    "owner",
			token:       "header." + base64.RawURLEncoding.EncodeToString([]byte("not-valid-json")) + ".signature",
			wantTenant:  "",
			wantErr:     true,
			errContains: "parse claims",
		},
		{
			name:     "Should return error when claim key is missing",
			claimKey: "owner",
			token: createTestJWT(map[string]any{
				"sub":       "user123",
				"tenant_id": "tenant-abc",
				"exp":       1735689600,
			}),
			wantTenant:  "",
			wantErr:     true,
			errContains: "claim not found",
		},
		{
			name:     "Should return error when claim value is not a string",
			claimKey: "owner",
			token: createTestJWT(map[string]any{
				"sub":   "user123",
				"owner": 12345, // numeric value instead of string
				"exp":   1735689600,
			}),
			wantTenant:  "",
			wantErr:     true,
			errContains: "not a string",
		},
		{
			name:     "Should return error when claim value is empty string",
			claimKey: "owner",
			token: createTestJWT(map[string]any{
				"sub":   "user123",
				"owner": "",
				"exp":   1735689600,
			}),
			wantTenant:  "",
			wantErr:     true,
			errContains: "empty",
		},
		{
			name:     "Should handle UUID tenant ID",
			claimKey: "owner",
			token: createTestJWT(map[string]any{
				"sub":   "user123",
				"owner": "550e8400-e29b-41d4-a716-446655440000",
				"exp":   1735689600,
			}),
			wantTenant: "550e8400-e29b-41d4-a716-446655440000",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := NewExtractor(tt.claimKey)
			require.NotNil(t, extractor)

			tenantID, err := extractor.ExtractFromJWT(tt.token)

			if tt.wantErr {
				require.Error(t, err, "Expected error but got none")
				assert.Contains(t, err.Error(), tt.errContains, "Error message should contain expected text")
				assert.Empty(t, tenantID, "Tenant ID should be empty on error")
			} else {
				require.NoError(t, err, "Expected no error but got: %v", err)
				assert.Equal(t, tt.wantTenant, tenantID, "Tenant ID should match expected")
			}
		})
	}
}

func TestExtractor_ExtractFromContext(t *testing.T) {
	tests := []struct {
		name        string
		claimKey    string
		setupCtx    func() context.Context
		wantTenant  string
		wantErr     bool
		errContains string
	}{
		{
			name:     "Should extract tenant from context",
			claimKey: "owner",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), TenantContextKey, "tenant-from-context")
			},
			wantTenant: "tenant-from-context",
			wantErr:    false,
		},
		{
			name:     "Should extract UUID tenant from context",
			claimKey: "owner",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), TenantContextKey, "550e8400-e29b-41d4-a716-446655440000")
			},
			wantTenant: "550e8400-e29b-41d4-a716-446655440000",
			wantErr:    false,
		},
		{
			name:     "Should return error for nil context",
			claimKey: "owner",
			setupCtx: func() context.Context {
				return nil
			},
			wantTenant:  "",
			wantErr:     true,
			errContains: "nil context",
		},
		{
			name:     "Should return error when tenant is not in context",
			claimKey: "owner",
			setupCtx: func() context.Context {
				return context.Background()
			},
			wantTenant:  "",
			wantErr:     true,
			errContains: "tenant context missing",
		},
		{
			name:     "Should return error when tenant value is not a string",
			claimKey: "owner",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), TenantContextKey, 12345)
			},
			wantTenant:  "",
			wantErr:     true,
			errContains: "not a string",
		},
		{
			name:     "Should return error when tenant value is empty string",
			claimKey: "owner",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), TenantContextKey, "")
			},
			wantTenant:  "",
			wantErr:     true,
			errContains: "empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := NewExtractor(tt.claimKey)
			require.NotNil(t, extractor)

			ctx := tt.setupCtx()
			tenantID, err := extractor.ExtractFromContext(ctx)

			if tt.wantErr {
				require.Error(t, err, "Expected error but got none")
				assert.Contains(t, err.Error(), tt.errContains, "Error message should contain expected text")
				assert.Empty(t, tenantID, "Tenant ID should be empty on error")
			} else {
				require.NoError(t, err, "Expected no error but got: %v", err)
				assert.Equal(t, tt.wantTenant, tenantID, "Tenant ID should match expected")
			}
		})
	}
}

func TestExtractor_Interface(t *testing.T) {
	// Verify that extractorImpl implements Extractor interface
	var _ Extractor = (*extractorImpl)(nil)
	var _ Extractor = NewExtractor("owner")
}

func TestExtractor_DefaultClaimKey(t *testing.T) {
	// Test that empty string defaults to "tenantId"
	extractor := NewExtractor("")
	require.NotNil(t, extractor)

	token := createTestJWT(map[string]any{
		"sub":      "user123",
		"tenantId": "tenant-default-test",
		"exp":      1735689600,
	})

	tenantID, err := extractor.ExtractFromJWT(token)
	require.NoError(t, err)
	assert.Equal(t, "tenant-default-test", tenantID)
}
