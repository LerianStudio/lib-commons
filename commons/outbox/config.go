package outbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/internal/nilcheck"
	"go.opentelemetry.io/otel/metric"
)

const (
	defaultDispatchInterval             = 2 * time.Second
	defaultColdDispatchInterval         = time.Minute
	defaultBatchSize                    = 50
	defaultPublishMaxAttempts           = 3
	defaultPublishBackoff               = 200 * time.Millisecond
	defaultListPendingFailureThreshold  = 3
	defaultRetryWindow                  = 5 * time.Minute
	defaultMaxDispatchAttempts          = 10
	defaultProcessingTimeout            = 10 * time.Minute
	defaultPriorityBudget               = 10
	defaultMaxFailedPerBatch            = 25
	defaultMaxTenantMetricDimensions    = 1000
	defaultMaxTrackedFailureTenants     = 4096
	defaultTenantFailureCounterFallback = "_default"
	defaultRetentionSweepInterval       = time.Hour
	defaultRetentionBatchSize           = 500
)

// DispatcherConfig controls dispatcher polling, retry, and metric behavior.
type DispatcherConfig struct {
	// DispatchInterval is the periodic interval between dispatch cycles.
	DispatchInterval time.Duration
	// ColdDispatchInterval is the maximum polling interval for scopes with no
	// recent outbox activity. It also defines how long a scope remains active
	// after work is observed.
	ColdDispatchInterval time.Duration
	// BatchSize is the max number of events processed per cycle.
	BatchSize int
	// PublishMaxAttempts is the max publish attempts for one event.
	PublishMaxAttempts int
	// PublishBackoff is the base backoff between publish retries.
	PublishBackoff time.Duration
	// ListPendingFailureThreshold emits an error log once repeated list failures reach this count.
	ListPendingFailureThreshold int
	// RetryWindow is the minimum age for failed events to become retry-eligible.
	RetryWindow time.Duration
	// MaxDispatchAttempts is the max total dispatch attempts before invalidation.
	MaxDispatchAttempts int
	// ProcessingTimeout is the age threshold for reclaiming stuck processing events.
	ProcessingTimeout time.Duration
	// PriorityBudget limits how many events can be selected via priority lists per cycle.
	PriorityBudget int
	// MaxFailedPerBatch limits how many failed events are reclaimed in one cycle.
	MaxFailedPerBatch int
	// PriorityEventTypes defines ordered event types to pull first each cycle.
	// For MultiTypePendingRepository implementations, these types are the
	// exclusive claim scope and unscoped fallback claims are disabled.
	PriorityEventTypes []string
	// IncludeTenantMetrics enables tenant metric attributes and can increase cardinality.
	IncludeTenantMetrics bool
	// MaxTenantMetricDimensions caps unique tenant labels before falling back to an overflow label.
	MaxTenantMetricDimensions int
	// MaxTrackedListPendingFailureTenants caps in-memory tenant counters for ListPending failures.
	MaxTrackedListPendingFailureTenants int
	// MeterProvider overrides the default global meter provider when set.
	MeterProvider metric.MeterProvider
	// OnInvalid is an optional best-effort callback invoked when an event
	// transitions to INVALID (non-retryable error or max dispatch attempts
	// exhausted). It must not panic; panics and errors are logged and swallowed.
	OnInvalid func(ctx context.Context, event *OutboxEvent, err error)
	// OnFailed is an optional best-effort callback invoked when an event fails
	// a dispatch attempt but remains retryable (marked FAILED). It must not
	// panic; panics and errors are logged and swallowed.
	OnFailed func(ctx context.Context, event *OutboxEvent, err error)
	// RetentionPublished, when positive, makes the sweep delete a PUBLISHED
	// event created longer ago than this. While it and RetentionInvalid are both
	// zero, retention is off and the other Retention fields are ignored.
	RetentionPublished time.Duration
	// RetentionInvalid, when positive, makes the sweep delete an event that became
	// INVALID longer ago than this; zero keeps them forever. Negative windows are
	// rejected. PENDING, PROCESSING and FAILED events are never deleted.
	RetentionInvalid time.Duration
	// RetentionSweepInterval is how often each dispatch scope is swept. It
	// defaults to one hour when retention is enabled.
	RetentionSweepInterval time.Duration
	// RetentionBatchSize bounds the events of each swept status deleted per sweep
	// per dispatch scope, so a large backlog drains one batch per interval. It
	// defaults to 500 when retention is enabled; a negative value is rejected.
	RetentionBatchSize int
	// RetentionKeepEventTypes lists event types the sweep never deletes.
	RetentionKeepEventTypes []string
}

// DefaultDispatcherConfig returns the baseline dispatcher configuration.
func DefaultDispatcherConfig() DispatcherConfig {
	return DispatcherConfig{
		DispatchInterval:                    defaultDispatchInterval,
		ColdDispatchInterval:                defaultColdDispatchInterval,
		BatchSize:                           defaultBatchSize,
		PublishMaxAttempts:                  defaultPublishMaxAttempts,
		PublishBackoff:                      defaultPublishBackoff,
		ListPendingFailureThreshold:         defaultListPendingFailureThreshold,
		RetryWindow:                         defaultRetryWindow,
		MaxDispatchAttempts:                 defaultMaxDispatchAttempts,
		ProcessingTimeout:                   defaultProcessingTimeout,
		PriorityBudget:                      defaultPriorityBudget,
		MaxFailedPerBatch:                   defaultMaxFailedPerBatch,
		PriorityEventTypes:                  nil,
		IncludeTenantMetrics:                false,
		MaxTenantMetricDimensions:           defaultMaxTenantMetricDimensions,
		MaxTrackedListPendingFailureTenants: defaultMaxTrackedFailureTenants,
		MeterProvider:                       nil,
	}
}

func (cfg *DispatcherConfig) normalize() {
	defaults := DefaultDispatcherConfig()

	if cfg.DispatchInterval <= 0 {
		cfg.DispatchInterval = defaults.DispatchInterval
	}

	if cfg.ColdDispatchInterval <= 0 {
		cfg.ColdDispatchInterval = defaults.ColdDispatchInterval
	}

	if cfg.ColdDispatchInterval < cfg.DispatchInterval {
		cfg.ColdDispatchInterval = cfg.DispatchInterval
	}

	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaults.BatchSize
	}

	if cfg.PublishMaxAttempts <= 0 {
		cfg.PublishMaxAttempts = defaults.PublishMaxAttempts
	}

	if cfg.PublishBackoff <= 0 {
		cfg.PublishBackoff = defaults.PublishBackoff
	}

	if cfg.ListPendingFailureThreshold <= 0 {
		cfg.ListPendingFailureThreshold = defaults.ListPendingFailureThreshold
	}

	if cfg.RetryWindow <= 0 {
		cfg.RetryWindow = defaults.RetryWindow
	}

	if cfg.MaxDispatchAttempts <= 0 {
		cfg.MaxDispatchAttempts = defaults.MaxDispatchAttempts
	}

	if cfg.ProcessingTimeout <= 0 {
		cfg.ProcessingTimeout = defaults.ProcessingTimeout
	}

	if cfg.PriorityBudget <= 0 {
		cfg.PriorityBudget = defaults.PriorityBudget
	}

	if cfg.MaxFailedPerBatch <= 0 {
		cfg.MaxFailedPerBatch = defaults.MaxFailedPerBatch
	}

	if cfg.MaxTenantMetricDimensions <= 0 {
		cfg.MaxTenantMetricDimensions = defaults.MaxTenantMetricDimensions
	}

	if cfg.MaxTrackedListPendingFailureTenants <= 0 {
		cfg.MaxTrackedListPendingFailureTenants = defaults.MaxTrackedListPendingFailureTenants
	}

	cfg.normalizeRetention()
}

// normalizeRetention fills retention defaults only when retention is enabled,
// leaving a disabled configuration exactly as the caller wrote it.
func (cfg *DispatcherConfig) normalizeRetention() {
	if !cfg.retentionEnabled() {
		return
	}

	if cfg.RetentionSweepInterval <= 0 {
		cfg.RetentionSweepInterval = defaultRetentionSweepInterval
	}

	if cfg.RetentionBatchSize == 0 {
		cfg.RetentionBatchSize = defaultRetentionBatchSize
	}
}

// validate rejects configuration that normalize must not silently repair.
func (cfg *DispatcherConfig) validate() error {
	if cfg.RetentionPublished < 0 {
		return fmt.Errorf("%w: retention %s must not be negative", ErrOutboxRetentionConfigInvalid, cfg.RetentionPublished)
	}

	if cfg.RetentionInvalid < 0 {
		return fmt.Errorf("%w: invalid retention %s must not be negative", ErrOutboxRetentionConfigInvalid, cfg.RetentionInvalid)
	}

	if cfg.retentionEnabled() && cfg.RetentionBatchSize < 0 {
		return fmt.Errorf("%w: batch size %d must not be negative", ErrOutboxRetentionConfigInvalid, cfg.RetentionBatchSize)
	}

	return nil
}

func (cfg *DispatcherConfig) retentionEnabled() bool {
	return cfg.RetentionPublished > 0 || cfg.RetentionInvalid > 0
}

// DispatcherOption mutates dispatcher configuration at construction.
type DispatcherOption func(*Dispatcher)

// WithBatchSize sets the maximum events processed in one dispatch cycle.
func WithBatchSize(size int) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if size > 0 {
			dispatcher.cfg.BatchSize = size
		}
	}
}

// WithDispatchInterval sets the dispatch polling interval.
func WithDispatchInterval(interval time.Duration) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if interval > 0 {
			dispatcher.cfg.DispatchInterval = interval
		}
	}
}

// WithColdDispatchInterval sets the maximum polling interval for scopes with
// no recent outbox activity. Values shorter than DispatchInterval are
// normalized to DispatchInterval.
func WithColdDispatchInterval(interval time.Duration) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if interval > 0 {
			dispatcher.cfg.ColdDispatchInterval = interval
		}
	}
}

// WithPublishMaxAttempts sets max publish attempts per event.
func WithPublishMaxAttempts(maxAttempts int) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if maxAttempts > 0 {
			dispatcher.cfg.PublishMaxAttempts = maxAttempts
		}
	}
}

// WithPublishBackoff sets base backoff for publish retry attempts.
func WithPublishBackoff(backoff time.Duration) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if backoff > 0 {
			dispatcher.cfg.PublishBackoff = backoff
		}
	}
}

// WithRetryWindow sets failed-event cooldown before retry reclamation.
func WithRetryWindow(retryWindow time.Duration) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if retryWindow > 0 {
			dispatcher.cfg.RetryWindow = retryWindow
		}
	}
}

// WithMaxDispatchAttempts sets max dispatch attempts before invalidation.
func WithMaxDispatchAttempts(attempts int) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if attempts > 0 {
			dispatcher.cfg.MaxDispatchAttempts = attempts
		}
	}
}

// WithProcessingTimeout sets the timeout used to reclaim stuck processing events.
func WithProcessingTimeout(timeout time.Duration) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if timeout > 0 {
			dispatcher.cfg.ProcessingTimeout = timeout
		}
	}
}

// WithListPendingFailureThreshold sets the log threshold for repeated list failures.
func WithListPendingFailureThreshold(threshold int) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if threshold > 0 {
			dispatcher.cfg.ListPendingFailureThreshold = threshold
		}
	}
}

// WithPriorityBudget sets the per-cycle priority selection budget.
func WithPriorityBudget(budget int) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if budget > 0 {
			dispatcher.cfg.PriorityBudget = budget
		}
	}
}

// WithMaxFailedPerBatch sets max failed events reclaimed each cycle.
func WithMaxFailedPerBatch(maxFailed int) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if maxFailed > 0 {
			dispatcher.cfg.MaxFailedPerBatch = maxFailed
		}
	}
}

// WithPriorityEventTypes sets the ordered event types selected before generic pending events.
// Repositories implementing MultiTypePendingRepository treat them as an exclusive
// claim scope and do not fall back to unscoped stuck, failed, or pending events.
func WithPriorityEventTypes(eventTypes ...string) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		types := make([]string, 0, len(eventTypes))
		for _, eventType := range eventTypes {
			normalized := strings.TrimSpace(eventType)
			if normalized == "" {
				continue
			}

			types = append(types, normalized)
		}

		if len(types) == 0 {
			dispatcher.cfg.PriorityEventTypes = nil

			return
		}

		dispatcher.cfg.PriorityEventTypes = types
	}
}

// WithRetryClassifier sets the non-retryable error classifier.
func WithRetryClassifier(classifier RetryClassifier) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if nilcheck.Interface(classifier) {
			dispatcher.retryClassifier = nil

			return
		}

		dispatcher.retryClassifier = classifier
	}
}

// WithTenantMetricAttributes toggles tenant attributes for dispatcher metrics.
func WithTenantMetricAttributes(enabled bool) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		dispatcher.cfg.IncludeTenantMetrics = enabled
	}
}

// WithMaxTenantMetricDimensions sets the maximum unique tenant labels used in metrics.
func WithMaxTenantMetricDimensions(maxDimensions int) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if maxDimensions > 0 {
			dispatcher.cfg.MaxTenantMetricDimensions = maxDimensions
		}
	}
}

// WithMaxTrackedListPendingFailureTenants sets the in-memory cap for tenant-specific ListPending failure counters.
func WithMaxTrackedListPendingFailureTenants(maxTenants int) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if maxTenants > 0 {
			dispatcher.cfg.MaxTrackedListPendingFailureTenants = maxTenants
		}
	}
}

// WithOnInvalid sets a best-effort callback invoked when an event transitions to INVALID.
// Passing nil disables the callback.
func WithOnInvalid(fn func(ctx context.Context, event *OutboxEvent, err error)) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		dispatcher.cfg.OnInvalid = fn
	}
}

// WithOnFailed sets a best-effort callback invoked when an event is marked FAILED but remains retryable.
// Passing nil disables the callback.
func WithOnFailed(fn func(ctx context.Context, event *OutboxEvent, err error)) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		dispatcher.cfg.OnFailed = fn
	}
}

// WithMeterProvider injects a custom meter provider for dispatcher metrics.
// Passing nil keeps the default global OpenTelemetry meter provider.
func WithMeterProvider(provider metric.MeterProvider) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		if nilcheck.Interface(provider) {
			dispatcher.cfg.MeterProvider = nil

			return
		}

		dispatcher.cfg.MeterProvider = provider
	}
}

// WithRetentionPublished enables the retention sweep: PUBLISHED events created
// longer ago than retention are deleted in bounded batches. Zero disables it;
// a negative value makes NewDispatcher fail.
func WithRetentionPublished(retention time.Duration) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		dispatcher.cfg.RetentionPublished = retention
	}
}

// WithRetentionInvalid makes the retention sweep also delete events that became
// INVALID longer ago than retention. Zero, the default, keeps them forever; a
// negative value makes NewDispatcher fail.
func WithRetentionInvalid(retention time.Duration) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		dispatcher.cfg.RetentionInvalid = retention
	}
}

// WithRetentionSweepInterval sets how often each dispatch scope is swept.
// Non-positive values keep the one-hour default.
func WithRetentionSweepInterval(interval time.Duration) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		dispatcher.cfg.RetentionSweepInterval = interval
	}
}

// WithRetentionBatchSize bounds the events of each swept status deleted per
// sweep per dispatch scope. Zero keeps the default of 500; a negative value
// makes NewDispatcher fail while retention is enabled.
func WithRetentionBatchSize(size int) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		dispatcher.cfg.RetentionBatchSize = size
	}
}

// WithRetentionKeepEventTypes lists event types the retention sweep never
// deletes. Blank entries are dropped.
func WithRetentionKeepEventTypes(eventTypes ...string) DispatcherOption {
	return func(dispatcher *Dispatcher) {
		types := make([]string, 0, len(eventTypes))

		for _, eventType := range eventTypes {
			if normalized := strings.TrimSpace(eventType); normalized != "" {
				types = append(types, normalized)
			}
		}

		if len(types) == 0 {
			types = nil
		}

		dispatcher.cfg.RetentionKeepEventTypes = types
	}
}
