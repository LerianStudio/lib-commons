package poolmanager

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"
)

const TenantIDMessageHeader = "X-Tenant-ID"

// PublishingWithTenant creates amqp.Publishing with tenant header from context.
func PublishingWithTenant(ctx context.Context, contentType string, body []byte) amqp.Publishing {
	tenantID := GetTenantIDFromContext(ctx)

	headers := amqp.Table{}
	if tenantID != "" {
		headers[TenantIDMessageHeader] = tenantID
	}

	return amqp.Publishing{
		ContentType: contentType,
		Headers:     headers,
		Body:        body,
	}
}

// ExtractTenantFromMessage extracts tenantId from message headers and returns context with tenantId.
func ExtractTenantFromMessage(msg amqp.Delivery) context.Context {
	ctx := context.Background()

	tenantID := GetTenantFromHeaders(msg.Headers)
	if tenantID != "" {
		ctx = SetTenantIDInContext(ctx, tenantID)
	}

	return ctx
}

// SetTenantHeader adds tenant ID to amqp.Table headers.
// If headers is nil, creates a new amqp.Table.
func SetTenantHeader(headers amqp.Table, tenantID string) amqp.Table {
	if headers == nil {
		headers = amqp.Table{}
	}
	if tenantID != "" {
		headers[TenantIDMessageHeader] = tenantID
	}
	return headers
}

// GetTenantFromHeaders extracts tenant ID from amqp.Table headers.
// Returns empty string if not found.
func GetTenantFromHeaders(headers amqp.Table) string {
	if headers == nil {
		return ""
	}

	if tenantID, ok := headers[TenantIDMessageHeader].(string); ok {
		return tenantID
	}

	return ""
}
