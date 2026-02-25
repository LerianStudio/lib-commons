// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

// Package testutil provides shared test helpers for the tenant-manager
// sub-packages, eliminating duplicated mock implementations across test files.
package testutil

import (
	"fmt"
	"strings"
	"sync"

	"github.com/LerianStudio/lib-commons/v3/commons/log"
)

// MockLogger is a no-op implementation of log.Logger for unit tests.
// It discards all log output, allowing tests to focus on business logic.
type MockLogger struct{}

func (m *MockLogger) Info(_ ...any)                                  {}
func (m *MockLogger) Infof(_ string, _ ...any)                       {}
func (m *MockLogger) Infoln(_ ...any)                                {}
func (m *MockLogger) Error(_ ...any)                                 {}
func (m *MockLogger) Errorf(_ string, _ ...any)                      {}
func (m *MockLogger) Errorln(_ ...any)                               {}
func (m *MockLogger) Warn(_ ...any)                                  {}
func (m *MockLogger) Warnf(_ string, _ ...any)                       {}
func (m *MockLogger) Warnln(_ ...any)                                {}
func (m *MockLogger) Debug(_ ...any)                                 {}
func (m *MockLogger) Debugf(_ string, _ ...any)                      {}
func (m *MockLogger) Debugln(_ ...any)                               {}
func (m *MockLogger) Fatal(_ ...any)                                 {}
func (m *MockLogger) Fatalf(_ string, _ ...any)                      {}
func (m *MockLogger) Fatalln(_ ...any)                               {}
func (m *MockLogger) WithFields(_ ...any) log.Logger                 { return m }
func (m *MockLogger) WithDefaultMessageTemplate(_ string) log.Logger { return m }
func (m *MockLogger) Sync() error                                    { return nil }

// NewMockLogger returns a new no-op MockLogger that satisfies log.Logger.
func NewMockLogger() log.Logger {
	return &MockLogger{}
}

// CapturingLogger implements log.Logger and captures log messages for assertion.
// This enables verifying log output content in tests (e.g., connection_mode=lazy).
type CapturingLogger struct {
	mu       sync.Mutex
	Messages []string
}

func (cl *CapturingLogger) record(msg string) {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	cl.Messages = append(cl.Messages, msg)
}

// GetMessages returns a thread-safe copy of all captured messages.
func (cl *CapturingLogger) GetMessages() []string {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	copied := make([]string, len(cl.Messages))
	copy(copied, cl.Messages)

	return copied
}

// ContainsSubstring returns true if any captured message contains the given substring.
func (cl *CapturingLogger) ContainsSubstring(sub string) bool {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	for _, msg := range cl.Messages {
		if strings.Contains(msg, sub) {
			return true
		}
	}

	return false
}

func (cl *CapturingLogger) Info(args ...any)                  { cl.record(fmt.Sprint(args...)) }
func (cl *CapturingLogger) Infof(format string, args ...any)  { cl.record(fmt.Sprintf(format, args...)) }
func (cl *CapturingLogger) Infoln(args ...any)                { cl.record(fmt.Sprintln(args...)) }
func (cl *CapturingLogger) Error(args ...any)                 { cl.record(fmt.Sprint(args...)) }
func (cl *CapturingLogger) Errorf(format string, args ...any) { cl.record(fmt.Sprintf(format, args...)) }
func (cl *CapturingLogger) Errorln(args ...any)               { cl.record(fmt.Sprintln(args...)) }
func (cl *CapturingLogger) Warn(args ...any)                  { cl.record(fmt.Sprint(args...)) }
func (cl *CapturingLogger) Warnf(format string, args ...any)  { cl.record(fmt.Sprintf(format, args...)) }
func (cl *CapturingLogger) Warnln(args ...any)                { cl.record(fmt.Sprintln(args...)) }
func (cl *CapturingLogger) Debug(args ...any)                 { cl.record(fmt.Sprint(args...)) }
func (cl *CapturingLogger) Debugf(format string, args ...any) { cl.record(fmt.Sprintf(format, args...)) }
func (cl *CapturingLogger) Debugln(args ...any)               { cl.record(fmt.Sprintln(args...)) }
func (cl *CapturingLogger) Fatal(args ...any)                 { cl.record(fmt.Sprint(args...)) }
func (cl *CapturingLogger) Fatalf(format string, args ...any) { cl.record(fmt.Sprintf(format, args...)) }
func (cl *CapturingLogger) Fatalln(args ...any)               { cl.record(fmt.Sprintln(args...)) }
func (cl *CapturingLogger) WithFields(_ ...any) log.Logger    { return cl }
func (cl *CapturingLogger) WithDefaultMessageTemplate(_ string) log.Logger {
	return cl
}
func (cl *CapturingLogger) Sync() error { return nil }

// NewCapturingLogger returns a new CapturingLogger that records all log messages.
func NewCapturingLogger() *CapturingLogger {
	return &CapturingLogger{}
}
