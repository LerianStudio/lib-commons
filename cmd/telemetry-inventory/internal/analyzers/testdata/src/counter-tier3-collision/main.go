package countertier3collision

import "context"

// MyService defines a method named RecordAccountCreated. It is not the
// lib-commons MetricsFactory and the analyzer must NOT detect it as a
// Tier-3 counter. Without the isMetricsFactory type guard the analyzer
// would fire a false-positive `counter "accounts_created" tier=3` report.
type MyService struct{}

func (*MyService) RecordAccountCreated(context.Context) error {
	return nil
}

func emit(s *MyService, ctx context.Context) error {
	return s.RecordAccountCreated(ctx)
}
