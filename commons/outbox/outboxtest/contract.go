// Package outboxtest provides backend-agnostic contract tests for
// commons/outbox repositories. Both Postgres and Mongo backends should run
// this shared suite to guarantee equivalent observable behavior.
package outboxtest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/LerianStudio/lib-commons/v7/commons/errgroup"
	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

// Factory constructs a fresh isolated repository for a single subtest.
type Factory func(t *testing.T) outbox.OutboxRepository

// TxFactory starts a transaction for repositories that support SQL-backed
// CreateWithTx semantics. The returned cleanup function is always called.
type TxFactory func(t *testing.T, ctx context.Context) (outbox.Tx, func())

// RunOption configures [Run].
type RunOption func(*runConfig)

type runConfig struct {
	skip      map[string]struct{}
	txFactory TxFactory
}

// SkipSubtest skips one named contract subtest.
func SkipSubtest(name string) RunOption {
	return func(cfg *runConfig) {
		if cfg.skip == nil {
			cfg.skip = make(map[string]struct{})
		}

		cfg.skip[name] = struct{}{}
	}
}

// WithTransactionFactory enables contract coverage for repositories that support
// CreateWithTx with a real SQL transaction.
func WithTransactionFactory(factory TxFactory) RunOption {
	return func(cfg *runConfig) {
		cfg.txFactory = factory
	}
}

func (cfg *runConfig) shouldSkip(name string) bool {
	if cfg == nil || cfg.skip == nil {
		return false
	}

	_, ok := cfg.skip[name]

	return ok
}

// Run executes the shared repository contract suite.
func Run(t *testing.T, factory Factory, opts ...RunOption) {
	t.Helper()

	cfg := &runConfig{}

	for _, opt := range opts {
		if opt == nil {
			continue
		}

		opt(cfg)
	}

	run := func(name string, fn func(*testing.T)) {
		t.Run(name, func(t *testing.T) {
			if cfg.shouldSkip(name) {
				t.Skipf("skipped by SkipSubtest(%q)", name)
			}

			fn(t)
		})
	}

	run("CreateThenGetRoundtrip", func(t *testing.T) { testCreateThenGetRoundtrip(t, factory) })
	run("CreateForcesPendingLifecycleInvariants", func(t *testing.T) { testCreateForcesPendingLifecycleInvariants(t, factory) })
	run("CreateWithTx", func(t *testing.T) { testCreateWithTx(t, factory, cfg.txFactory) })
	run("EmptyRepositoryReturnsNoRows", func(t *testing.T) { testEmptyRepositoryReturnsNoRows(t, factory) })
	run("ListPendingClaimsProcessing", func(t *testing.T) { testListPendingClaimsProcessing(t, factory) })
	run("ConcurrentListPendingClaimsUnique", func(t *testing.T) { testConcurrentListPendingClaimsUnique(t, factory) })
	run("ListPendingByTypeFilters", func(t *testing.T) { testListPendingByTypeFilters(t, factory) })
	run("MarkPublishedAfterClaim", func(t *testing.T) { testMarkPublishedAfterClaim(t, factory) })
	run("StateTransitionsRequireProcessing", func(t *testing.T) { testStateTransitionsRequireProcessing(t, factory) })
	run("MarkFailedRedactsSensitiveData", func(t *testing.T) { testMarkFailedRedactsSensitiveData(t, factory) })
	run("MarkFailedAtMaxAttemptsInvalidates", func(t *testing.T) { testMarkFailedAtMaxAttemptsInvalidates(t, factory) })
	run("MarkFailedAccumulatesDistinctCauses", func(t *testing.T) { testMarkFailedAccumulatesDistinctCauses(t, factory) })
	run("MarkFailedBoundsAccumulatedCauses", func(t *testing.T) { testMarkFailedBoundsAccumulatedCauses(t, factory) })
	run("MarkFailedKeepsCauseContainedInAnother", func(t *testing.T) { testMarkFailedKeepsCauseContainedInAnother(t, factory) })
	run("ListFailedForRetryReadOnly", func(t *testing.T) { testListFailedForRetryReadOnly(t, factory) })
	run("RetryScansSkipRowsAtMaxAttempts", func(t *testing.T) { testRetryScansSkipRowsAtMaxAttempts(t, factory) })
	run("ResetForRetryMovesFailedToProcessing", func(t *testing.T) { testResetForRetryMovesFailedToProcessing(t, factory) })
	run("ResetStuckProcessingReprocessesAndInvalidates", func(t *testing.T) { testResetStuckProcessingReprocessesAndInvalidates(t, factory) })
	run("TenantIsolationAndDiscovery", func(t *testing.T) { testTenantIsolationAndDiscovery(t, factory) })
	run("WrongTenantMutationsRejected", func(t *testing.T) { testWrongTenantMutationsRejected(t, factory) })
	run("DispatcherLifecyclePersistsPublishedState", func(t *testing.T) { testDispatcherLifecyclePersistsPublishedState(t, factory) })
}

func testCreateThenGetRoundtrip(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	event := newEvent(t, ctx, "payment.created")

	created, err := repo.Create(ctx, event)
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, outbox.OutboxStatusPending, created.Status)

	stored, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, created.ID, stored.ID)
	require.Equal(t, created.EventType, stored.EventType)
	require.Equal(t, created.AggregateID, stored.AggregateID)
	require.Equal(t, string(created.Payload), string(stored.Payload))
	require.Equal(t, outbox.OutboxStatusPending, stored.Status)
}

func testCreateForcesPendingLifecycleInvariants(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	now := time.Now().UTC()
	publishedAt := now.Add(-time.Minute)

	created, err := repo.Create(ctx, &outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   "payment.invariant.override",
		AggregateID: uuid.New(),
		Payload:     []byte(`{"ok":true}`),
		Status:      outbox.OutboxStatusPublished,
		Attempts:    9,
		PublishedAt: &publishedAt,
		LastError:   "must not persist",
		CreatedAt:   now,
		UpdatedAt:   now.Add(-time.Hour),
	})
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, outbox.OutboxStatusPending, created.Status)
	require.Equal(t, 0, created.Attempts)
	require.Nil(t, created.PublishedAt)
	require.Empty(t, created.LastError)
	require.False(t, created.UpdatedAt.Before(created.CreatedAt))
}

func testCreateWithTx(t *testing.T, factory Factory, txFactory TxFactory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")

	created, err := repo.CreateWithTx(ctx, nil, newEvent(t, ctx, "payment.tx.nil"))
	require.NoError(t, err)
	require.NotNil(t, created)

	stored, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, created.ID, stored.ID)

	if txFactory == nil {
		return
	}

	tx, cleanup := txFactory(t, ctx)
	if cleanup != nil {
		defer cleanup()
	}

	txCreated, err := repo.CreateWithTx(ctx, tx, newEvent(t, ctx, "payment.tx.real"))
	require.NoError(t, err)
	require.NotNil(t, txCreated)
	require.NoError(t, tx.Commit())

	txStored, err := repo.GetByID(ctx, txCreated.ID)
	require.NoError(t, err)
	require.NotNil(t, txStored)
	require.Equal(t, txCreated.ID, txStored.ID)

	rollbackTx, rollbackCleanup := txFactory(t, ctx)
	if rollbackCleanup != nil {
		defer rollbackCleanup()
	}

	rolledBack, err := repo.CreateWithTx(ctx, rollbackTx, newEvent(t, ctx, "payment.tx.rollback"))
	require.NoError(t, err)
	require.NotNil(t, rolledBack)
	require.NoError(t, rollbackTx.Rollback())

	_, err = repo.GetByID(ctx, rolledBack.ID)
	require.Error(t, err)
}

func testEmptyRepositoryReturnsNoRows(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	now := time.Now().UTC()

	pending, err := repo.ListPending(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, pending)

	failed, err := repo.ListFailedForRetry(ctx, 10, now, 3)
	require.NoError(t, err)
	require.Empty(t, failed)

	retried, err := repo.ResetForRetry(ctx, 10, now, 3)
	require.NoError(t, err)
	require.Empty(t, retried)

	stuck, err := repo.ResetStuckProcessing(ctx, 10, now, 3)
	require.NoError(t, err)
	require.Empty(t, stuck)
}

func testListPendingClaimsProcessing(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	created := createEvent(t, repo, ctx, "payment.pending")

	pending, err := repo.ListPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, created.ID, pending[0].ID)
	require.Equal(t, outbox.OutboxStatusProcessing, pending[0].Status)

	require.Empty(t, mustListPending(t, repo, ctx, 10))
}

func testConcurrentListPendingClaimsUnique(t *testing.T, factory Factory) {
	t.Helper()

	const (
		totalEvents = 12
		workers     = 4
		batchSize   = 3
	)

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	expected := make(map[uuid.UUID]struct{}, totalEvents)

	for i := range totalEvents {
		created := createEvent(t, repo, ctx, fmt.Sprintf("payment.concurrent.%02d", i))
		expected[created.ID] = struct{}{}
	}

	start := make(chan struct{})
	results := make(chan []*outbox.OutboxEvent, workers)

	group, groupCtx := errgroup.WithContext(ctx)

	for range workers {
		group.Go(func() error {
			select {
			case <-start:
			case <-groupCtx.Done():
				return groupCtx.Err()
			}

			events, err := repo.ListPending(groupCtx, batchSize)
			if err != nil {
				return err
			}

			results <- events

			return nil
		})
	}

	close(start)
	require.NoError(t, group.Wait())
	close(results)

	seen := make(map[uuid.UUID]struct{}, totalEvents)

	for events := range results {
		for _, event := range events {
			require.Equal(t, outbox.OutboxStatusProcessing, event.Status)
			require.Contains(t, expected, event.ID)
			require.NotContains(t, seen, event.ID)

			seen[event.ID] = struct{}{}
		}
	}

	require.Len(t, seen, totalEvents)
	require.Empty(t, mustListPending(t, repo, ctx, totalEvents))
}

func testListPendingByTypeFilters(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	target := createEvent(t, repo, ctx, "payment.priority")
	_ = createEvent(t, repo, ctx, "payment.other")

	events, err := repo.ListPendingByType(ctx, "payment.priority", 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, target.ID, events[0].ID)
	require.Equal(t, outbox.OutboxStatusProcessing, events[0].Status)
}

func testMarkPublishedAfterClaim(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	created := createEvent(t, repo, ctx, "payment.publish")
	claimSinglePending(t, repo, ctx, created.ID)

	now := time.Now().UTC()
	require.NoError(t, repo.MarkPublished(ctx, created.ID, now))

	stored, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, outbox.OutboxStatusPublished, stored.Status)
	require.NotNil(t, stored.PublishedAt)
}

func testStateTransitionsRequireProcessing(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")

	publishedEvent := createEvent(t, repo, ctx, "payment.published.guard")
	err := repo.MarkPublished(ctx, publishedEvent.ID, time.Now().UTC())
	require.Error(t, err)

	failedEvent := createEvent(t, repo, ctx, "payment.failed.guard")
	err = repo.MarkFailed(ctx, failedEvent.ID, "retry error", 3)
	require.Error(t, err)

	invalidEvent := createEvent(t, repo, ctx, "payment.invalid.guard")
	err = repo.MarkInvalid(ctx, invalidEvent.ID, "non-retryable error")
	require.Error(t, err)
}

func testMarkFailedRedactsSensitiveData(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	created := createEvent(t, repo, ctx, "payment.failed")
	claimSinglePending(t, repo, ctx, created.ID)

	require.NoError(t, repo.MarkFailed(ctx, created.ID, "password=super-secret", 5))

	stored, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, outbox.OutboxStatusFailed, stored.Status)
	require.NotContains(t, stored.LastError, "super-secret")
	require.Equal(t, 1, stored.Attempts)
}

func testMarkFailedAtMaxAttemptsInvalidates(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	created := createEvent(t, repo, ctx, "payment.failed.exhausted")
	claimSinglePending(t, repo, ctx, created.ID)

	require.NoError(t, repo.MarkFailed(ctx, created.ID, "terminal failure", 1))

	stored, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, outbox.OutboxStatusInvalid, stored.Status)
	require.Equal(t, 1, stored.Attempts)
	// Exhausting the budget must NOT replace the cause with a restatement of
	// the exhaustion: status and attempts above already carry that. The column
	// answers why, and a quarantined row that cannot say why is the defect.
	require.Equal(t, "terminal failure", stored.LastError)
	require.NotContains(t, stored.LastError, "max dispatch attempts exceeded")
}

func testListFailedForRetryReadOnly(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	created := createEvent(t, repo, ctx, "payment.retry.readonly")
	claimSinglePending(t, repo, ctx, created.ID)
	require.NoError(t, repo.MarkFailed(ctx, created.ID, "transient error", 5))

	failed, err := repo.ListFailedForRetry(ctx, 10, time.Now().UTC(), 5)
	require.NoError(t, err)
	require.Len(t, failed, 1)
	require.Equal(t, created.ID, failed[0].ID)
	require.Equal(t, outbox.OutboxStatusFailed, failed[0].Status)

	stored, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, outbox.OutboxStatusFailed, stored.Status)
	require.Equal(t, 1, stored.Attempts)
}

func testRetryScansSkipRowsAtMaxAttempts(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	created := createEvent(t, repo, ctx, "payment.retry.exhausted")
	claimSinglePending(t, repo, ctx, created.ID)
	require.NoError(t, repo.MarkFailed(ctx, created.ID, "first failure", 2))

	failed, err := repo.ListFailedForRetry(ctx, 10, time.Now().UTC(), 1)
	require.NoError(t, err)
	require.Empty(t, failed)

	retried, err := repo.ResetForRetry(ctx, 10, time.Now().UTC(), 1)
	require.NoError(t, err)
	require.Empty(t, retried)

	stored, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, outbox.OutboxStatusFailed, stored.Status)
	require.Equal(t, 1, stored.Attempts)
}

func testResetForRetryMovesFailedToProcessing(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	created := createEvent(t, repo, ctx, "payment.retry")
	claimSinglePending(t, repo, ctx, created.ID)
	require.NoError(t, repo.MarkFailed(ctx, created.ID, "transient error", 5))

	retried, err := repo.ResetForRetry(ctx, 10, time.Now().UTC(), 5)
	require.NoError(t, err)
	require.Len(t, retried, 1)
	require.Equal(t, created.ID, retried[0].ID)
	require.Equal(t, outbox.OutboxStatusProcessing, retried[0].Status)

	stored, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, outbox.OutboxStatusProcessing, stored.Status)
	require.Equal(t, 1, stored.Attempts)
}

func testResetStuckProcessingReprocessesAndInvalidates(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	retryEvent := createEvent(t, repo, ctx, "payment.stuck.retry")
	exhaustedEvent := createEvent(t, repo, ctx, "payment.stuck.exhausted")

	claimAllPending(t, repo, ctx, 10)

	// retryEvent -> PROCESSING attempts=1.
	require.NoError(t, repo.MarkFailed(ctx, retryEvent.ID, "first failure", 10))
	_, err := repo.ResetForRetry(ctx, 10, time.Now().UTC(), 10)
	require.NoError(t, err)

	// exhaustedEvent -> PROCESSING attempts=2.
	require.NoError(t, repo.MarkFailed(ctx, exhaustedEvent.ID, "first failure", 10))
	_, err = repo.ResetForRetry(ctx, 10, time.Now().UTC(), 10)
	require.NoError(t, err)
	require.NoError(t, repo.MarkFailed(ctx, exhaustedEvent.ID, "second failure", 10))
	_, err = repo.ResetForRetry(ctx, 10, time.Now().UTC(), 10)
	require.NoError(t, err)

	reset, err := repo.ResetStuckProcessing(ctx, 10, time.Now().UTC(), 3)
	require.NoError(t, err)
	require.Len(t, reset, 1)
	require.Equal(t, retryEvent.ID, reset[0].ID)
	require.Equal(t, outbox.OutboxStatusProcessing, reset[0].Status)
	require.Equal(t, 2, reset[0].Attempts)

	retriedStored, err := repo.GetByID(ctx, retryEvent.ID)
	require.NoError(t, err)
	require.NotNil(t, retriedStored)
	require.Equal(t, outbox.OutboxStatusProcessing, retriedStored.Status)
	require.Equal(t, 2, retriedStored.Attempts)

	exhaustedStored, err := repo.GetByID(ctx, exhaustedEvent.ID)
	require.NoError(t, err)
	require.NotNil(t, exhaustedStored)
	require.Equal(t, outbox.OutboxStatusInvalid, exhaustedStored.Status)
	require.Equal(t, 3, exhaustedStored.Attempts)
	// The stuck reclaim has no error of its own, so it names the condition —
	// and it accumulates rather than replacing whatever diagnosis the row
	// already earned from its earlier attempts.
	require.Contains(t, exhaustedStored.LastError, outbox.StuckInProcessingCause)
	require.NotContains(t, exhaustedStored.LastError, "max dispatch attempts exceeded")
}

// testMarkFailedAccumulatesDistinctCauses is the guard against the two
// expressions drifting. The Postgres backend mirrors outbox.AppendErrorCause in
// SQL (accumulating in Go there would need a read-modify-write and lose the
// atomicity of one UPDATE), while Mongo calls the function. This asserts the
// resulting BEHAVIOUR against every backend, so a divergence fails the contract
// instead of surviving as two rules that disagree.
func testMarkFailedAccumulatesDistinctCauses(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	created := createEvent(t, repo, ctx, "payment.failed.accumulating")

	// First attempt: the cause that actually diagnoses the row.
	claimSinglePending(t, repo, ctx, created.ID)
	require.NoError(t, repo.MarkFailed(ctx, created.ID, "handler not registered", 4))

	// A repeat of the same cause must not be stored twice, or ten identical
	// timeouts would evict the first cause from a bounded column.
	resetSingleFailed(t, repo, ctx, created.ID)
	require.NoError(t, repo.MarkFailed(ctx, created.ID, "handler not registered", 4))

	// A later, DIFFERENT cause is what shows the degradation.
	resetSingleFailed(t, repo, ctx, created.ID)
	require.NoError(t, repo.MarkFailed(ctx, created.ID, "context deadline exceeded", 4))

	// Final attempt: the budget runs out here.
	resetSingleFailed(t, repo, ctx, created.ID)
	require.NoError(t, repo.MarkFailed(ctx, created.ID, "broker unreachable", 4))

	stored, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, outbox.OutboxStatusInvalid, stored.Status)
	require.Equal(t, 4, stored.Attempts)

	require.Contains(t, stored.LastError, "handler not registered",
		"the FIRST cause is the diagnosis and must survive to the terminal row")
	require.Contains(t, stored.LastError, "context deadline exceeded")
	require.Contains(t, stored.LastError, "broker unreachable")
	require.NotContains(t, stored.LastError, "max dispatch attempts exceeded")
	require.Equal(t, 1, strings.Count(stored.LastError, "handler not registered"),
		"a repeated cause is recorded once, not once per attempt")
	require.LessOrEqual(t, utf8.RuneCountInString(stored.LastError), outbox.MaxLastErrorLength)
}

// testMarkFailedBoundsAccumulatedCauses drives the value PAST its ceiling.
//
// Without this, a backend could omit the cap entirely and still pass: the
// short causes above never approach the limit. That matters more than a
// missing assertion usually would, because the column this library ships is
// `last_error VARCHAR(512)` and Postgres REJECTS an oversized value (SQLSTATE
// 22001) instead of truncating it — an uncapped backend would not store a long
// value, it would fail MarkFailed and strand the row in PROCESSING, retrying
// for ever.
func testMarkFailedBoundsAccumulatedCauses(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	created := createEvent(t, repo, ctx, "payment.failed.saturating")

	first := "first cause: " + strings.Repeat("a", 200)

	claimSinglePending(t, repo, ctx, created.ID)
	require.NoError(t, repo.MarkFailed(ctx, created.ID, first, 8))

	for i := range 6 {
		resetSingleFailed(t, repo, ctx, created.ID)

		cause := "later cause " + string(rune('A'+i)) + ": " + strings.Repeat("b", 200)

		require.NoError(t, repo.MarkFailed(ctx, created.ID, cause, 8))
	}

	stored, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)

	require.LessOrEqual(t, utf8.RuneCountInString(stored.LastError), outbox.MaxLastErrorLength,
		"the stored value must fit the column the library ships, or the write fails outright")
	require.Contains(t, stored.LastError, "first cause:",
		"saturating drops the NEWEST causes; the first is the diagnosis and stays")
	require.Equal(t, 1, strings.Count(stored.LastError, outbox.LastErrorTruncationMarker),
		"the marker is appended once, not once per further attempt")
	require.NotContains(t, stored.LastError, "later cause F:",
		"a cause that did not fit must be dropped, not silently squeezed in")
}

// testMarkFailedKeepsCauseContainedInAnother pins whole-cause duplicate
// detection. A substring test would drop the second cause here, because it is
// contained in the first — and losing a distinct diagnosis is the defect this
// suite exists to catch.
func testMarkFailedKeepsCauseContainedInAnother(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	created := createEvent(t, repo, ctx, "payment.failed.substring")

	claimSinglePending(t, repo, ctx, created.ID)
	require.NoError(t, repo.MarkFailed(ctx, created.ID, "request timeout while calling broker", 4))

	resetSingleFailed(t, repo, ctx, created.ID)
	require.NoError(t, repo.MarkFailed(ctx, created.ID, "timeout", 4))

	stored, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)

	require.Contains(t, stored.LastError, "request timeout while calling broker")
	require.Equal(t, []string{"request timeout while calling broker", "timeout"},
		strings.Split(stored.LastError, "\n"),
		"a distinct cause contained in an earlier one is still its own cause")
}

func testWrongTenantMutationsRejected(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	baseCtx := contractContext(t)
	tenantA := outbox.ContextWithTenantID(baseCtx, "tenant-a")
	tenantB := outbox.ContextWithTenantID(baseCtx, "tenant-b")

	eventB := createEvent(t, repo, tenantB, "payment.wrong-tenant")
	claimSinglePending(t, repo, tenantB, eventB.ID)

	require.Error(t, repo.MarkPublished(tenantA, eventB.ID, time.Now().UTC()))
	require.Error(t, repo.MarkFailed(tenantA, eventB.ID, "wrong tenant", 5))
	require.Error(t, repo.MarkInvalid(tenantA, eventB.ID, "wrong tenant"))

	storedB, err := repo.GetByID(tenantB, eventB.ID)
	require.NoError(t, err)
	require.NotNil(t, storedB)
	require.Equal(t, outbox.OutboxStatusProcessing, storedB.Status)
}

func testTenantIsolationAndDiscovery(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	baseCtx := contractContext(t)
	tenantA := outbox.ContextWithTenantID(baseCtx, "tenant-a")
	tenantB := outbox.ContextWithTenantID(baseCtx, "tenant-b")

	eventA := createEvent(t, repo, tenantA, "payment.a")
	eventB := createEvent(t, repo, tenantB, "payment.b")

	pendingA := mustListPending(t, repo, tenantA, 10)
	require.Len(t, pendingA, 1)
	require.Equal(t, eventA.ID, pendingA[0].ID)

	pendingB := mustListPending(t, repo, tenantB, 10)
	require.Len(t, pendingB, 1)
	require.Equal(t, eventB.ID, pendingB[0].ID)

	_, err := repo.GetByID(tenantA, eventB.ID)
	require.Error(t, err)

	tenants, err := repo.ListTenants(contractContext(t))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"tenant-a", "tenant-b"}, tenants)
}

func testDispatcherLifecyclePersistsPublishedState(t *testing.T, factory Factory) {
	t.Helper()

	repo := factory(t)
	ctx := outbox.ContextWithTenantID(contractContext(t), "tenant-a")
	created := createEvent(t, repo, ctx, "payment.dispatch")

	handlers := outbox.NewHandlerRegistry()
	require.NoError(t, handlers.Register("payment.dispatch", func(_ context.Context, event *outbox.OutboxEvent) error {
		require.Equal(t, created.ID, event.ID)
		return nil
	}))

	dispatcher, err := outbox.NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		outbox.WithBatchSize(10),
		outbox.WithPublishMaxAttempts(1),
	)
	require.NoError(t, err)

	result := dispatcher.DispatchOnceResult(ctx)
	require.Equal(t, 1, result.Processed)
	require.Equal(t, 1, result.Published)
	require.Equal(t, 0, result.Failed)
	require.Equal(t, 0, result.StateUpdateFailed)

	stored, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, outbox.OutboxStatusPublished, stored.Status)
	require.NotNil(t, stored.PublishedAt)
}

func newEvent(t *testing.T, ctx context.Context, eventType string) *outbox.OutboxEvent {
	t.Helper()

	event, err := outbox.NewOutboxEvent(ctx, eventType, uuid.New(), []byte(`{"ok":true}`))
	require.NoError(t, err)

	return event
}

func contractContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	return ctx
}

func createEvent(t *testing.T, repo outbox.OutboxRepository, ctx context.Context, eventType string) *outbox.OutboxEvent {
	t.Helper()

	created, err := repo.Create(ctx, newEvent(t, ctx, eventType))
	require.NoError(t, err)

	return created
}

func mustListPending(t *testing.T, repo outbox.OutboxRepository, ctx context.Context, limit int) []*outbox.OutboxEvent {
	t.Helper()

	events, err := repo.ListPending(ctx, limit)
	require.NoError(t, err)

	return events
}

func claimSinglePending(t *testing.T, repo outbox.OutboxRepository, ctx context.Context, id uuid.UUID) {
	t.Helper()

	events := mustListPending(t, repo, ctx, 10)
	require.Len(t, events, 1)
	require.Equal(t, id, events[0].ID)
	require.Equal(t, outbox.OutboxStatusProcessing, events[0].Status)
}

// resetSingleFailed moves one FAILED row back to PROCESSING so the next
// MarkFailed is legal. The generous maxAttempts keeps the reset itself from
// skipping the row; what the caller is exercising is MarkFailed's own budget.
func resetSingleFailed(t *testing.T, repo outbox.OutboxRepository, ctx context.Context, id uuid.UUID) {
	t.Helper()

	retried, err := repo.ResetForRetry(ctx, 10, time.Now().UTC(), 1000)
	require.NoError(t, err)
	require.Len(t, retried, 1)
	require.Equal(t, id, retried[0].ID)
	require.Equal(t, outbox.OutboxStatusProcessing, retried[0].Status)
}

func claimAllPending(t *testing.T, repo outbox.OutboxRepository, ctx context.Context, limit int) []*outbox.OutboxEvent {
	t.Helper()

	events := mustListPending(t, repo, ctx, limit)
	for _, event := range events {
		require.Equal(t, outbox.OutboxStatusProcessing, event.Status)
	}

	return events
}
