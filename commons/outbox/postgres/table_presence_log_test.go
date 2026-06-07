//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	libLog "github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/require"
)

// recordingLogger captures Log calls for assertions. It satisfies
// libLog.Logger and is safe for the single-goroutine probe path here.
type recordingLogger struct {
	mu      sync.Mutex
	entries []recordedEntry
}

type recordedEntry struct {
	level  libLog.Level
	msg    string
	fields []libLog.Field
}

func (l *recordingLogger) Log(_ context.Context, level libLog.Level, msg string, fields ...libLog.Field) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = append(l.entries, recordedEntry{level: level, msg: msg, fields: fields})
}

func (l *recordingLogger) With(...libLog.Field) libLog.Logger { return l }
func (l *recordingLogger) WithGroup(string) libLog.Logger     { return l }
func (l *recordingLogger) Enabled(libLog.Level) bool          { return true }
func (l *recordingLogger) Sync(context.Context) error         { return nil }

func (l *recordingLogger) warnCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	count := 0

	for _, e := range l.entries {
		if e.level == libLog.LevelWarn {
			count++
		}
	}

	return count
}

func (l *recordingLogger) lastWarn() (recordedEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for i := len(l.entries) - 1; i >= 0; i-- {
		if l.entries[i].level == libLog.LevelWarn {
			return l.entries[i], true
		}
	}

	return recordedEntry{}, false
}

// TestTablePresenceProbe_LogsWarnOncePerTTL proves the missing-table skip is
// observable (contract F: skip-and-log, not silent), and that the warn is
// rate-limited by the presence cache: a fresh probe miss warns once; a cached
// miss within the TTL emits no additional warn.
func TestTablePresenceProbe_LogsWarnOncePerTTL(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	defer func() { _ = db.Close() }()

	// to_regclass returns NULL when the table is absent.
	mock.ExpectQuery("SELECT to_regclass").
		WithArgs("outbox_events").
		WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow(nil))

	logger := &recordingLogger{}

	repo := &Repository{
		logger:    logger,
		tableName: "outbox_events",
	}

	probe := repo.newTablePresenceProbe(func(context.Context) (*sql.DB, error) {
		return db, nil
	})

	guard := newTablePresenceGuard(probe, time.Hour)

	// Fresh miss: probe runs, table absent, exactly one warn emitted.
	present, err := guard.present(context.Background(), "22222222-2222-2222-2222-222222222222")
	require.NoError(t, err)
	require.False(t, present)
	require.Equal(t, 1, logger.warnCount())

	entry, ok := logger.lastWarn()
	require.True(t, ok)
	require.Contains(t, entry.msg, "outbox table absent")
	requireFieldValue(t, entry.fields, "tenant.id", "22222222-2222-2222-2222-222222222222")
	requireFieldValue(t, entry.fields, "outbox.table", "outbox_events")

	// Cached miss within TTL: probe must NOT run again (no further SQL expected),
	// and no additional warn is emitted — confirming the rate limit.
	present, err = guard.present(context.Background(), "22222222-2222-2222-2222-222222222222")
	require.NoError(t, err)
	require.False(t, present)
	require.Equal(t, 1, logger.warnCount(), "cached miss must not re-log")

	require.NoError(t, mock.ExpectationsWereMet())
}

// TestTablePresenceProbe_NoWarnWhenTablePresent confirms the warn fires only on
// absence, not on a healthy tenant.
func TestTablePresenceProbe_NoWarnWhenTablePresent(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	defer func() { _ = db.Close() }()

	mock.ExpectQuery("SELECT to_regclass").
		WithArgs("outbox_events").
		WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow("outbox_events"))

	logger := &recordingLogger{}

	repo := &Repository{logger: logger, tableName: "outbox_events"}

	probe := repo.newTablePresenceProbe(func(context.Context) (*sql.DB, error) {
		return db, nil
	})

	present, err := probe(context.Background(), "33333333-3333-3333-3333-333333333333")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, 0, logger.warnCount())

	require.NoError(t, mock.ExpectationsWereMet())
}

func requireFieldValue(t *testing.T, fields []libLog.Field, key, want string) {
	t.Helper()

	for _, f := range fields {
		if f.Key == key {
			require.Equal(t, want, f.Value)
			return
		}
	}

	t.Fatalf("field %q not found in log entry", key)
}
