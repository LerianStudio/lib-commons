package transaction

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

var oneHundred = decimal.NewFromInt(100)

// BuildIntentPlan validates input allocations and builds a normalized intent plan.
func BuildIntentPlan(input TransactionIntentInput, status TransactionStatus) (IntentPlan, error) {
	if strings.TrimSpace(input.Asset) == "" {
		return IntentPlan{}, NewDomainError(ErrorInvalidInput, "asset", "asset is required")
	}

	if !input.Total.IsPositive() {
		return IntentPlan{}, NewDomainError(ErrorInvalidInput, "total", "total must be greater than zero")
	}

	if len(input.Sources) == 0 {
		return IntentPlan{}, NewDomainError(ErrorInvalidInput, "sources", "at least one source is required")
	}

	if len(input.Destinations) == 0 {
		return IntentPlan{}, NewDomainError(ErrorInvalidInput, "destinations", "at least one destination is required")
	}

	sources, err := buildPostings(input.Asset, input.Total, input.Pending, status, input.Sources, true)
	if err != nil {
		return IntentPlan{}, err
	}

	destinations, err := buildPostings(input.Asset, input.Total, input.Pending, status, input.Destinations, false)
	if err != nil {
		return IntentPlan{}, err
	}

	sourceTotal := sumPostings(sources)

	destinationTotal := sumPostings(destinations)
	if !sourceTotal.Equal(input.Total) || !destinationTotal.Equal(input.Total) {
		return IntentPlan{}, NewDomainError(
			ErrorTransactionValueMismatch,
			"total",
			fmt.Sprintf("source total=%s destination total=%s expected=%s", sourceTotal, destinationTotal, input.Total),
		)
	}

	sourceIDs := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		sourceIDs[source.Target.BalanceID] = struct{}{}
	}

	for _, destination := range destinations {
		if _, exists := sourceIDs[destination.Target.BalanceID]; exists {
			return IntentPlan{}, NewDomainError(ErrorTransactionAmbiguous, "destinations", "balance appears as source and destination")
		}
	}

	return IntentPlan{
		Asset:        input.Asset,
		Total:        input.Total,
		Pending:      input.Pending,
		Sources:      sources,
		Destinations: destinations,
	}, nil
}

// ValidateBalanceEligibility checks whether balances can participate in a plan.
// It validates:
//   - All referenced balances exist in the catalog
//   - Asset codes match the transaction asset
//   - Sending/receiving permissions are met
//   - Source balances have sufficient available funds for the posting amount
//   - Account ownership: posting target AccountID matches balance AccountID
//   - All balances share the same OrganizationID and LedgerID (no cross-scope mixing)
//   - External account constraints (pending holds, zero-balance destinations)
func ValidateBalanceEligibility(plan IntentPlan, balances map[string]Balance) error {
	if len(balances) == 0 {
		return NewDomainError(ErrorAccountIneligibility, "balances", "balance catalog is empty")
	}

	// Cross-scope validation: all balances must belong to the same org and ledger.
	var refOrgID, refLedgerID string

	for _, posting := range plan.Sources {
		if err := validateSourcePosting(plan, posting, balances, &refOrgID, &refLedgerID); err != nil {
			return err
		}
	}

	for _, posting := range plan.Destinations {
		if err := validateDestinationPosting(plan, posting, balances, &refOrgID, &refLedgerID); err != nil {
			return err
		}
	}

	return nil
}

// validateSourcePosting validates a single source posting against its balance.
func validateSourcePosting(plan IntentPlan, posting Posting, balances map[string]Balance, refOrgID, refLedgerID *string) error {
	balance, ok := balances[posting.Target.BalanceID]
	if !ok {
		return NewDomainError(ErrorAccountIneligibility, "sources", "source balance not found")
	}

	// Account ownership validation
	if balance.AccountID != posting.Target.AccountID {
		return NewDomainError(ErrorAccountIneligibility, "sources", "source posting accountId does not match balance accountId")
	}

	// Cross-scope check
	if err := validateScope(refOrgID, refLedgerID, balance, "sources"); err != nil {
		return err
	}

	if balance.Asset != plan.Asset {
		return NewDomainError(ErrorAssetCodeNotFound, "sources", "source asset does not match transaction asset")
	}

	if !balance.AllowSending {
		return NewDomainError(ErrorAccountStatusTransactionRestriction, "sources", "source balance is not allowed to send")
	}

	// Amount sufficiency check: source must have enough available funds
	if balance.AllowSending && balance.Available.LessThan(posting.Amount) {
		return NewDomainError(ErrorInsufficientFunds, "sources",
			fmt.Sprintf("source balance available %s is less than posting amount %s", balance.Available, posting.Amount))
	}

	if plan.Pending && balance.AccountType == AccountTypeExternal {
		return NewDomainError(ErrorOnHoldExternalAccount, "sources", "external source cannot be put on hold")
	}

	return nil
}

// validateDestinationPosting validates a single destination posting against its balance.
func validateDestinationPosting(plan IntentPlan, posting Posting, balances map[string]Balance, refOrgID, refLedgerID *string) error {
	balance, ok := balances[posting.Target.BalanceID]
	if !ok {
		return NewDomainError(ErrorAccountIneligibility, "destinations", "destination balance not found")
	}

	// Account ownership validation
	if balance.AccountID != posting.Target.AccountID {
		return NewDomainError(ErrorAccountIneligibility, "destinations", "destination posting accountId does not match balance accountId")
	}

	// Cross-scope check
	if err := validateScope(refOrgID, refLedgerID, balance, "destinations"); err != nil {
		return err
	}

	if balance.Asset != plan.Asset {
		return NewDomainError(ErrorAssetCodeNotFound, "destinations", "destination asset does not match transaction asset")
	}

	if !balance.AllowReceiving {
		return NewDomainError(ErrorAccountStatusTransactionRestriction, "destinations", "destination balance is not allowed to receive")
	}

	if err := validateExternalDestinationBalance(balance); err != nil {
		return err
	}

	return nil
}

// validateExternalDestinationBalance enforces constraints on external destination accounts:
// they must have exactly zero available balance (negative indicates data corruption).
func validateExternalDestinationBalance(balance Balance) error {
	if balance.AccountType != AccountTypeExternal {
		return nil
	}

	if balance.Available.IsNegative() {
		return NewDomainError(ErrorDataCorruption, "balance", "external destination account has negative balance, indicating data corruption")
	}

	if balance.Available.IsPositive() {
		return NewDomainError(ErrorInsufficientFunds, "destinations", "external destination must have zero available balance")
	}

	return nil
}

// validateScope ensures all balances in a plan share the same OrganizationID and LedgerID.
// On first call (ref values are empty), it captures the reference values.
// On subsequent calls, it compares against the captured reference.
func validateScope(refOrgID, refLedgerID *string, balance Balance, field string) error {
	if balance.OrganizationID != "" || balance.LedgerID != "" {
		if *refOrgID == "" && *refLedgerID == "" {
			*refOrgID = balance.OrganizationID
			*refLedgerID = balance.LedgerID
		} else {
			if balance.OrganizationID != *refOrgID {
				return NewDomainError(ErrorCrossScope, field,
					fmt.Sprintf("balance organizationId %q does not match expected %q", balance.OrganizationID, *refOrgID))
			}

			if balance.LedgerID != *refLedgerID {
				return NewDomainError(ErrorCrossScope, field,
					fmt.Sprintf("balance ledgerId %q does not match expected %q", balance.LedgerID, *refLedgerID))
			}
		}
	}

	return nil
}

// ApplyPosting applies a posting transition to a balance and returns the new state.
func ApplyPosting(balance Balance, posting Posting) (Balance, error) {
	if err := validatePostingAgainstBalance(balance, posting); err != nil {
		return Balance{}, err
	}

	result := balance

	updated, err := applyPostingOperation(result, posting)
	if err != nil {
		return Balance{}, err
	}

	if err := validatePostingResult(updated); err != nil {
		return Balance{}, err
	}

	updated.Version++

	return updated, nil
}

func validatePostingAgainstBalance(balance Balance, posting Posting) error {
	if err := posting.Target.validate("posting.target"); err != nil {
		return err
	}

	if balance.ID != posting.Target.BalanceID {
		return NewDomainError(ErrorAccountIneligibility, "posting.target.balanceId", "posting does not belong to the provided balance")
	}

	if balance.AccountID != posting.Target.AccountID {
		return NewDomainError(ErrorAccountIneligibility, "posting.target.accountId", "posting account does not match balance account")
	}

	if balance.Asset != posting.Asset {
		return NewDomainError(ErrorAssetCodeNotFound, "posting.asset", "posting asset does not match balance asset")
	}

	if !posting.Amount.IsPositive() {
		return NewDomainError(ErrorInvalidInput, "posting.amount", "posting amount must be greater than zero")
	}

	return nil
}

func applyPostingOperation(balance Balance, posting Posting) (Balance, error) {
	result := balance

	switch posting.Operation {
	case OperationOnHold:
		return applyOnHold(result, posting)
	case OperationRelease:
		return applyRelease(result, posting)
	case OperationDebit:
		return applyDebit(result, posting)
	case OperationCredit:
		return applyCredit(result, posting)
	default:
		return Balance{}, NewDomainError(ErrorInvalidInput, "posting.operation", "unsupported operation")
	}
}

func applyOnHold(balance Balance, posting Posting) (Balance, error) {
	if posting.Status != StatusPending {
		return Balance{}, NewDomainError(ErrorInvalidStateTransition, "posting.status", "ON_HOLD requires PENDING status")
	}

	balance.Available = balance.Available.Sub(posting.Amount)
	balance.OnHold = balance.OnHold.Add(posting.Amount)

	return balance, nil
}

func applyRelease(balance Balance, posting Posting) (Balance, error) {
	if posting.Status != StatusCanceled {
		return Balance{}, NewDomainError(ErrorInvalidStateTransition, "posting.status", "RELEASE requires CANCELED status")
	}

	balance.OnHold = balance.OnHold.Sub(posting.Amount)
	balance.Available = balance.Available.Add(posting.Amount)

	return balance, nil
}

func applyDebit(balance Balance, posting Posting) (Balance, error) {
	switch posting.Status {
	case StatusApproved:
		balance.OnHold = balance.OnHold.Sub(posting.Amount)
	case StatusCreated:
		balance.Available = balance.Available.Sub(posting.Amount)
	case StatusCanceled:
		// Pending destination cancellation: the debit reverses the original credit
		// that was applied when the destination received funds during the PENDING phase.
		// ResolveOperation(pending=true, isSource=false, StatusCanceled) → OperationDebit.
		balance.Available = balance.Available.Sub(posting.Amount)
	default:
		return Balance{}, NewDomainError(
			ErrorInvalidStateTransition,
			"posting.status",
			"DEBIT only supports CREATED, APPROVED, or CANCELED status",
		)
	}

	return balance, nil
}

func applyCredit(balance Balance, posting Posting) (Balance, error) {
	switch posting.Status {
	case StatusCreated, StatusApproved, StatusPending:
		balance.Available = balance.Available.Add(posting.Amount)
	default:
		return Balance{}, NewDomainError(
			ErrorInvalidStateTransition,
			"posting.status",
			"CREDIT only supports CREATED, APPROVED, or PENDING status",
		)
	}

	return balance, nil
}

func validatePostingResult(balance Balance) error {
	if balance.Available.IsNegative() {
		return NewDomainError(ErrorInsufficientFunds, "posting.amount", "operation would result in negative available balance")
	}

	if balance.OnHold.IsNegative() {
		return NewDomainError(ErrorInsufficientFunds, "posting.amount", "operation would result in negative on-hold balance")
	}

	return nil
}

// ResolveOperation resolves the posting operation from pending/source/status semantics.
func ResolveOperation(pending bool, isSource bool, status TransactionStatus) (Operation, error) {
	if pending {
		switch status {
		case StatusPending:
			if isSource {
				return OperationOnHold, nil
			}

			return OperationCredit, nil
		case StatusCanceled:
			if isSource {
				return OperationRelease, nil
			}

			return OperationDebit, nil
		case StatusApproved:
			if isSource {
				return OperationDebit, nil
			}

			return OperationCredit, nil
		default:
			return "", NewDomainError(ErrorInvalidStateTransition, "status", "pending transactions only support PENDING, APPROVED, or CANCELED status")
		}
	}

	switch status {
	case StatusCreated:
		if isSource {
			return OperationDebit, nil
		}

		return OperationCredit, nil
	default:
		return "", NewDomainError(ErrorInvalidStateTransition, "status", "non-pending transactions only support CREATED status")
	}
}

func buildPostings(asset string, total decimal.Decimal, pending bool, status TransactionStatus, allocations []Allocation, isSource bool) ([]Posting, error) {
	postings := make([]Posting, len(allocations))
	allocated := decimal.Zero
	remainderIndex := -1

	side := "destinations"
	if isSource {
		side = "sources"
	}

	for i, allocation := range allocations {
		field := fmt.Sprintf("%s[%d]", side, i)

		posting, amount, usesRemainder, err := buildPostingFromAllocation(
			asset,
			total,
			pending,
			status,
			isSource,
			allocation,
			field,
		)
		if err != nil {
			return nil, err
		}

		postings[i] = posting

		if usesRemainder {
			if remainderIndex >= 0 {
				return nil, NewDomainError(ErrorInvalidInput, field+".remainder", "only one remainder allocation is allowed")
			}

			remainderIndex = i

			continue
		}

		allocated = allocated.Add(amount)
	}

	if remainderIndex >= 0 {
		remainder, err := computeRemainderAllocation(total, allocated)
		if err != nil {
			return nil, err
		}

		postings[remainderIndex].Amount = remainder
		allocated = allocated.Add(remainder)
	}

	if err := validateAllocatedTotal(allocated, total); err != nil {
		return nil, err
	}

	return postings, nil
}

func buildPostingFromAllocation(
	asset string,
	total decimal.Decimal,
	pending bool,
	status TransactionStatus,
	isSource bool,
	allocation Allocation,
	field string,
) (Posting, decimal.Decimal, bool, error) {
	if err := allocation.Target.validate(field + ".target"); err != nil {
		return Posting{}, decimal.Zero, false, err
	}

	if err := validateAllocationStrategy(allocation, field); err != nil {
		return Posting{}, decimal.Zero, false, err
	}

	operation, err := ResolveOperation(pending, isSource, status)
	if err != nil {
		return Posting{}, decimal.Zero, false, err
	}

	posting := Posting{
		Target:    allocation.Target,
		Asset:     asset,
		Operation: operation,
		Status:    status,
		Route:     allocation.Route,
	}

	amount, usesRemainder, err := resolveAllocationAmount(total, allocation, field)
	if err != nil {
		return Posting{}, decimal.Zero, false, err
	}

	if usesRemainder {
		return posting, decimal.Zero, true, nil
	}

	posting.Amount = amount

	return posting, amount, false, nil
}

func validateAllocationStrategy(allocation Allocation, field string) error {
	strategyCount := 0
	if allocation.Amount != nil {
		strategyCount++
	}

	if allocation.Share != nil {
		strategyCount++
	}

	if allocation.Remainder {
		strategyCount++
	}

	if strategyCount != 1 {
		return NewDomainError(ErrorInvalidInput, field, "allocation must define exactly one strategy: amount, share, or remainder")
	}

	return nil
}

func resolveAllocationAmount(total decimal.Decimal, allocation Allocation, field string) (decimal.Decimal, bool, error) {
	if allocation.Amount != nil {
		if !allocation.Amount.IsPositive() {
			return decimal.Zero, false, NewDomainError(ErrorInvalidInput, field+".amount", "amount must be greater than zero")
		}

		return *allocation.Amount, false, nil
	}

	if allocation.Share != nil {
		share := *allocation.Share
		if !share.IsPositive() || share.GreaterThan(oneHundred) {
			return decimal.Zero, false, NewDomainError(ErrorInvalidInput, field+".share", "share must be greater than 0 and at most 100")
		}

		amount := total.Mul(share.Div(oneHundred))
		if !amount.IsPositive() {
			return decimal.Zero, false, NewDomainError(ErrorInvalidInput, field+".share", "share produces a non-positive amount")
		}

		return amount, false, nil
	}

	if allocation.Remainder {
		return decimal.Zero, true, nil
	}

	return decimal.Zero, false, NewDomainError(ErrorInvalidInput, field, "allocation must define exactly one strategy: amount, share, or remainder")
}

func computeRemainderAllocation(total decimal.Decimal, allocated decimal.Decimal) (decimal.Decimal, error) {
	remainder := total.Sub(allocated)
	if !remainder.IsPositive() {
		return decimal.Zero, NewDomainError(ErrorTransactionValueMismatch, "allocations", "remainder is zero or negative")
	}

	return remainder, nil
}

func validateAllocatedTotal(allocated decimal.Decimal, total decimal.Decimal) error {
	if !allocated.Equal(total) {
		return NewDomainError(
			ErrorTransactionValueMismatch,
			"allocations",
			fmt.Sprintf("allocated=%s expected=%s", allocated, total),
		)
	}

	return nil
}

func sumPostings(postings []Posting) decimal.Decimal {
	total := decimal.Zero

	for _, posting := range postings {
		total = total.Add(posting.Amount)
	}

	return total
}
