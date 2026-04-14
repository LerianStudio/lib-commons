//go:build unit

package transaction_test

import (
	"errors"
	"fmt"

	"github.com/LerianStudio/lib-commons/v5/commons/transaction"
)

func ExampleNewDomainError() {
	err := transaction.NewDomainError(transaction.ErrorInvalidInput, "asset", "asset is required")

	var domainErr transaction.DomainError
	ok := errors.As(err, &domainErr)

	fmt.Println(ok)
	fmt.Println(domainErr.Code, domainErr.Field)

	// Output:
	// true
	// 1001 asset
}
