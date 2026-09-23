package postgres

import (
	"encoding/json"
	"fmt"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
)

// DefaultTraceContextColumn is the conventional name of the optional jsonb
// column holding an event's W3C trace carrier. See the migrations directory for
// the ALTER TABLE that adds it.
const DefaultTraceContextColumn = "trace_context"

// EncodeTraceContext renders an event's trace carrier as a driver value for the
// trace context jsonb column, returning a nil value (SQL NULL) when there is
// nothing to persist. The carrier is reduced to traceparent and tracestate
// first, so the column can never hold anything else.
//
// It is exported for services that write their outbox row with their own INSERT
// rather than through this repository: they bind EncodeTraceContext(event.TraceContext)
// to the column and still read the rows back through this repository.
func EncodeTraceContext(carrier map[string]string) (any, error) {
	sanitized := outbox.SanitizeTraceContext(carrier)
	if sanitized == nil {
		return nil, nil //nolint:nilnil // a nil driver value is the SQL NULL this column stores for an event without a carrier.
	}

	encoded, err := json.Marshal(sanitized)
	if err != nil {
		return nil, fmt.Errorf("encoding outbox trace context: %w", err)
	}

	return encoded, nil
}

// decodeTraceContext parses the jsonb column back into a trace carrier. A NULL,
// empty or unreadable column yields a nil carrier.
//
// A row whose carrier cannot be parsed is read as a row without one: the trace
// context is optional telemetry, and failing the read would stop the dispatcher
// from delivering an otherwise valid event. The event then publishes on the
// dispatch cycle trace, exactly as one written before the column existed.
func decodeTraceContext(raw []byte) map[string]string {
	if len(raw) == 0 {
		return nil
	}

	var carrier map[string]string
	if err := json.Unmarshal(raw, &carrier); err != nil {
		return nil
	}

	return outbox.SanitizeTraceContext(carrier)
}

// selectColumns returns the column list to read an outbox event with, extended
// with the trace context column only when the repository was configured for it.
// A table without that column keeps producing the legacy list.
func (repo *Repository) selectColumns() string {
	if repo == nil || repo.traceContextColumn == "" {
		return outboxColumns
	}

	return outboxColumns + ", " + quoteIdentifier(repo.traceContextColumn)
}

// tracesContext reports whether this repository reads and writes the optional
// trace context column.
func (repo *Repository) tracesContext() bool {
	return repo != nil && repo.traceContextColumn != ""
}
