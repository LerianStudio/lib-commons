//go:build unit

package log_test

import (
	"fmt"

	ulog "github.com/LerianStudio/lib-commons/v4/commons/log"
)

func ExampleParseLevel() {
	level, err := ulog.ParseLevel("warning")

	fmt.Println(err == nil)
	fmt.Println(level.String())

	// Output:
	// true
	// warn
}
