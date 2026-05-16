//go:build unit

package postgres

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
)

// TestLogInfo_NilLogger covers the nil logger guard.
func TestLogInfo_NilLogger(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{Logger: nil}}
	assert.NotPanics(t, func() {
		s.logInfo(context.Background(), "test message")
	})
}

// TestLogWarn_NilLogger covers the nil logger guard.
func TestLogWarn_NilLogger(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{Logger: nil}}
	assert.NotPanics(t, func() {
		s.logWarn(context.Background(), "test message")
	})
}

// TestLogDebug_NilLogger covers the nil logger guard.
func TestLogDebug_NilLogger(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{Logger: nil}}
	assert.NotPanics(t, func() {
		s.logDebug(context.Background(), "test message")
	})
}

// TestLogInfo_WithNopLogger covers the enabled logger path.
func TestLogInfo_WithNopLogger(t *testing.T) {
	t.Parallel()

	logger := log.NewNop()
	s := &Store{cfg: Config{Logger: logger}}
	assert.NotPanics(t, func() {
		s.logInfo(context.Background(), "test message", log.String("k", "v"))
	})
}

// TestLogWarn_WithNopLogger covers the enabled logger path.
func TestLogWarn_WithNopLogger(t *testing.T) {
	t.Parallel()

	logger := log.NewNop()
	s := &Store{cfg: Config{Logger: logger}}
	assert.NotPanics(t, func() {
		s.logWarn(context.Background(), "test message", log.String("k", "v"))
	})
}

// TestLogDebug_WithNopLogger covers the enabled logger path.
func TestLogDebug_WithNopLogger(t *testing.T) {
	t.Parallel()

	logger := log.NewNop()
	s := &Store{cfg: Config{Logger: logger}}
	assert.NotPanics(t, func() {
		s.logDebug(context.Background(), "test message", log.String("k", "v"))
	})
}

// TestQuoteIdentifier covers the quoteIdentifier function.
func TestQuoteIdentifier(t *testing.T) {
	t.Parallel()

	result := quoteIdentifier("my_table")
	assert.Equal(t, `"my_table"`, result)
}

func TestQuoteIdentifier_Empty(t *testing.T) {
	t.Parallel()

	result := quoteIdentifier("")
	assert.Equal(t, `""`, result)
}
