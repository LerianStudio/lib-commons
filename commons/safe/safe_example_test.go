//go:build unit

package safe_test

import (
	"fmt"

	"github.com/LerianStudio/lib-commons/v5/commons/safe"
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
