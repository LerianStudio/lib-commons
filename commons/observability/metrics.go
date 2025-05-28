package observability

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// BusinessMetrics provides business-specific metrics
type BusinessMetrics struct {
	meter metric.Meter
	
	// Transaction metrics
	transactionCounter       metric.Int64Counter
	transactionDuration      metric.Float64Histogram
	transactionAmount        metric.Float64Histogram
	transactionErrorCounter  metric.Int64Counter
	
	// Account metrics
	accountCounter           metric.Int64Counter
	accountBalanceGauge      metric.Float64ObservableGauge
	
	// Ledger metrics
	ledgerCounter            metric.Int64Counter
	
	// Asset metrics
	assetCounter             metric.Int64Counter
	assetRateGauge           metric.Float64ObservableGauge
}

// NewBusinessMetrics creates a new business metrics instance
func NewBusinessMetrics(meter metric.Meter) (*BusinessMetrics, error) {
	bm := &BusinessMetrics{meter: meter}
	
	// Initialize transaction metrics
	transactionCounter, err := meter.Int64Counter(
		"midaz.transaction.count",
		metric.WithDescription("Total number of transactions"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction counter: %w", err)
	}
	bm.transactionCounter = transactionCounter
	
	transactionDuration, err := meter.Float64Histogram(
		"midaz.transaction.duration",
		metric.WithDescription("Transaction processing duration"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction duration histogram: %w", err)
	}
	bm.transactionDuration = transactionDuration
	
	transactionAmount, err := meter.Float64Histogram(
		"midaz.transaction.amount",
		metric.WithDescription("Transaction amounts"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction amount histogram: %w", err)
	}
	bm.transactionAmount = transactionAmount
	
	transactionErrorCounter, err := meter.Int64Counter(
		"midaz.transaction.errors",
		metric.WithDescription("Total number of transaction errors"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction error counter: %w", err)
	}
	bm.transactionErrorCounter = transactionErrorCounter
	
	// Initialize account metrics
	accountCounter, err := meter.Int64Counter(
		"midaz.account.count",
		metric.WithDescription("Total number of accounts created"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create account counter: %w", err)
	}
	bm.accountCounter = accountCounter
	
	// Initialize ledger metrics
	ledgerCounter, err := meter.Int64Counter(
		"midaz.ledger.count",
		metric.WithDescription("Total number of ledgers created"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create ledger counter: %w", err)
	}
	bm.ledgerCounter = ledgerCounter
	
	// Initialize asset metrics
	assetCounter, err := meter.Int64Counter(
		"midaz.asset.count",
		metric.WithDescription("Total number of assets created"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create asset counter: %w", err)
	}
	bm.assetCounter = assetCounter
	
	return bm, nil
}

// RecordTransaction records a transaction metric
func (bm *BusinessMetrics) RecordTransaction(
	ctx context.Context,
	organizationID string,
	ledgerID string,
	status string,
	transactionType string,
	amount float64,
	currency string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("organization_id", organizationID),
		attribute.String("ledger_id", ledgerID),
		attribute.String("status", status),
		attribute.String("type", transactionType),
	}
	
	if currency != "" {
		attrs = append(attrs, attribute.String("currency", currency))
	}
	
	bm.transactionCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	
	if amount > 0 {
		bm.transactionAmount.Record(ctx, amount, metric.WithAttributes(attrs...))
	}
}

// RecordTransactionDuration records transaction processing duration
func (bm *BusinessMetrics) RecordTransactionDuration(
	ctx context.Context,
	organizationID string,
	ledgerID string,
	duration float64,
) {
	attrs := []attribute.KeyValue{
		attribute.String("organization_id", organizationID),
		attribute.String("ledger_id", ledgerID),
	}
	
	bm.transactionDuration.Record(ctx, duration, metric.WithAttributes(attrs...))
}

// RecordTransactionError records a transaction error
func (bm *BusinessMetrics) RecordTransactionError(
	ctx context.Context,
	organizationID string,
	ledgerID string,
	errorType string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("organization_id", organizationID),
		attribute.String("ledger_id", ledgerID),
		attribute.String("error_type", errorType),
	}
	
	bm.transactionErrorCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordAccount records an account creation
func (bm *BusinessMetrics) RecordAccount(
	ctx context.Context,
	organizationID string,
	ledgerID string,
	accountType string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("organization_id", organizationID),
		attribute.String("ledger_id", ledgerID),
		attribute.String("account_type", accountType),
	}
	
	bm.accountCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordLedger records a ledger creation
func (bm *BusinessMetrics) RecordLedger(
	ctx context.Context,
	organizationID string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("organization_id", organizationID),
	}
	
	bm.ledgerCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordAsset records an asset creation
func (bm *BusinessMetrics) RecordAsset(
	ctx context.Context,
	organizationID string,
	ledgerID string,
	assetType string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("organization_id", organizationID),
		attribute.String("ledger_id", ledgerID),
		attribute.String("asset_type", assetType),
	}
	
	bm.assetCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}