package poolmanager

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
)

func TestPublishingWithTenant(t *testing.T) {
	t.Run("creates publishing with tenant header", func(t *testing.T) {
		ctx := SetTenantIDInContext(context.Background(), "banco-acme")
		body := []byte(`{"key": "value"}`)

		pub := PublishingWithTenant(ctx, "application/json", body)

		assert.Equal(t, "application/json", pub.ContentType)
		assert.Equal(t, body, pub.Body)
		assert.Equal(t, "banco-acme", pub.Headers[TenantIDMessageHeader])
	})

	t.Run("creates publishing without tenant header when no tenantID", func(t *testing.T) {
		ctx := context.Background()
		body := []byte(`{"key": "value"}`)

		pub := PublishingWithTenant(ctx, "application/json", body)

		assert.Equal(t, "application/json", pub.ContentType)
		assert.Equal(t, body, pub.Body)
		assert.NotContains(t, pub.Headers, TenantIDMessageHeader)
	})
}

func TestExtractTenantFromMessage(t *testing.T) {
	t.Run("extracts tenantID from message headers", func(t *testing.T) {
		msg := amqp.Delivery{
			Headers: amqp.Table{
				TenantIDMessageHeader: "banco-acme",
			},
		}

		ctx := ExtractTenantFromMessage(msg)

		assert.Equal(t, "banco-acme", GetTenantIDFromContext(ctx))
	})

	t.Run("returns empty context when no tenant header", func(t *testing.T) {
		msg := amqp.Delivery{
			Headers: amqp.Table{},
		}

		ctx := ExtractTenantFromMessage(msg)

		assert.Equal(t, "", GetTenantIDFromContext(ctx))
	})

	t.Run("returns empty context when headers is nil", func(t *testing.T) {
		msg := amqp.Delivery{
			Headers: nil,
		}

		ctx := ExtractTenantFromMessage(msg)

		assert.Equal(t, "", GetTenantIDFromContext(ctx))
	})
}

func TestSetTenantHeader(t *testing.T) {
	t.Run("adds tenant to existing headers", func(t *testing.T) {
		headers := amqp.Table{
			"other-header": "value",
		}

		result := SetTenantHeader(headers, "banco-acme")

		assert.Equal(t, "banco-acme", result[TenantIDMessageHeader])
		assert.Equal(t, "value", result["other-header"])
	})

	t.Run("creates headers if nil", func(t *testing.T) {
		result := SetTenantHeader(nil, "banco-acme")

		assert.NotNil(t, result)
		assert.Equal(t, "banco-acme", result[TenantIDMessageHeader])
	})

	t.Run("does not add header if tenantID is empty", func(t *testing.T) {
		headers := amqp.Table{}

		result := SetTenantHeader(headers, "")

		assert.NotContains(t, result, TenantIDMessageHeader)
	})
}

func TestGetTenantFromHeaders(t *testing.T) {
	t.Run("returns tenantID from headers", func(t *testing.T) {
		headers := amqp.Table{
			TenantIDMessageHeader: "banco-acme",
		}

		tenantID := GetTenantFromHeaders(headers)

		assert.Equal(t, "banco-acme", tenantID)
	})

	t.Run("returns empty string when header not found", func(t *testing.T) {
		headers := amqp.Table{}

		tenantID := GetTenantFromHeaders(headers)

		assert.Equal(t, "", tenantID)
	})

	t.Run("returns empty string when headers is nil", func(t *testing.T) {
		tenantID := GetTenantFromHeaders(nil)

		assert.Equal(t, "", tenantID)
	})

	t.Run("returns empty string when header is not a string", func(t *testing.T) {
		headers := amqp.Table{
			TenantIDMessageHeader: 123,
		}

		tenantID := GetTenantFromHeaders(headers)

		assert.Equal(t, "", tenantID)
	})
}
