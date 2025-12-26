package poolmanager

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockChannel implements a mock for amqp.Channel methods we need
type mockChannel struct {
	lastPublish    *publishCapture
	publishError   error
	publishCalled  bool
	isClosed       bool
	closedError    error
}

type publishCapture struct {
	exchange   string
	routingKey string
	mandatory  bool
	immediate  bool
	msg        amqp.Publishing
}

func (m *mockChannel) PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error {
	m.publishCalled = true
	m.lastPublish = &publishCapture{
		exchange:   exchange,
		routingKey: key,
		mandatory:  mandatory,
		immediate:  immediate,
		msg:        msg,
	}
	return m.publishError
}

func (m *mockChannel) IsClosed() bool {
	return m.isClosed
}

func (m *mockChannel) Close() error {
	m.isClosed = true
	return m.closedError
}

// TestNewTenantRabbitMQPublisher tests the constructor behavior
func TestNewTenantRabbitMQPublisher(t *testing.T) {
	tests := []struct {
		name     string
		channel  AMQPChannel
		tenantID string
		wantNil  bool
	}{
		{
			name:     "valid channel and tenant ID",
			channel:  &mockChannel{},
			tenantID: "tenant-123",
			wantNil:  false,
		},
		{
			name:     "valid channel with UUID tenant ID",
			channel:  &mockChannel{},
			tenantID: "550e8400-e29b-41d4-a716-446655440000",
			wantNil:  false,
		},
		{
			name:     "nil channel returns nil",
			channel:  nil,
			tenantID: "tenant-123",
			wantNil:  true,
		},
		{
			name:     "empty tenant ID returns nil",
			channel:  &mockChannel{},
			tenantID: "",
			wantNil:  true,
		},
		{
			name:     "whitespace only tenant ID returns nil",
			channel:  &mockChannel{},
			tenantID: "   ",
			wantNil:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NewTenantRabbitMQPublisher(tt.channel, tt.tenantID)
			if tt.wantNil {
				assert.Nil(t, result, "expected nil TenantRabbitMQPublisher")
			} else {
				assert.NotNil(t, result, "expected non-nil TenantRabbitMQPublisher")
			}
		})
	}
}

// TestTenantRabbitMQPublisher_GetTenantID tests retrieving the tenant ID
func TestTenantRabbitMQPublisher_GetTenantID(t *testing.T) {
	tests := []struct {
		name             string
		tenantID         string
		expectedTenantID string
	}{
		{
			name:             "returns correct tenant ID",
			tenantID:         "tenant-abc",
			expectedTenantID: "tenant-abc",
		},
		{
			name:             "returns UUID tenant ID",
			tenantID:         "550e8400-e29b-41d4-a716-446655440000",
			expectedTenantID: "550e8400-e29b-41d4-a716-446655440000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			publisher := NewTenantRabbitMQPublisher(&mockChannel{}, tt.tenantID)
			require.NotNil(t, publisher)
			assert.Equal(t, tt.expectedTenantID, publisher.GetTenantID())
		})
	}
}

// TestTenantRabbitMQPublisher_Publish tests the publish behavior
func TestTenantRabbitMQPublisher_Publish(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name           string
		tenantID       string
		exchange       string
		routingKey     string
		msg            amqp.Publishing
		publishError   error
		expectError    bool
		validateHeader func(t *testing.T, msg amqp.Publishing)
	}{
		{
			name:       "injects X-Tenant-ID header",
			tenantID:   "tenant-123",
			exchange:   "test-exchange",
			routingKey: "test-key",
			msg: amqp.Publishing{
				ContentType: "application/json",
				Body:        []byte(`{"test": "data"}`),
			},
			expectError: false,
			validateHeader: func(t *testing.T, msg amqp.Publishing) {
				require.NotNil(t, msg.Headers)
				tenantID, ok := msg.Headers[TenantIDHeader]
				require.True(t, ok, "X-Tenant-ID header should exist")
				assert.Equal(t, "tenant-123", tenantID)
			},
		},
		{
			name:       "preserves existing headers",
			tenantID:   "tenant-456",
			exchange:   "test-exchange",
			routingKey: "test-key",
			msg: amqp.Publishing{
				ContentType: "application/json",
				Headers: amqp.Table{
					"existing-header": "existing-value",
				},
				Body: []byte(`{"test": "data"}`),
			},
			expectError: false,
			validateHeader: func(t *testing.T, msg amqp.Publishing) {
				require.NotNil(t, msg.Headers)
				// Check tenant header
				tenantID, ok := msg.Headers[TenantIDHeader]
				require.True(t, ok, "X-Tenant-ID header should exist")
				assert.Equal(t, "tenant-456", tenantID)
				// Check existing header is preserved
				existing, ok := msg.Headers["existing-header"]
				require.True(t, ok, "existing-header should be preserved")
				assert.Equal(t, "existing-value", existing)
			},
		},
		{
			name:       "overwrites existing X-Tenant-ID header",
			tenantID:   "correct-tenant",
			exchange:   "test-exchange",
			routingKey: "test-key",
			msg: amqp.Publishing{
				ContentType: "application/json",
				Headers: amqp.Table{
					TenantIDHeader: "wrong-tenant",
				},
				Body: []byte(`{"test": "data"}`),
			},
			expectError: false,
			validateHeader: func(t *testing.T, msg amqp.Publishing) {
				require.NotNil(t, msg.Headers)
				tenantID, ok := msg.Headers[TenantIDHeader]
				require.True(t, ok, "X-Tenant-ID header should exist")
				assert.Equal(t, "correct-tenant", tenantID, "X-Tenant-ID should be overwritten with publisher's tenant ID")
			},
		},
		{
			name:       "handles publish error",
			tenantID:   "tenant-789",
			exchange:   "test-exchange",
			routingKey: "test-key",
			msg: amqp.Publishing{
				ContentType: "application/json",
				Body:        []byte(`{"test": "data"}`),
			},
			publishError: assert.AnError,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockChannel{
				publishError: tt.publishError,
			}

			publisher := NewTenantRabbitMQPublisher(mock, tt.tenantID)
			require.NotNil(t, publisher)

			err := publisher.Publish(ctx, tt.exchange, tt.routingKey, tt.msg)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				require.True(t, mock.publishCalled, "Publish should have been called")
				assert.Equal(t, tt.exchange, mock.lastPublish.exchange)
				assert.Equal(t, tt.routingKey, mock.lastPublish.routingKey)

				if tt.validateHeader != nil {
					tt.validateHeader(t, mock.lastPublish.msg)
				}
			}
		})
	}
}

// TestTenantRabbitMQPublisher_Publish_NilContext tests nil context handling
func TestTenantRabbitMQPublisher_Publish_NilContext(t *testing.T) {
	mock := &mockChannel{}
	publisher := NewTenantRabbitMQPublisher(mock, "tenant-123")
	require.NotNil(t, publisher)

	//nolint:staticcheck // SA1012: Testing nil context handling intentionally
	err := publisher.Publish(nil, "exchange", "key", amqp.Publishing{})
	assert.Error(t, err, "publish with nil context should error")
}

// TestExtractTenantFromMessage tests extracting tenant ID from AMQP messages
func TestExtractTenantFromMessage(t *testing.T) {
	tests := []struct {
		name           string
		msg            amqp.Delivery
		expectedTenant string
		expectError    bool
	}{
		{
			name: "extracts tenant ID from header",
			msg: amqp.Delivery{
				Headers: amqp.Table{
					TenantIDHeader: "tenant-123",
				},
			},
			expectedTenant: "tenant-123",
			expectError:    false,
		},
		{
			name: "extracts UUID tenant ID",
			msg: amqp.Delivery{
				Headers: amqp.Table{
					TenantIDHeader: "550e8400-e29b-41d4-a716-446655440000",
				},
			},
			expectedTenant: "550e8400-e29b-41d4-a716-446655440000",
			expectError:    false,
		},
		{
			name: "error when headers are nil",
			msg: amqp.Delivery{
				Headers: nil,
			},
			expectError: true,
		},
		{
			name: "error when header is missing",
			msg: amqp.Delivery{
				Headers: amqp.Table{
					"other-header": "value",
				},
			},
			expectError: true,
		},
		{
			name: "error when header is empty string",
			msg: amqp.Delivery{
				Headers: amqp.Table{
					TenantIDHeader: "",
				},
			},
			expectError: true,
		},
		{
			name: "error when header is wrong type (int)",
			msg: amqp.Delivery{
				Headers: amqp.Table{
					TenantIDHeader: 123,
				},
			},
			expectError: true,
		},
		{
			name: "error when header is wrong type (nil)",
			msg: amqp.Delivery{
				Headers: amqp.Table{
					TenantIDHeader: nil,
				},
			},
			expectError: true,
		},
		{
			name: "error when header is whitespace only",
			msg: amqp.Delivery{
				Headers: amqp.Table{
					TenantIDHeader: "   ",
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tenantID, err := ExtractTenantFromMessage(tt.msg)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, tenantID)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedTenant, tenantID)
			}
		})
	}
}

// TestTenantIsolation tests that different publishers inject their own tenant IDs
func TestTenantIsolation(t *testing.T) {
	ctx := context.Background()

	mock1 := &mockChannel{}
	mock2 := &mockChannel{}

	publisher1 := NewTenantRabbitMQPublisher(mock1, "tenant-1")
	publisher2 := NewTenantRabbitMQPublisher(mock2, "tenant-2")

	require.NotNil(t, publisher1)
	require.NotNil(t, publisher2)

	// Publish from both publishers
	err := publisher1.Publish(ctx, "exchange", "key", amqp.Publishing{Body: []byte("msg1")})
	require.NoError(t, err)

	err = publisher2.Publish(ctx, "exchange", "key", amqp.Publishing{Body: []byte("msg2")})
	require.NoError(t, err)

	// Verify each message has the correct tenant ID
	tenant1ID, ok := mock1.lastPublish.msg.Headers[TenantIDHeader]
	require.True(t, ok)
	assert.Equal(t, "tenant-1", tenant1ID, "first publisher should inject tenant-1")

	tenant2ID, ok := mock2.lastPublish.msg.Headers[TenantIDHeader]
	require.True(t, ok)
	assert.Equal(t, "tenant-2", tenant2ID, "second publisher should inject tenant-2")
}

// TestPublishWithHeaders tests that various header combinations work correctly
func TestPublishWithHeaders(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name           string
		initialHeaders amqp.Table
		expectedCount  int
	}{
		{
			name:           "nil headers creates new table",
			initialHeaders: nil,
			expectedCount:  1, // only X-Tenant-ID
		},
		{
			name:           "empty headers adds tenant header",
			initialHeaders: amqp.Table{},
			expectedCount:  1, // only X-Tenant-ID
		},
		{
			name: "preserves multiple headers",
			initialHeaders: amqp.Table{
				"header1": "value1",
				"header2": "value2",
				"header3": "value3",
			},
			expectedCount: 4, // 3 existing + X-Tenant-ID
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockChannel{}
			publisher := NewTenantRabbitMQPublisher(mock, "tenant-test")
			require.NotNil(t, publisher)

			msg := amqp.Publishing{
				Headers: tt.initialHeaders,
				Body:    []byte("test"),
			}

			err := publisher.Publish(ctx, "exchange", "key", msg)
			require.NoError(t, err)

			assert.Len(t, mock.lastPublish.msg.Headers, tt.expectedCount)
			assert.Contains(t, mock.lastPublish.msg.Headers, TenantIDHeader)
		})
	}
}
