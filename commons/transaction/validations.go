package transaction

import (
	"context"
	"github.com/LerianStudio/lib-commons/commons"
	constant "github.com/LerianStudio/lib-commons/commons/constants"
	"github.com/LerianStudio/lib-commons/commons/opentelemetry"
	"github.com/shopspring/decimal"
	"strconv"
	"strings"
)

// ValidateBalancesRules function with some validates in accounts and DSL operations
func ValidateBalancesRules(ctx context.Context, transaction Transaction, validate Responses, balances []*Balance) error {
	logger := commons.NewLoggerFromContext(ctx)
	tracer := commons.NewTracerFromContext(ctx)

	_, spanValidateBalances := tracer.Start(ctx, "validations.validate_balances_rules")

	if len(balances) != (len(validate.From) + len(validate.To)) {
		err := commons.ValidateBusinessError(constant.ErrAccountIneligibility, "ValidateAccounts")

		opentelemetry.HandleSpanError(&spanValidateBalances, "validations.validate_balances_rules", err)

		return err
	}

	for _, balance := range balances {
		if err := validateFromBalances(balance, validate.From, validate.Asset); err != nil {
			opentelemetry.HandleSpanError(&spanValidateBalances, "validations.validate_from_balances_", err)

			logger.Errorf("validations.validate_from_balances_err: %s", err)

			return err
		}

		if err := validateToBalances(balance, validate.To, validate.Asset); err != nil {
			opentelemetry.HandleSpanError(&spanValidateBalances, "validations.validate_to_balances_", err)

			logger.Errorf("validations.validate_to_balances_err: %s", err)

			return err
		}

		if err := validateBalance(balance, transaction, validate.From); err != nil {
			opentelemetry.HandleSpanError(&spanValidateBalances, "validations.validate_to_balances_", err)

			logger.Errorf("validations.validate_balances_err: %s", err)

			return err
		}
	}

	spanValidateBalances.End()

	return nil
}

func validateBalance(balance *Balance, dsl Transaction, from map[string]Amount) error {
	for key := range from {
		for _, f := range dsl.Send.Source.From {
			if balance.ID == key || balance.Alias == key {
				blc := Balance{
					Available: balance.Available,
					OnHold:    balance.OnHold,
				}

				ba, err := OperateBalances(from[f.AccountAlias], blc)
				if err != nil {
					return err
				}

				if ba.Available.IsNegative() && balance.AccountType != constant.ExternalAccountType {
					return commons.ValidateBusinessError(constant.ErrInsufficientFunds, "validateBalance", balance.Alias)
				}
			}
		}
	}

	return nil
}

func validateFromBalances(balance *Balance, from map[string]Amount, asset string) error {
	for key := range from {
		if balance.ID == key || balance.Alias == key {
			if balance.AssetCode != asset {
				return commons.ValidateBusinessError(constant.ErrAssetCodeNotFound, "validateFromAccounts")
			}

			if !balance.AllowSending {
				return commons.ValidateBusinessError(constant.ErrAccountStatusTransactionRestriction, "validateFromAccounts")
			}

			if (balance.Available.IsZero() || balance.Available.IsNegative()) && balance.AccountType != constant.ExternalAccountType {
				return commons.ValidateBusinessError(constant.ErrInsufficientFunds, "validateFromAccounts", balance.Alias)
			}
		}
	}

	return nil
}

func validateToBalances(balance *Balance, to map[string]Amount, asset string) error {
	for key := range to {
		if balance.ID == key || balance.Alias == key {
			if balance.AssetCode != asset {
				return commons.ValidateBusinessError(constant.ErrAssetCodeNotFound, "validateToAccounts")
			}

			if !balance.AllowReceiving {
				return commons.ValidateBusinessError(constant.ErrAccountStatusTransactionRestriction, "validateToAccounts")
			}

			if balance.Available.IsPositive() && balance.AccountType == constant.ExternalAccountType {
				return commons.ValidateBusinessError(constant.ErrInsufficientFunds, "validateToAccounts", balance.Alias)
			}
		}
	}

	return nil
}

// ValidateFromToOperation func that validate operate balance
func ValidateFromToOperation(ft FromTo, validate Responses, balance *Balance) (Amount, Balance, error) {
	amount := Amount{}

	balanceAfter := Balance{}

	if ft.IsFrom {
		blc := Balance{
			Available: balance.Available,
			OnHold:    balance.OnHold,
		}

		ba, err := OperateBalances(validate.From[ft.AccountAlias], blc)
		if err != nil {
			return Amount{}, Balance{}, err
		}

		if ba.Available.IsNegative() && balance.AccountType != constant.ExternalAccountType {
			return amount, balanceAfter, commons.ValidateBusinessError(constant.ErrInsufficientFunds, "ValidateFromToOperation", balance.Alias)
		}

		amount = Amount{
			Value: validate.From[ft.AccountAlias].Value,
		}

		balanceAfter = ba
	} else {
		blc := Balance{
			Available: balance.Available,
			OnHold:    balance.OnHold,
		}

		ba, err := OperateBalances(validate.To[ft.AccountAlias], blc)
		if err != nil {
			return Amount{}, Balance{}, err
		}

		amount = Amount{
			Value: validate.To[ft.AccountAlias].Value,
		}

		balanceAfter = ba
	}

	return amount, balanceAfter, nil
}

// UpdateBalances function with some updates values in balances.
func UpdateBalances(fromTo map[string]Amount, balances []*Balance, result chan []*Balance, er chan error) {
	newBalances := make([]*Balance, 0)

	for _, balance := range balances {
		for key := range fromTo {
			if balance.ID == key || balance.Alias == key {
				blc := Balance{
					Available: balance.Available,
					OnHold:    balance.OnHold,
				}

				b, err := OperateBalances(fromTo[key], blc)
				if err != nil {
					er <- err

					return
				}

				ac := Balance{
					ID:             balance.ID,
					Alias:          balance.Alias,
					OrganizationID: balance.OrganizationID,
					LedgerID:       balance.LedgerID,
					AssetCode:      balance.AssetCode,
					Available:      b.Available,
					OnHold:         b.OnHold,
					AllowSending:   balance.AllowSending,
					AllowReceiving: balance.AllowReceiving,
					AccountType:    balance.AccountType,
					Version:        balance.Version,
					CreatedAt:      balance.CreatedAt,
					UpdatedAt:      balance.UpdatedAt,
				}

				newBalances = append(newBalances, &ac)

				break
			}
		}
	}

	result <- newBalances
}

// SplitAlias function to split alias with index
func SplitAlias(alias string) string {
	if strings.Contains(alias, "#") {
		return strings.Split(alias, "#")[1]
	}

	return alias
}

// ConcatAlias function to concat alias with index
func ConcatAlias(i int, alias string) string {
	return strconv.Itoa(i) + "#" + alias
}

// OperateBalances Function to sum or sub two balances and Normalize the scale
func OperateBalances(amount Amount, balance Balance) (Balance, error) {
	var (
		total       decimal.Decimal
		totalOnHold decimal.Decimal
	)

	total = balance.Available
	totalOnHold = balance.OnHold

	switch amount.Operation {
	case constant.DEBIT:
		total = balance.Available.Sub(amount.Value)
	case constant.CREDIT:
		total = balance.Available.Add(amount.Value)
	case constant.ONHOLD:
		total = balance.Available.Sub(amount.Value)
		totalOnHold = balance.OnHold.Add(amount.Value)
	case constant.RELEASE:
		totalOnHold = balance.OnHold.Sub(amount.Value)
		total = balance.Available.Add(amount.Value)
	}

	return Balance{
		Available: total,
		OnHold:    totalOnHold,
	}, nil
}

// DetermineOperation Function to determine the operation
func DetermineOperation(isPending bool, isFrom bool, transactionType string) string {
	if isPending && isFrom && transactionType == constant.PENDING {
		return constant.ONHOLD
	} else if isPending && isFrom && transactionType == constant.CANCELED {
		return constant.RELEASE
	} else if isPending && !isFrom && transactionType == constant.PENDING {
		return constant.CREDIT
	} else if !isPending && isFrom {
		return constant.DEBIT
	} else if !isPending && !isFrom {
		return constant.CREDIT
	}

	return ""
}

// CalculateTotal Calculate total for sources/destinations based on shares, amounts and remains
func CalculateTotal(fromTos []FromTo, transaction Transaction, transactionType string, t chan decimal.Decimal, ft chan map[string]Amount, sd chan []string) {
	fmto := make(map[string]Amount)
	scdt := make([]string, 0)

	total := Amount{
		Asset: transaction.Send.Asset,
		Value: decimal.NewFromInt(0),
	}

	remaining := Amount{
		Asset: transaction.Send.Asset,
		Value: transaction.Send.Value,
	}

	for i := range fromTos {
		operation := DetermineOperation(transaction.Pending, fromTos[i].IsFrom, transactionType)

		if fromTos[i].Share != nil && fromTos[i].Share.Percentage != 0 {
			oneHundred := decimal.NewFromInt(100)

			percentage := decimal.NewFromInt(fromTos[i].Share.Percentage)

			percentageOfPercentage := decimal.NewFromInt(fromTos[i].Share.PercentageOfPercentage)
			if percentageOfPercentage.IsZero() {
				percentageOfPercentage = oneHundred
			}

			firstPart := percentage.Div(oneHundred)
			secondPart := percentageOfPercentage.Div(oneHundred)
			shareValue := transaction.Send.Value.Mul(firstPart).Mul(secondPart)

			fmto[fromTos[i].AccountAlias] = Amount{
				Asset:     transaction.Send.Asset,
				Value:     shareValue,
				Operation: operation,
			}

			total.Value = total.Value.Add(shareValue)
		}

		if fromTos[i].Amount != nil && fromTos[i].Amount.Value.IsPositive() {
			amount := Amount{
				Asset:     fromTos[i].Amount.Asset,
				Value:     fromTos[i].Amount.Value,
				Operation: operation,
			}

			fmto[fromTos[i].AccountAlias] = amount
			total.Value = total.Value.Add(amount.Value)
		}

		if !commons.IsNilOrEmpty(&fromTos[i].Remaining) {
			total.Value = total.Value.Add(remaining.Value)

			remaining.Operation = operation

			fmto[fromTos[i].AccountAlias] = remaining
			fromTos[i].Amount = &remaining
		}

		scdt = append(scdt, fromTos[i].SplitAlias())
	}

	t <- total.Value
	ft <- fmto
	sd <- scdt
}

// AppendIfNotExist Append if not exist
func AppendIfNotExist(slice []string, s []string) []string {
	for _, v := range s {
		if !commons.Contains(slice, v) {
			slice = append(slice, v)
		}
	}

	return slice
}

// ValidateSendSourceAndDistribute Validate send and distribute totals
func ValidateSendSourceAndDistribute(transaction Transaction, transactionType string) (*Responses, error) {
	var (
		sourcesTotal      decimal.Decimal
		destinationsTotal decimal.Decimal
	)

	response := &Responses{
		Total:        transaction.Send.Value,
		Asset:        transaction.Send.Asset,
		From:         make(map[string]Amount),
		To:           make(map[string]Amount),
		Sources:      make([]string, 0),
		Destinations: make([]string, 0),
		Aliases:      make([]string, 0),
		Pending:      transaction.Pending,
	}

	t := make(chan decimal.Decimal)
	ft := make(chan map[string]Amount)
	sd := make(chan []string)

	go CalculateTotal(transaction.Send.Source.From, transaction, transactionType, t, ft, sd)
	sourcesTotal = <-t
	response.From = <-ft
	response.Sources = <-sd
	response.Aliases = AppendIfNotExist(response.Aliases, response.Sources)

	go CalculateTotal(transaction.Send.Distribute.To, transaction, transactionType, t, ft, sd)
	destinationsTotal = <-t
	response.To = <-ft
	response.Destinations = <-sd
	response.Aliases = AppendIfNotExist(response.Aliases, response.Destinations)

	for i, source := range response.Sources {
		if _, ok := response.To[ConcatAlias(i, source)]; ok {
			return nil, commons.ValidateBusinessError(constant.ErrTransactionAmbiguous, "ValidateSendSourceAndDistribute")
		}
	}

	for i, destination := range response.Destinations {
		if _, ok := response.From[ConcatAlias(i, destination)]; ok {
			return nil, commons.ValidateBusinessError(constant.ErrTransactionAmbiguous, "ValidateSendSourceAndDistribute")
		}
	}

	if !sourcesTotal.Equal(destinationsTotal) || !destinationsTotal.Equal(response.Total) {
		return nil, commons.ValidateBusinessError(constant.ErrTransactionValueMismatch, "ValidateSendSourceAndDistribute")
	}

	return response, nil
}
