// Package countertier2vardecl exercises three otherwise-unreached analyzer
// helpers:
//
//  1. resolveVarInitLit — f.Counter(LedgerTxTotal) where LedgerTxTotal is a
//     package-level `var ... = metrics.Metric{...}`. The analyzer must
//     resolve the ident back to its composite literal init and harvest
//     Name/Unit/Description.
//
//  2. bindValueSpec — `var counter, _ = f.Counter(...)` (a top-level
//     ValueSpec). The analyzer must bind the call to the `counter` ident
//     so subsequent .WithLabels(...).AddOne(...) sites can be attributed
//     back to the counter.
//
//  3. extractMapStringKeys — counter.WithLabels(map[string]string{...}).
//     The analyzer must surface "tenant_id" and "result" as labels on the
//     recording site.
package countertier2vardecl

import (
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
)

// LedgerTxTotal is a package-level metric definition. The analyzer resolves
// this ident back to its composite literal via resolveVarInitLit.
var LedgerTxTotal = metrics.Metric{
	Name:        "ledger_tx_total",
	Unit:        "1",
	Description: "ledger transactions",
}

func emit(f *metrics.MetricsFactory, ctx context.Context) error {
	// Top-level ValueSpec — exercises bindValueSpec on the analyzer's
	// AST walk.
	var counter, err = f.Counter(LedgerTxTotal) // want `counter "ledger_tx_total" tier=2`
	if err != nil {
		return err
	}

	// WithLabels(map[string]string{...}) drives extractMapStringKeys; the
	// recorded labels must be unioned and sorted onto this emission site.
	return counter.WithLabels(map[string]string{"tenant_id": "", "result": ""}).AddOne(ctx) // want `counter record site labels=\[result tenant_id\]`
}
