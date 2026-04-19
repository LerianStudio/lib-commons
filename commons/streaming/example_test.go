//go:build unit

package streaming_test

import (
	"context"
	"fmt"

	"github.com/LerianStudio/lib-commons/v5/commons/streaming"
)

// Example_basicUsage shows the minimal Emit path exercised against a
// MockEmitter. This is the 10-line bootstrap pattern from the package
// godoc, compressed for an executable example.
func Example_basicUsage() {
	mock := streaming.NewMockEmitter()

	if err := mock.Emit(context.Background(), streaming.Event{
		TenantID:     "t-abc",
		ResourceType: "transaction",
		EventType:    "created",
		Source:       "//lerian.midaz/transaction-service",
		Subject:      "tx-123",
		Payload:      []byte(`{"amount":100}`),
	}); err != nil {
		return
	}

	fmt.Println(len(mock.Events()))
	// Output: 1
}
