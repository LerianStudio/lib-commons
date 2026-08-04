// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

// Package testutil provides shared test helpers for the tenant-manager
// sub-packages, eliminating duplicated mock implementations across test files.
package testutil

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/LerianStudio/lib-observability/v2/log"
)

// NewMockLogger returns a no-op logger that satisfies log.Logger.
// It delegates to log.NewNop() to avoid duplicating the standard no-op implementation.
func NewMockLogger() log.Logger {
	return log.NewNop()
}

// CapturingLogger implements log.Logger and captures log messages for assertion.
// This enables verifying log output content in tests (e.g., connection_mode=lazy).
// Messages are private to prevent unsafe concurrent access; use GetMessages() or
// ContainsSubstring() for thread-safe reads.
type CapturingLogger struct {
	mu       sync.Mutex
	messages []string
}

func (cl *CapturingLogger) record(msg string) {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	cl.messages = append(cl.messages, msg)
}

// GetMessages returns a thread-safe copy of all captured messages.
func (cl *CapturingLogger) GetMessages() []string {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	copied := make([]string, len(cl.messages))
	copy(copied, cl.messages)

	return copied
}

// Clear resets the captured messages, useful for testing multi-phase log assertions.
func (cl *CapturingLogger) Clear() {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	cl.messages = nil
}

// ContainsSubstring returns true if any captured message contains the given substring.
func (cl *CapturingLogger) ContainsSubstring(sub string) bool {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	for _, msg := range cl.messages {
		if strings.Contains(msg, sub) {
			return true
		}
	}

	return false
}

func (cl *CapturingLogger) Log(_ context.Context, _ log.Level, msg string, fields ...log.Field) {
	if len(fields) == 0 {
		cl.record(msg)

		return
	}

	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, fmt.Sprintf("%s=%v", field.Key, field.Value))
	}

	cl.record(fmt.Sprintf("%s %s", msg, strings.Join(parts, " ")))
}
func (cl *CapturingLogger) With(_ ...log.Field) log.Logger { return cl }
func (cl *CapturingLogger) WithGroup(_ string) log.Logger  { return cl }
func (cl *CapturingLogger) Enabled(_ log.Level) bool       { return true }
func (cl *CapturingLogger) Sync(_ context.Context) error   { return nil }

// NewCapturingLogger returns a new CapturingLogger that records all log messages.
func NewCapturingLogger() *CapturingLogger {
	return &CapturingLogger{}
}

// LogEntry is a single captured log record with its level.
type LogEntry struct {
	Level   log.Level
	Message string
}

// LevelCapturingLogger implements log.Logger and captures both the level and the
// rendered message of every record, so tests can assert that a diagnostic was
// emitted at the intended severity and not swallowed at debug level.
type LevelCapturingLogger struct {
	mu      sync.Mutex
	entries []LogEntry
}

// NewLevelCapturingLogger returns a LevelCapturingLogger recording all records.
func NewLevelCapturingLogger() *LevelCapturingLogger {
	return &LevelCapturingLogger{}
}

// Entries returns a thread-safe copy of all captured entries.
func (l *LevelCapturingLogger) Entries() []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()

	copied := make([]LogEntry, len(l.entries))
	copy(copied, l.entries)

	return copied
}

// ContainsAtLevel reports whether any record logged at the given level contains
// every one of the supplied substrings.
func (l *LevelCapturingLogger) ContainsAtLevel(level log.Level, subs ...string) bool {
	for _, entry := range l.Entries() {
		if entry.Level != level {
			continue
		}

		matched := true

		for _, sub := range subs {
			if !strings.Contains(entry.Message, sub) {
				matched = false

				break
			}
		}

		if matched {
			return true
		}
	}

	return false
}

func (l *LevelCapturingLogger) Log(_ context.Context, level log.Level, msg string, fields ...log.Field) {
	rendered := msg

	if len(fields) > 0 {
		parts := make([]string, 0, len(fields))
		for _, field := range fields {
			parts = append(parts, fmt.Sprintf("%s=%v", field.Key, field.Value))
		}

		rendered = fmt.Sprintf("%s %s", msg, strings.Join(parts, " "))
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = append(l.entries, LogEntry{Level: level, Message: rendered})
}

func (l *LevelCapturingLogger) With(_ ...log.Field) log.Logger { return l }
func (l *LevelCapturingLogger) WithGroup(_ string) log.Logger  { return l }
func (l *LevelCapturingLogger) Enabled(_ log.Level) bool       { return true }
func (l *LevelCapturingLogger) Sync(_ context.Context) error   { return nil }
