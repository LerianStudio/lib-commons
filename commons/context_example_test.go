//go:build unit

package commons_test

import (
	"context"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons"
)

func ExampleWithTimeoutSafe() {
	ctx := context.Background()

	timeoutCtx, cancel, err := commons.WithTimeoutSafe(ctx, 100*time.Millisecond)
	if cancel != nil {
		defer cancel()
	}

	_, hasDeadline := timeoutCtx.Deadline()

	fmt.Println(err == nil)
	fmt.Println(hasDeadline)

	// Output:
	// true
	// true
}
