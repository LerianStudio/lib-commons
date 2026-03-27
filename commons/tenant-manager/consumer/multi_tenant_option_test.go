// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
)

// --------------------------------------------------------------------------
// Constructor parameter validation tests
// --------------------------------------------------------------------------

func TestNewMultiTenantConsumer_NilRabbitmq_Accepted(t *testing.T) {
	t.Parallel()

	server := setupTenantManagerAPIServer(t, nil)

	// RabbitMQ is now optional -- nil is accepted (HTTP-only mode)
	consumer, err := NewMultiTenantConsumerWithError(
		newTestConfig(server.URL),
		testutil.NewMockLogger(),
	)

	require.NoError(t, err, "nil rabbitmq should be accepted (HTTP-only mode)")
	require.NotNil(t, consumer, "consumer should not be nil")
	assert.Nil(t, consumer.rabbitmq, "rabbitmq should be nil when not provided")

	require.NoError(t, consumer.Close())
}

func TestNewMultiTenantConsumer_ValidConstructor_Accepted(t *testing.T) {
	t.Parallel()

	server := setupTenantManagerAPIServer(t, nil)

	consumer, err := NewMultiTenantConsumerWithError(
		newTestConfig(server.URL),
		testutil.NewMockLogger(),
		WithRabbitMQ(dummyRabbitMQManager()),
	)

	require.NoError(t, err, "valid constructor should be accepted")
	require.NotNil(t, consumer, "consumer should not be nil")
	assert.NotNil(t, consumer.dispatcher, "dispatcher should be created")
	assert.NotNil(t, consumer.rabbitmq, "rabbitmq should be set via WithRabbitMQ option")

	require.NoError(t, consumer.Close())
}
