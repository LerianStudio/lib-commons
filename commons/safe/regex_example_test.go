//go:build unit

package safe_test

import (
	"errors"
	"fmt"

	"github.com/LerianStudio/lib-commons/v4/commons/safe"
)

func ExampleCompile_errorHandling() {
	_, err := safe.Compile("[")

	fmt.Println(errors.Is(err, safe.ErrInvalidRegex))

	// Output:
	// true
}
