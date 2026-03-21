package outbox

import (
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/internal/nilcheck"
	"go.opentelemetry.io/otel/metric"
)

const (
	defaultDispatchInterval             = 2 * time.Second
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
)

// DispatcherConfig controls dispatcher polling, retry, and metric behavior.
type DispatcherConfig struct {
	// DispatchInterval is the periodic interval between dispatch cycles.
	DispatchInterval time.Duration
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
	PriorityEventTypes []string
	// IncludeTenantMetrics enables tenant metric attributes and can increase cardinality.
	IncludeTenantMetrics bool
	// MaxTenantMetricDimensions caps unique tenant labels before falling back to an overflow label.
	MaxTenantMetricDimensions int
	// MaxTrackedListPendingFailureTenants caps in-memory tenant counters for ListPending failures.
	MaxTrackedListPendingFailureTenants int
	// MeterProvider overrides the default global meter provider when set.
	MeterProvider metric.MeterProvider
}

// DefaultDispatcherConfig returns the baseline dispatcher configuration.
func DefaultDispatcherConfig() DispatcherConfig {
	return DispatcherConfig{
		DispatchInterval:                    defaultDispatchInterval,
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
