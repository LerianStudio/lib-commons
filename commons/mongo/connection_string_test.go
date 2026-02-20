// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package mongo

import (
	"fmt"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v3/commons/log"
	"github.com/stretchr/testify/assert"
)

func TestBuildConnectionString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		scheme     string
		user       string
		password   string
		host       string
		port       string
		parameters string
		expected   string
	}{
		{
			name:       "basic_connection_no_parameters",
			scheme:     "mongodb",
			user:       "admin",
			password:   "secret123",
			host:       "localhost",
			port:       "27017",
			parameters: "",
			expected:   "mongodb://admin:secret123@localhost:27017/",
		},
		{
			name:       "connection_with_single_parameter",
			scheme:     "mongodb",
			user:       "admin",
			password:   "secret123",
			host:       "localhost",
			port:       "27017",
			parameters: "authSource=admin",
			expected:   "mongodb://admin:secret123@localhost:27017/?authSource=admin",
		},
		{
			name:       "connection_with_multiple_parameters",
			scheme:     "mongodb",
			user:       "admin",
			password:   "secret123",
			host:       "mongo.example.com",
			port:       "5703",
			parameters: "replicaSet=rs0&authSource=admin&directConnection=true",
			expected:   "mongodb://admin:secret123@mongo.example.com:5703/?replicaSet=rs0&authSource=admin&directConnection=true",
		},
		{
			name:       "mongodb_srv_scheme_omits_port",
			scheme:     "mongodb+srv",
			user:       "user",
			password:   "pass",
			host:       "cluster.mongodb.net",
			port:       "27017",
			parameters: "retryWrites=true&w=majority",
			expected:   "mongodb+srv://user:pass@cluster.mongodb.net/?retryWrites=true&w=majority",
		},
		{
			name:       "mongodb_srv_without_parameters",
			scheme:     "mongodb+srv",
			user:       "user",
			password:   "pass",
			host:       "cluster.mongodb.net",
			port:       "",
			parameters: "",
			expected:   "mongodb+srv://user:pass@cluster.mongodb.net/",
		},
		{
			name:       "special_characters_in_password_url_encoded",
			scheme:     "mongodb",
			user:       "admin",
			password:   "p@ss:word/123",
			host:       "localhost",
			port:       "27017",
			parameters: "",
			expected:   "mongodb://admin:p%40ss%3Aword%2F123@localhost:27017/",
		},
		{
			name:       "special_characters_in_username_url_encoded",
			scheme:     "mongodb",
			user:       "user@domain",
			password:   "pass",
			host:       "localhost",
			port:       "27017",
			parameters: "",
			expected:   "mongodb://user%40domain:pass@localhost:27017/",
		},
		{
			name:       "empty_credentials",
			scheme:     "mongodb",
			user:       "",
			password:   "",
			host:       "localhost",
			port:       "27017",
			parameters: "",
			expected:   "mongodb://localhost:27017/",
		},
		{
			name:       "empty_user_with_password",
			scheme:     "mongodb",
			user:       "",
			password:   "secret",
			host:       "localhost",
			port:       "27017",
			parameters: "",
			expected:   "mongodb://localhost:27017/",
		},
		{
			name:       "user_without_password",
			scheme:     "mongodb",
			user:       "admin",
			password:   "",
			host:       "localhost",
			port:       "27017",
			parameters: "",
			expected:   "mongodb://admin:@localhost:27017/",
		},
		{
			name:       "empty_parameters_no_question_mark",
			scheme:     "mongodb",
			user:       "user",
			password:   "pass",
			host:       "db.local",
			port:       "27017",
			parameters: "",
			expected:   "mongodb://user:pass@db.local:27017/",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := BuildConnectionString(tt.scheme, tt.user, tt.password, tt.host, tt.port, tt.parameters, nil)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildConnectionString_LoggerMasksCredentials(t *testing.T) {
	t.Parallel()

	logger := &testLogger{}

	_ = BuildConnectionString("mongodb", "dbuser", "supersecret", "localhost", "27017", "authSource=admin", logger)

	assert.Len(t, logger.debugs, 1, "expected exactly one debug log")
	assert.True(t, strings.Contains(logger.debugs[0], "<credentials>"), "expected credentials to be masked")
	assert.False(t, strings.Contains(logger.debugs[0], "dbuser"), "username should not appear in logs")
	assert.False(t, strings.Contains(logger.debugs[0], "supersecret"), "password should not appear in logs")
}

func TestBuildConnectionString_NilLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()

	assert.NotPanics(t, func() {
		_ = BuildConnectionString("mongodb", "user", "pass", "localhost", "27017", "", nil)
	})
}

type testLogger struct {
	debugs []string
}

func (l *testLogger) Debug(args ...any) {}
func (l *testLogger) Debugf(format string, args ...any) {
	l.debugs = append(l.debugs, fmt.Sprintf(format, args...))
}
func (l *testLogger) Debugln(args ...any)                              {}
func (l *testLogger) Info(args ...any)                                 {}
func (l *testLogger) Infof(format string, args ...any)                 {}
func (l *testLogger) Infoln(args ...any)                               {}
func (l *testLogger) Warn(args ...any)                                 {}
func (l *testLogger) Warnf(format string, args ...any)                 {}
func (l *testLogger) Warnln(args ...any)                               {}
func (l *testLogger) Error(args ...any)                                {}
func (l *testLogger) Errorf(format string, args ...any)                {}
func (l *testLogger) Errorln(args ...any)                              {}
func (l *testLogger) Fatal(args ...any)                                {}
func (l *testLogger) Fatalf(format string, args ...any)                {}
func (l *testLogger) Fatalln(args ...any)                              {}
func (l *testLogger) WithFields(fields ...any) log.Logger              { return l }
func (l *testLogger) WithDefaultMessageTemplate(msg string) log.Logger { return l }
func (l *testLogger) Sync() error                                      { return nil }
