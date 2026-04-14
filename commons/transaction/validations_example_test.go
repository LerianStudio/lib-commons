//go:build unit

package transaction_test

import (
	"fmt"

	"github.com/LerianStudio/lib-commons/v5/commons/transaction"
	"github.com/shopspring/decimal"
)

func ExampleBuildIntentPlan() {
	total := decimal.NewFromInt(100)

	input := transaction.TransactionIntentInput{
		Asset:   "USD",
		Total:   total,
		Pending: false,
		Sources: []transaction.Allocation{{
			Target: transaction.LedgerTarget{AccountID: "acc-src", BalanceID: "bal-src"},
			Amount: &total,
		}},
		Destinations: []transaction.Allocation{{
			Target: transaction.LedgerTarget{AccountID: "acc-dst", BalanceID: "bal-dst"},
			Amount: &total,
		}},
	}

	plan, err := transaction.BuildIntentPlan(input, transaction.StatusCreated)

	fmt.Println(err == nil)
	fmt.Println(plan.Sources[0].Operation, plan.Destinations[0].Operation)

	// Output:
	// true
	// DEBIT CREDIT
}
