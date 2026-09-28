//go:build unit

package safe_test

import (
	"errors"
	"fmt"

	"github.com/LerianStudio/lib-commons/v7/commons/safe"
	"github.com/shopspring/decimal"
)

func ExampleDivide() {
	result, err := safe.Divide(decimal.NewFromInt(25), decimal.NewFromInt(5))

	fmt.Println(err == nil)
	fmt.Println(result.String())

	// Output:
	// true
	// 5
}

func ExampleParseDecimal() {
	amount, err := safe.ParseDecimal("12500.50")

	fmt.Println(amount.StringFixed(2), err)

	_, err = safe.ParseDecimal("1e-1001")

	fmt.Println(errors.Is(err, safe.ErrDecimalOutOfBounds))

	// Output:
	// 12500.50 <nil>
	// true
}
