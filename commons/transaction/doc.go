// Package transaction provides transaction intent planning and posting validations.
//
// Core flow:
//   - BuildIntentPlan validates and expands allocations into postings.
//   - ValidateBalanceEligibility checks source/destination constraints.
//   - ApplyPosting applies operation/status transitions to balances.
//
// The package enforces deterministic behavior using typed domain errors.
package transaction
