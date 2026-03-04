package postgres

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v3/commons/log"
	"github.com/LerianStudio/lib-commons/v3/commons/pointers"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

func TestPostgresConnection_MultiStatementEnabled_Nil_DefaultsToTrue(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockLogger := log.NewMockLogger(ctrl)

	pc := &PostgresConnection{
		ConnectionStringPrimary: "postgres://user:pass@localhost:5432/testdb",
		ConnectionStringReplica: "postgres://user:pass@localhost:5432/testdb",
		PrimaryDBName:           "testdb",
		ReplicaDBName:           "testdb",
		Logger:                  mockLogger,
		MaxOpenConnections:      10,
		MaxIdleConnections:      5,
		MultiStatementEnabled:   nil, // explicitly nil to test default
	}

	// Verify the field is nil
	assert.Nil(t, pc.MultiStatementEnabled, "MultiStatementEnabled should be nil by default")

	// Verify default resolution logic (using helper method from Connect())
	assert.True(t, pc.resolveMultiStatementEnabled(), "nil MultiStatementEnabled should resolve to true")
}

func TestPostgresConnection_MultiStatementEnabled_ExplicitTrue(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockLogger := log.NewMockLogger(ctrl)

	pc := &PostgresConnection{
		ConnectionStringPrimary: "postgres://user:pass@localhost:5432/testdb",
		ConnectionStringReplica: "postgres://user:pass@localhost:5432/testdb",
		PrimaryDBName:           "testdb",
		ReplicaDBName:           "testdb",
		Logger:                  mockLogger,
		MaxOpenConnections:      10,
		MaxIdleConnections:      5,
		MultiStatementEnabled:   pointers.Bool(true), // explicitly true
	}

	// Verify the field is set to true
	assert.NotNil(t, pc.MultiStatementEnabled, "MultiStatementEnabled should not be nil")
	assert.True(t, *pc.MultiStatementEnabled, "MultiStatementEnabled should be true")

	// Verify resolution logic (using helper method from Connect())
	assert.True(t, pc.resolveMultiStatementEnabled(), "explicit true should resolve to true")
}

func TestPostgresConnection_MultiStatementEnabled_ExplicitFalse(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockLogger := log.NewMockLogger(ctrl)

	pc := &PostgresConnection{
		ConnectionStringPrimary: "postgres://user:pass@localhost:5432/testdb",
		ConnectionStringReplica: "postgres://user:pass@localhost:5432/testdb",
		PrimaryDBName:           "testdb",
		ReplicaDBName:           "testdb",
		Logger:                  mockLogger,
		MaxOpenConnections:      10,
		MaxIdleConnections:      5,
		MultiStatementEnabled:   pointers.Bool(false), // explicitly false
	}

	// Verify the field is set to false
	assert.NotNil(t, pc.MultiStatementEnabled, "MultiStatementEnabled should not be nil")
	assert.False(t, *pc.MultiStatementEnabled, "MultiStatementEnabled should be false")

	// Verify resolution logic (using helper method from Connect())
	assert.False(t, pc.resolveMultiStatementEnabled(), "explicit false should resolve to false")
}

func TestPostgresConnection_MultiStatementEnabled_AllCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		multiStatementEnabled *bool
		expectedResolved      bool
		description           string
	}{
		{
			name:                  "nil_defaults_to_true",
			multiStatementEnabled: nil,
			expectedResolved:      true,
			description:           "backward compatibility - nil should default to true",
		},
		{
			name:                  "explicit_true",
			multiStatementEnabled: pointers.Bool(true),
			expectedResolved:      true,
			description:           "explicit true should resolve to true",
		},
		{
			name:                  "explicit_false",
			multiStatementEnabled: pointers.Bool(false),
			expectedResolved:      false,
			description:           "explicit false should resolve to false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)

			mockLogger := log.NewMockLogger(ctrl)

			pc := &PostgresConnection{
				ConnectionStringPrimary: "postgres://user:pass@localhost:5432/testdb",
				ConnectionStringReplica: "postgres://user:pass@localhost:5432/testdb",
				PrimaryDBName:           "testdb",
				ReplicaDBName:           "testdb",
				Logger:                  mockLogger,
				MaxOpenConnections:      10,
				MaxIdleConnections:      5,
				MultiStatementEnabled:   tt.multiStatementEnabled,
			}

			// Use the same resolution logic as Connect()
			assert.Equal(t, tt.expectedResolved, pc.resolveMultiStatementEnabled(), tt.description)
		})
	}
}
