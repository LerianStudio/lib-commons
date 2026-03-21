//go:build unit

package opentelemetry_test

import (
	"fmt"
	"sort"
	"strings"

	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
)

func ExampleBuildAttributesFromValue() {
	type payload struct {
		ID        string `json:"id"`
		RiskScore int    `json:"risk_score"`
	}

	attrs, err := opentelemetry.BuildAttributesFromValue("customer", payload{
		ID:        "cst_123",
		RiskScore: 8,
	}, nil)

	keys := make([]string, 0, len(attrs))
	for _, kv := range attrs {
		keys = append(keys, string(kv.Key))
	}
	sort.Strings(keys)

	fmt.Println(err == nil)
	fmt.Println(strings.Join(keys, ","))

	// Output:
	// true
	// customer.id,customer.risk_score
}
