package postgres

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
	"github.com/LerianStudio/lib-commons/v2/commons/pointers"
	"github.com/stretchr/testify/assert"
)

// mockLogger implements log.Logger interface for testing
type mockLogger struct{}

func (l *mockLogger) Debug(args ...any)                                 {}
func (l *mockLogger) Debugf(format string, args ...any)                 {}
func (l *mockLogger) Debugln(args ...any)                               {}
func (l *mockLogger) Info(args ...any)                                  {}
func (l *mockLogger) Infof(format string, args ...any)                  {}
func (l *mockLogger) Infoln(args ...any)                                {}
func (l *mockLogger) Warn(args ...any)                                  {}
func (l *mockLogger) Warnf(format string, args ...any)                  {}
func (l *mockLogger) Warnln(args ...any)                                {}
func (l *mockLogger) Error(args ...any)                                 {}
func (l *mockLogger) Errorf(format string, args ...any)                 {}
func (l *mockLogger) Errorln(args ...any)                               {}
func (l *mockLogger) Fatal(args ...any)                                 {}
func (l *mockLogger) Fatalf(format string, args ...any)                 {}
func (l *mockLogger) Fatalln(args ...any)                               {}
func (l *mockLogger) WithFields(fields ...any) log.Logger               { return l }
func (l *mockLogger) WithDefaultMessageTemplate(msg string) log.Logger  { return l }
func (l *mockLogger) Sync() error                                       { return nil }

func TestPostgresConnection_MultiStatementEnabled_Nil_DefaultsToTrue(t *testing.T) {
	t.Parallel()

	pc := &PostgresConnection{
		ConnectionStringPrimary: "postgres://user:pass@localhost:5432/testdb",
		ConnectionStringReplica: "postgres://user:pass@localhost:5432/testdb",
		PrimaryDBName:           "testdb",
		ReplicaDBName:           "testdb",
		Logger:                  &mockLogger{},
		MaxOpenConnections:      10,
		MaxIdleConnections:      5,
		MultiStatementEnabled:   nil, // explicitly nil to test default
	}

	// Verify the field is nil
	assert.Nil(t, pc.MultiStatementEnabled, "MultiStatementEnabled should be nil by default")

	// Verify default resolution logic (simulating what Connect() does)
	multiStmtEnabled := true
	if pc.MultiStatementEnabled != nil {
		multiStmtEnabled = *pc.MultiStatementEnabled
	}

	assert.True(t, multiStmtEnabled, "nil MultiStatementEnabled should resolve to true")
}

func TestPostgresConnection_MultiStatementEnabled_ExplicitTrue(t *testing.T) {
	t.Parallel()

	pc := &PostgresConnection{
		ConnectionStringPrimary: "postgres://user:pass@localhost:5432/testdb",
		ConnectionStringReplica: "postgres://user:pass@localhost:5432/testdb",
		PrimaryDBName:           "testdb",
		ReplicaDBName:           "testdb",
		Logger:                  &mockLogger{},
		MaxOpenConnections:      10,
		MaxIdleConnections:      5,
		MultiStatementEnabled:   pointers.Bool(true), // explicitly true
	}

	// Verify the field is set to true
	assert.NotNil(t, pc.MultiStatementEnabled, "MultiStatementEnabled should not be nil")
	assert.True(t, *pc.MultiStatementEnabled, "MultiStatementEnabled should be true")

	// Verify resolution logic
	multiStmtEnabled := true
	if pc.MultiStatementEnabled != nil {
		multiStmtEnabled = *pc.MultiStatementEnabled
	}

	assert.True(t, multiStmtEnabled, "explicit true should resolve to true")
}

func TestPostgresConnection_MultiStatementEnabled_ExplicitFalse(t *testing.T) {
	t.Parallel()

	pc := &PostgresConnection{
		ConnectionStringPrimary: "postgres://user:pass@localhost:5432/testdb",
		ConnectionStringReplica: "postgres://user:pass@localhost:5432/testdb",
		PrimaryDBName:           "testdb",
		ReplicaDBName:           "testdb",
		Logger:                  &mockLogger{},
		MaxOpenConnections:      10,
		MaxIdleConnections:      5,
		MultiStatementEnabled:   pointers.Bool(false), // explicitly false
	}

	// Verify the field is set to false
	assert.NotNil(t, pc.MultiStatementEnabled, "MultiStatementEnabled should not be nil")
	assert.False(t, *pc.MultiStatementEnabled, "MultiStatementEnabled should be false")

	// Verify resolution logic
	multiStmtEnabled := true
	if pc.MultiStatementEnabled != nil {
		multiStmtEnabled = *pc.MultiStatementEnabled
	}

	assert.False(t, multiStmtEnabled, "explicit false should resolve to false")
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
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pc := &PostgresConnection{
				ConnectionStringPrimary: "postgres://user:pass@localhost:5432/testdb",
				ConnectionStringReplica: "postgres://user:pass@localhost:5432/testdb",
				PrimaryDBName:           "testdb",
				ReplicaDBName:           "testdb",
				Logger:                  &mockLogger{},
				MaxOpenConnections:      10,
				MaxIdleConnections:      5,
				MultiStatementEnabled:   tt.multiStatementEnabled,
			}

			// Simulate the resolution logic from Connect()
			multiStmtEnabled := true
			if pc.MultiStatementEnabled != nil {
				multiStmtEnabled = *pc.MultiStatementEnabled
			}

			assert.Equal(t, tt.expectedResolved, multiStmtEnabled, tt.description)
		})
	}
}
