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

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
)

// NewMockLogger returns a no-op logger that satisfies obs.Logger.
// It delegates to obs.Nop() to avoid duplicating the standard no-op implementation.
func NewMockLogger() obs.Logger {
	return obs.Nop()
}

// CapturingLogger implements obs.Logger and captures log messages for assertion.
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

func (cl *CapturingLogger) Log(_ context.Context, _ int, msg string, fields ...any) {
	if len(fields) == 0 {
		cl.record(msg)

		return
	}

	cl.record(fmt.Sprintf("%s %s", msg, renderKV(fields)))
}
func (cl *CapturingLogger) Enabled(_ int) bool           { return true }
func (cl *CapturingLogger) Sync(_ context.Context) error { return nil }

// NewCapturingLogger returns a new CapturingLogger that records all log messages.
func NewCapturingLogger() *CapturingLogger {
	return &CapturingLogger{}
}

// LogEntry is a single captured log record with its level.
type LogEntry struct {
	Level   int
	Message string
}

// LevelCapturingLogger implements obs.Logger and captures both the level and the
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
func (l *LevelCapturingLogger) ContainsAtLevel(level int, subs ...string) bool {
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

func (l *LevelCapturingLogger) Log(_ context.Context, level int, msg string, fields ...any) {
	rendered := msg

	if len(fields) > 0 {
		rendered = fmt.Sprintf("%s %s", msg, renderKV(fields))
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = append(l.entries, LogEntry{Level: level, Message: rendered})
}

func (l *LevelCapturingLogger) Enabled(_ int) bool           { return true }
func (l *LevelCapturingLogger) Sync(_ context.Context) error { return nil }

// renderKV renders alternating key/value pairs as "key=value" separated by
// spaces, so assertions can match on a stable textual form.
func renderKV(kv []any) string {
	normalized := obs.NormalizeKV(kv...)

	parts := make([]string, 0, len(normalized)/2)
	for i := 0; i < len(normalized); i += 2 {
		parts = append(parts, fmt.Sprintf("%v=%v", normalized[i], normalized[i+1]))
	}

	return strings.Join(parts, " ")
}
